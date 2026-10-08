package sluispulumi

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	sluisconfig "github.com/truvity/sluis/config"
	"github.com/truvity/sluis/storage/keys"
)

// The documents of the configuration layer, by the name each has in it.
const (
	docSluis  = "sluis"
	docPolicy = "policy"
)

// The names of the configuration secrets this library writes, in layout v3.
const (
	stateSecretName      = "issuer/state-secret"
	recoveryPasswordName = "recovery/password"
)

// renderDocuments renders the two documents the configuration layer holds and
// holds each to sluis's own loader, the one the function runs at cold start: a
// layer outlives the deploy that published it, so a document the binary would
// refuse is refused here, before anything is created.
//
// The documents come from LambdaArgs.Installation, rendered by
// github.com/truvity/sluis/config (the renderer `sluisctl render` runs), or,
// deprecated, from Config and Policy or PolicyPath, which are checked as
// follows. An installation is rendered into the same Config and Policy first,
// so that one set of checks covers both ways in.
//
// Into the service document the library writes what is its own: the apiVersion
// (v3), `policy.file`, `secrets` (ssm, the installation's root), the recovery
// password's and the state secret's names, `recovery.enabled`,
// `signingKey.verifyOnly` (from VerifyOnly), and, for the `invoke` trigger, the
// function it invokes. A value written in the document
// that disagrees with the library's is refused, naming it.
func renderDocuments(a *LambdaArgs) (map[string]string, error) {
	root := SSMRoot(a.Instance)
	out := map[string]string{}
	doc, err := yamlMap(a.Config, "Config")
	if err != nil {
		return nil, err
	}
	original, err := yamlMap(a.Config, "Config")
	if err != nil {
		return nil, err
	}
	if err = own(doc, "Config", "apiVersion", sluisconfig.APIVersion("sluis")); err != nil {
		return nil, err
	}
	policy, err := child(doc, "Config", "policy")
	if err != nil {
		return nil, err
	}
	if err = own(policy, "Config: policy", "file", DocumentPath(docPolicy)); err != nil {
		return nil, err
	}
	if err = ownServe(doc, a, root); err != nil {
		return nil, err
	}
	if !a.AllowEndpoints {
		if at := endpointIn(doc, ""); at != "" {
			return nil, fmt.Errorf("sluispulumi: LambdaArgs.Config names an endpoint (%s): the function reaches AWS at its own "+
				"endpoints; AllowEndpoints is for a test against LocalStack", at)
		}
	}
	if a.Installation != nil && !reflect.DeepEqual(doc, original) {
		return nil, errors.New("sluispulumi: LambdaArgs.Installation: the rendered service document and the library disagree " +
			"about a key the library owns: this is a bug in the library")
	}
	added, err := ownRuntime(doc, a)
	if err != nil {
		return nil, err
	}
	if a.Installation != nil && !added {
		// The renderer wrote this document, and the library's own keys with it:
		// they are held to be what the library would write, and the bytes are
		// the renderer's own, so that `sluisctl render` shows what the function
		// reads.
		out[docSluis] = a.Config
	} else {
		raw, err := yaml.Marshal(doc)
		if err != nil {
			return nil, err
		}
		out[docSluis] = string(raw)
	}
	policyDoc, err := renderPolicy(a)
	if err != nil {
		return nil, err
	}
	out[docPolicy] = policyDoc
	if err = validateDocuments(out); err != nil {
		return nil, err
	}
	return out, nil
}

// ownRuntime writes what the estate supplies and the runtime reads, beyond what
// the renderer knows: `instance` and the `keys:` block (LambdaArgs.Keys), and
// `ports.blob` for external blobs (StorageArgs.Blobs). It reports whether it
// wrote anything. A document that already names one of them is refused: the
// library owns it. The external endpoint is the library's own and is the one
// endpoint a document may carry without AllowEndpoints.
func ownRuntime(doc map[string]any, a *LambdaArgs) (bool, error) {
	added := false
	if a.Keys != nil {
		// A document rendered from an Installation already carries the library's
		// own block (withKeys), and that alone may be there.
		if cur, set := doc["keys"]; set && (a.Installation == nil || !reflect.DeepEqual(cur, a.Keys.keysBlock())) {
			return false, errors.New("sluispulumi: LambdaArgs.Config names keys and LambdaArgs.Keys is set: leave it out, the library writes it")
		}
		doc["keys"] = a.Keys.keysBlock()
		if alias := a.Keys.Secrets; alias != "" {
			secrets, err := child(doc, "Config", "secrets")
			if err != nil {
				return false, err
			}
			if err := own(secrets, "Config: secrets", "kmsKeyId", alias); err != nil {
				return false, err
			}
			if err := ownSecretsAdapterKey(doc, alias); err != nil {
				return false, err
			}
		}
		if err := own(doc, "Config", "instance", a.Instance); err != nil {
			return false, err
		}
		added = true
	}
	if b := a.Storage.External; b != nil {
		ports, err := child(doc, "Config", "ports")
		if err != nil {
			return false, err
		}
		if _, set := ports["blob"]; set {
			return false, errors.New("sluispulumi: LambdaArgs.Config names ports.blob and the storage's Blobs is set: leave it out, the library writes it")
		}
		if ad, ok := doc["adapters"].(map[string]any); ok {
			if _, set := ad["blobs"]; set {
				return false, errors.New("sluispulumi: LambdaArgs.Config names adapters.blobs and the storage's Blobs is set: leave it out")
			}
		}
		ports["blob"] = b.portsBlob()
		added = true
	}
	return added, nil
}

// withInstallation renders LambdaArgs.Installation into Config and Policy, the
// arguments the rest of the library reads, after completing the installation
// with what the arguments say and refusing one that says another: the library
// owns the shape, and the instance, region, account and function name are one
// fact stated once.
func (a LambdaArgs) withInstallation() (LambdaArgs, error) {
	if strings.TrimSpace(a.Config) != "" || strings.TrimSpace(a.Policy) != "" || a.PolicyPath != "" {
		return a, errors.New("sluispulumi: LambdaArgs.Installation replaces Config, Policy and PolicyPath: set the installation, or the documents, not both")
	}
	in := *a.Installation
	aws := sluisconfig.AWS{}
	if in.AWS != nil {
		aws = *in.AWS
	}
	in.AWS = &aws
	var errs []error
	fill := func(what, installation string, arg string, set func(string)) {
		switch {
		case installation == "":
			set(arg)
		case arg != "" && arg != installation:
			errs = append(errs, fmt.Errorf("sluispulumi: LambdaArgs.%s is %q and the installation's is %q: say it once", what, arg, installation))
		}
	}
	switch in.Shape {
	case "":
		in.Shape = sluisconfig.ShapeLambda
	case sluisconfig.ShapeLambda:
	default:
		errs = append(errs, fmt.Errorf("sluispulumi: LambdaArgs.Installation.Shape is %q: the library deploys shape lambda", in.Shape))
	}
	fill("Instance", in.Instance, a.Instance, func(v string) { in.Instance = v })
	fill("Region", aws.Region, a.Region, func(v string) { aws.Region = v })
	fill("AccountID", aws.Account, a.AccountID, func(v string) { aws.Account = v })
	fn := a.FunctionName
	if fn == "" {
		fn = a.FunctionNamePrefix
	}
	if fn == "" {
		fn = sluisconfig.DefaultFunctionName
	}
	fill("FunctionName", aws.FunctionName, a.FunctionName, func(string) { aws.FunctionName = fn })
	if err := errors.Join(errs...); err != nil {
		return a, err
	}
	if a.Instance == "" {
		a.Instance = in.Instance
	}
	if a.Region == "" {
		a.Region = aws.Region
	}
	if a.AccountID == "" {
		a.AccountID = aws.Account
	}
	if a.FunctionName == "" {
		a.FunctionName = aws.FunctionName
	}
	if r := a.Recovery; r != nil && r.Enabled != nil {
		rec := sluisconfig.Recovery{}
		if in.Recovery != nil {
			rec = *in.Recovery
		}
		if rec.Enabled != nil && *rec.Enabled != *r.Enabled {
			return a, fmt.Errorf("sluispulumi: LambdaArgs.Recovery.Enabled is %v and the installation's recovery.enabled is %v: say it once", *r.Enabled, *rec.Enabled)
		}
		rec.Enabled = r.Enabled
		in.Recovery = &rec
	}
	// With an installation an empty audience is never "any audience": it is the
	// console's, the one the controllers' token is minted for.
	aud := sluisconfig.ConsoleAudience(&in)
	switch {
	case a.WebIdentityAudience == "":
		a.WebIdentityAudience = aud
	case a.WebIdentityAudience != aud:
		return a, fmt.Errorf("sluispulumi: LambdaArgs.WebIdentityAudience is %q and the installation's console audience is %q: "+
			"leave it out, or say it once", a.WebIdentityAudience, aud)
	}
	if err := withVerifyOnly(&in, a.VerifyOnly); err != nil {
		return a, err
	}
	if k := a.Keys; k != nil && k.Secrets != "" {
		sec := sluisconfig.Secrets{}
		if in.Secrets != nil {
			sec = *in.Secrets
		}
		if sec.KMSKeyID != "" && sec.KMSKeyID != k.Secrets {
			return a, fmt.Errorf("sluispulumi: LambdaArgs.Keys.Secrets is %q and the installation's secrets.kmsKeyId is %q: say it once",
				k.Secrets, sec.KMSKeyID)
		}
		sec.KMSKeyID = k.Secrets
		in.Secrets = &sec
	}
	if err := withKeys(&in, a.Keys); err != nil {
		return a, err
	}
	if a.ParameterKeyArn != "" {
		sec := sluisconfig.Secrets{}
		if in.Secrets != nil {
			sec = *in.Secrets
		}
		if sec.KMSKeyID != "" && sec.KMSKeyID != a.ParameterKeyArn {
			return a, fmt.Errorf("sluispulumi: LambdaArgs.ParameterKeyArn is %q and the installation's secrets.kmsKeyId is %q: say it once",
				a.ParameterKeyArn, sec.KMSKeyID)
		}
		sec.KMSKeyID = a.ParameterKeyArn
		in.Secrets = &sec
	}
	plan, err := a.planAudit()
	if err != nil {
		return a, err
	}
	a.audit = plan
	if err := withAudit(&in, plan); err != nil {
		return a, err
	}
	service, policy, err := sluisconfig.Render(&in)
	if err != nil {
		return a, fmt.Errorf("sluispulumi: LambdaArgs.Installation: %w", err)
	}
	a.Config, a.Policy = string(service), string(policy)
	return a, nil
}

// withKeys makes the installation carry exactly the keys the library supplies
// (LambdaArgs.Keys). Without it the renderer would default `keys.sign` for a
// kmsWrapped signer that names no key, and the default would differ from the
// estate's. An installation that names another `keys.sign` is refused; one that
// names more than the library writes is left as it is, and refused with the
// document it renders.
func withKeys(in *sluisconfig.Installation, k *KeysArgs) error {
	if k == nil {
		return nil
	}
	if c := in.Keys; c != nil {
		if got := c.Keys[keys.Sign].Key; got != "" && got != k.Sign {
			return fmt.Errorf("sluispulumi: LambdaArgs.Keys.Sign is %q and the installation's keys.sign is %q: say it once", k.Sign, got)
		}
		if len(c.Keys) > 1 {
			return nil
		}
	}
	in.Keys = &keys.Config{Adapter: "kms", Keys: map[keys.Purpose]keys.Entry{keys.Sign: {Key: k.Sign}}}
	return nil
}

// ownServe writes the service document's library-owned keys.
func ownServe(doc map[string]any, a *LambdaArgs, root string) error {
	secrets, err := child(doc, "Config", "secrets")
	if err != nil {
		return err
	}
	if err = own(secrets, "Config: secrets", "source", "ssm"); err != nil {
		return err
	}
	if err = own(secrets, "Config: secrets", "root", root); err != nil {
		return err
	}
	if err = own(secrets, "Config: secrets", "region", a.Region); err != nil {
		return err
	}
	if a.ParameterKeyArn != "" {
		// The key the function's own writes use: the ssm adapter reads it from
		// here (secrets.kmsKeyId), and an adapter that names another is refused.
		if err = own(secrets, "Config: secrets", "kmsKeyId", a.ParameterKeyArn); err != nil {
			return err
		}
		if err = ownSecretsAdapterKey(doc, a.ParameterKeyArn); err != nil {
			return err
		}
	}
	recovery, err := child(doc, "Config", "recovery")
	if err != nil {
		return err
	}
	if err = own(recovery, "Config: recovery", "passwordSecret", recoveryPasswordName); err != nil {
		return err
	}
	if a.Recovery != nil && a.Recovery.Enabled != nil {
		if v, set := recovery["enabled"]; set && v != *a.Recovery.Enabled {
			return fmt.Errorf("sluispulumi: LambdaArgs.Config has recovery.enabled: %v and Recovery.Enabled is %v", v, *a.Recovery.Enabled)
		}
		recovery["enabled"] = *a.Recovery.Enabled
	}
	if err = ownTrigger(doc, a); err != nil {
		return err
	}
	if err = ownVerifyOnly(doc, a); err != nil {
		return err
	}
	if signing, ok := doc["signingKey"].(map[string]any); ok && a.WrappedSigning != nil {
		if _, remote := signing["kms"]; remote {
			return errors.New("sluispulumi: LambdaArgs.Config names signingKey.kms and WrappedSigning is set: " +
				"the library declares no asymmetric key with wrapped signing; name signingKey.kmsWrapped")
		}
	}
	if signing, ok := doc["signingKey"].(map[string]any); ok {
		for _, k := range []string{"kms", "kmsWrapped"} {
			if b, ok := signing[k].(map[string]any); ok {
				if err = own(b, "Config: signingKey."+k, "stateSecret", stateSecretName); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ownSecretsAdapterKey refuses an `adapters.secrets` ssm adapter that names a
// key other than the library's; the same value, or none, is accepted.
func ownSecretsAdapterKey(doc map[string]any, key string) error {
	adapters, ok := doc["adapters"].(map[string]any)
	if !ok {
		return nil
	}
	secrets, ok := adapters["secrets"].(map[string]any)
	if !ok || secrets["adapter"] != "ssm" {
		return nil
	}
	settings, ok := secrets["settings"].(map[string]any)
	if !ok {
		return nil
	}
	if v, set := settings["kmsKeyId"]; set && v != key {
		return fmt.Errorf("sluispulumi: LambdaArgs.Config: adapters.secrets.settings.kmsKeyId is %v and ParameterKeyArn is %q: say it once", v, key)
	}
	return nil
}

// ownTrigger writes the function a run-now invokes into `adapters.trigger` when
// the document chooses the `invoke` adapter: it is this function, for both kinds
// (the function runs the pass under the controller the policy says the target
// belongs to), so the library names it and a document that names another is
// refused.
func ownTrigger(doc map[string]any, a *LambdaArgs) error {
	adapters, ok := doc["adapters"].(map[string]any)
	if !ok {
		return nil
	}
	trigger, ok := adapters["trigger"].(map[string]any)
	if !ok || trigger["adapter"] != "invoke" {
		return nil
	}
	settings, err := child(trigger, "Config: adapters.trigger", "settings")
	if err != nil {
		return err
	}
	// A run-now invokes the alias, so that it runs the version the schedules and
	// the API run. The document may name the function (as a rendered
	// installation does) or the alias; anything else is another function.
	live := a.FunctionName + ":" + LiveAlias
	for _, kind := range []string{"github", "slack"} {
		if v, set := settings[kind]; set && v != a.FunctionName && v != live {
			return fmt.Errorf("sluispulumi: LambdaArgs.Config: adapters.trigger.settings.%s is %v, and the library writes %s: leave it out", kind, v, live)
		}
		settings[kind] = live
	}
	return nil
}

// renderPolicy is the canonical policy document, from Policy (a document) or
// PolicyPath (a file or a directory of layers, rendered by sluis's own
// renderer).
func renderPolicy(a *LambdaArgs) (string, error) {
	var doc *sluisconfig.PolicyDocument
	switch {
	case strings.TrimSpace(a.Policy) != "":
		dir, err := os.MkdirTemp("", "sluis-policy-")
		if err != nil {
			return "", err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		file := filepath.Join(dir, "policy.yaml")
		if err = os.WriteFile(file, []byte(a.Policy), 0o600); err != nil {
			return "", err
		}
		if doc, err = sluisconfig.Load[sluisconfig.PolicyDocument](file); err != nil {
			return "", fmt.Errorf("sluispulumi: LambdaArgs.Policy: %w", err)
		}
	default:
		var err error
		if doc, err = sluisconfig.RenderPolicyLayers(a.PolicyPath); err != nil {
			return "", fmt.Errorf("sluispulumi: LambdaArgs.PolicyPath: %w", err)
		}
	}
	raw, err := doc.Encode()
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// validateDocuments loads each rendered document as the function will.
func validateDocuments(docs map[string]string) error {
	dir, err := os.MkdirTemp("", "sluis-layer-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := func(name string) (string, error) {
		p := filepath.Join(dir, name+".yaml")
		return p, os.WriteFile(p, []byte(docs[name]), 0o600)
	}
	var errs []error
	for name, load := range map[string]func(string) error{
		docSluis:  func(p string) error { _, err := sluisconfig.Load[sluisconfig.Sluis](p); return err },
		docPolicy: func(p string) error { _, err := sluisconfig.Load[sluisconfig.PolicyDocument](p); return err },
	} {
		p, err := path(name)
		if err != nil {
			return err
		}
		if err = load(p); err != nil {
			errs = append(errs, fmt.Errorf("sluispulumi: the %s document: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func yamlMap(body, field string) (map[string]any, error) {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		return nil, fmt.Errorf("sluispulumi: LambdaArgs.%s is not YAML: %w", field, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

func child(m map[string]any, where, key string) (map[string]any, error) {
	v, set := m[key]
	if !set || v == nil {
		c := map[string]any{}
		m[key] = c
		return c, nil
	}
	c, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("sluispulumi: LambdaArgs.%s: %s is not a mapping", where, key)
	}
	return c, nil
}

// own sets a key the library owns, refusing a different value already there.
func own(m map[string]any, where, key, value string) error {
	if v, set := m[key]; set && v != value {
		return fmt.Errorf("sluispulumi: LambdaArgs.%s: %s is %v, and the library writes %s: leave it out", where, key, v, value)
	}
	m[key] = value
	return nil
}

// SSMRoot is an installation's SSM root in layout v3: `/sluis/<instance>`.
func SSMRoot(instance string) string { return "/sluis/" + instance }

var instancePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// validInstance is an instance name: lower-case letters, digits and dashes, and
// never `private` or `export`, which would nest its tree under another's (or
// under layout v2's /sluis/private and /sluis/export).
func validInstance(s string) bool {
	return instancePattern.MatchString(s) && s != "private" && s != "export"
}

// endpointIn is the path of the first `endpoint` key under v, or "".
func endpointIn(v any, at string) string {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			p := k
			if at != "" {
				p = at + "." + k
			}
			if s, ok := x.(string); k == "endpoint" && ok && s != "" {
				return p
			}
			if found := endpointIn(x, p); found != "" {
				return found
			}
		}
	case []any:
		for i, x := range t {
			if found := endpointIn(x, fmt.Sprintf("%s[%d]", at, i)); found != "" {
				return found
			}
		}
	}
	return ""
}

// MinPackageVersion is the oldest release this library deploys: the first that
// runs as one function (the v3 service document, SLUIS_CONFIG naming it, no
// SLUIS_ROLE). An older package does not start on what this library renders.
const MinPackageVersion = "1.63"

// The release zip's name: sluis-lambda_<version>_linux_<arch>.zip.
var packageName = regexp.MustCompile(`^sluis-lambda_v?([0-9]+)\.([0-9]+)\.[0-9]+[^_]*_linux_[a-z0-9]+\.zip$`)

// checkVersion refuses a package older than this library: older than
// MinPackageVersion, and older than this library's own minor when the build
// knows it (the library and the binary are released together, at one version).
func checkVersion(pkg, explicit string) error {
	major, minor, err := packageVersion(pkg, explicit)
	if err != nil {
		return err
	}
	floor := []string{MinPackageVersion}
	if own := libraryMinor(); own != "" {
		floor = append(floor, own)
	}
	for _, f := range floor {
		fm, fn, _ := majorMinor(f)
		if major < fm || (major == fm && minor < fn) {
			return fmt.Errorf("sluispulumi: the package is sluis %d.%d, older than %s, which this library deploys at the least: "+
				"deploy the release of the same version as this library", major, minor, f)
		}
	}
	return nil
}

func packageVersion(pkg, explicit string) (int, int, error) {
	if explicit != "" {
		major, minor, err := majorMinor(strings.TrimPrefix(explicit, "v"))
		if err != nil {
			return 0, 0, fmt.Errorf("sluispulumi: LambdaArgs.PackageVersion %q is not a version", explicit)
		}
		return major, minor, nil
	}
	base := pkg
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	m := packageName.FindStringSubmatch(base)
	if m == nil {
		return 0, 0, fmt.Errorf("sluispulumi: the package %q is not named sluis-lambda_<version>_linux_<arch>.zip: "+
			"set LambdaArgs.PackageVersion to the release it is", base)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major, minor, nil
}

func majorMinor(v string) (int, int, error) {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, 0, errors.New("not a version")
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, err
	}
	return major, minor, nil
}

// libraryMinor is this library's own released minor, "1.62", from the build's
// module list; empty in a build of its own source or a pseudo-version.
func libraryMinor() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, m := range info.Deps {
		if m.Path != "github.com/truvity/sluis/deploy/pulumi" {
			continue
		}
		v := strings.TrimPrefix(m.Version, "v")
		if strings.Contains(v, "-") {
			return ""
		}
		if major, minor, err := majorMinor(v); err == nil {
			return strconv.Itoa(major) + "." + strconv.Itoa(minor)
		}
	}
	return ""
}

// withAudit writes the audit adapter the plan decides into the installation:
// the queue the records are published to (installed here, or Audit.Use's), or
// the `log` adapter when audit is off. With the deprecated AuditQueueArn the
// estate wrote it and nothing is added. The installation naming another queue
// or adapter is refused: the fact is stated once.
func withAudit(in *sluisconfig.Installation, p *auditPlan) error {
	aws := *in.AWS
	switch p.mode {
	case auditInstall, auditUse:
		if aws.AuditQueueURL != "" && aws.AuditQueueURL != p.queueURL {
			return fmt.Errorf("sluispulumi: LambdaArgs.Audit publishes to %s and the installation's aws.auditQueueURL is %s: say it once",
				p.queueURL, aws.AuditQueueURL)
		}
		if c, ok := in.Adapters["audit"]; ok && c.Adapter != "sqs" {
			return fmt.Errorf("sluispulumi: LambdaArgs.Audit publishes to SQS and the installation's adapters.audit is %q: say it once", c.Adapter)
		}
		aws.AuditQueueURL = p.queueURL
	case auditOff:
		if aws.AuditQueueURL != "" {
			return errors.New("sluispulumi: LambdaArgs.Audit.Enabled is false and the installation's aws.auditQueueURL names a queue: say it once")
		}
		if c, ok := in.Adapters["audit"]; ok && c.Adapter != "log" {
			return fmt.Errorf("sluispulumi: LambdaArgs.Audit.Enabled is false and the installation's adapters.audit is %q: say it once", c.Adapter)
		}
		adapters := make(map[string]sluisconfig.AdapterChoice, len(in.Adapters)+1)
		for k, v := range in.Adapters {
			adapters[k] = v
		}
		adapters["audit"] = sluisconfig.AdapterChoice{Adapter: "log"}
		in.Adapters = adapters
	}
	in.AWS = &aws
	return nil
}
