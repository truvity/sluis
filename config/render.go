package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	internal "github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/storage/keys"
)

// DefaultSignKeyAlias is the alias of an installation's `keys.sign` key when the
// installation names none: the one the Pulumi library's grants are written for.
func DefaultSignKeyAlias(instance string) string { return "alias/sluis-" + instance + "-sign" }

// The names layout v3 gives the secrets sluis itself writes. They are fixed,
// so an installation never repeats them.
const (
	// StateSecretName is the issuer's sign-in state secret.
	StateSecretName = "issuer/state-secret"
	// RecoveryPasswordName is the recovery password, where there is no cluster
	// to prove access to.
	RecoveryPasswordName = "recovery/password"
)

// DefaultFunctionName is the Lambda function an installation that names none
// is rendered for.
const DefaultFunctionName = "sluis"

// LiveAlias is the alias of the Lambda function that every caller uses: the API,
// the schedules and a run-now. The Lambda shape's `invoke` trigger names
// `<function>:live`, so that a run-now runs the version the schedules run.
const LiveAlias = "live"

var instancePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// SSMRoot is the installation's secrets root in layout v3: /sluis/<instance>.
func SSMRoot(instance string) string { return "/sluis/" + instance }

// Render writes the two documents of an installation: the service document
// (`sluis.yaml`, sluis/v3) and the policy document (`policy.yaml`, policy/v2).
//
// It is deterministic: the keys of both are sorted, `apiVersion` comes first,
// and nothing but the installation decides a byte of the output, so a renderer
// run twice, or by `sluisctl render` and by the Pulumi library, writes the same
// documents. It does not change in.
//
// Render writes only what the installation says or the shape implies, and says
// in its error which key is wrong. Both outputs are then loaded by the loader
// the service runs at start (the schema, then the semantic checks), so a
// document Render returns is one the service accepts. What only a running
// process can check, such as an adapter that needs a platform answer the
// platform does not give, is still checked at start.
//
// What the shape implies:
//
//   - the preset, when none is named;
//   - `policy.file`, `publicURL`, `publicRootURL` and `inCluster`;
//   - the adapters the AWS resources and the OpenBao settings stand for, for
//     each concern `adapters` does not name;
//   - for shape lambda, the library-owned names: `secrets` (ssm, the
//     instance's root), `recovery.passwordSecret`, and the `invoke`
//     trigger's function; and for any signing by KMS, the state secret.
//
// A value the installation writes that disagrees with what the shape fixes is
// an error, not a choice.
func Render(in *Installation) (service, policy []byte, err error) {
	if in == nil {
		return nil, nil, errors.New("render: no installation")
	}
	if err := in.check(); err != nil {
		return nil, nil, fmt.Errorf("render: %w", err)
	}
	svc, err := in.service()
	if err != nil {
		return nil, nil, fmt.Errorf("render: %w", err)
	}
	pol, err := in.policyDocument()
	if err != nil {
		return nil, nil, fmt.Errorf("render: %w", err)
	}
	if service, err = encode(svc); err != nil {
		return nil, nil, fmt.Errorf("render: the service document: %w", err)
	}
	if policy, err = encode(pol); err != nil {
		return nil, nil, fmt.Errorf("render: the policy document: %w", err)
	}
	if err := verify(service, policy); err != nil {
		return nil, nil, fmt.Errorf("render: %w", err)
	}
	return service, policy, nil
}

// check is what the schema cannot say about an installation built in Go.
func (in *Installation) check() error {
	var errs []error
	if in.APIVersion != "" && in.APIVersion != InstallationVersion {
		errs = append(errs, fmt.Errorf("apiVersion is %q: this build reads %s", in.APIVersion, InstallationVersion))
	}
	if !instancePattern.MatchString(in.Instance) || in.Instance == "private" || in.Instance == "export" || in.Instance == "internal" || in.Instance == "external" {
		errs = append(errs, fmt.Errorf("instance %q is not lower-case letters, digits and dashes, or is `private`, `export`, `internal` or `external`", in.Instance))
	}
	switch in.Shape {
	case ShapeLambda, ShapeKubernetes, ShapeServer:
	default:
		errs = append(errs, fmt.Errorf("shape %q is none of lambda, kubernetes, server", in.Shape))
	}
	if strings.TrimSpace(in.Issuer.URL) == "" {
		errs = append(errs, errors.New("issuer.url is required: it is baked into every token and every relying party's trust"))
	}
	if in.Shape == ShapeLambda && (in.AWS == nil || in.AWS.Region == "") {
		errs = append(errs, errors.New("aws.region is required for shape lambda: it names the SSM parameters and the resources"))
	}
	if in.Keys != nil {
		if err := in.Keys.Validate(); err != nil {
			errs = append(errs, err)
		}
	}
	if in.Console != nil && in.Console.Client != "" && in.Access != nil && len(in.Access.Clients) > 0 {
		if _, ok := in.Access.Clients[in.Console.Client]; !ok {
			errs = append(errs, fmt.Errorf("console.client names %q, which access.clients does not declare", in.Console.Client))
		}
	}
	if why := port.Preset(in.preset()).Unavailable(); why != "" && len(in.Adapters) < len(port.Concerns) {
		errs = append(errs, fmt.Errorf("preset %s (the shape's, or named): %s; name `adapters` for every concern, or another preset", in.preset(), why))
	}
	for name := range in.Adapters {
		if !slices.Contains([]string{"state", "secrets", "blobs", "signing", "trigger", "schedule", "audit"}, name) {
			errs = append(errs, fmt.Errorf("adapters.%s is not a concern (state, secrets, blobs, signing, trigger, schedule, audit)", name))
		}
	}
	return errors.Join(errs...)
}

// preset is the one named, or the one the shape and what the installation is
// built on lead to.
func (in *Installation) preset() string {
	if in.Preset != "" {
		return in.Preset
	}
	switch in.Shape {
	case ShapeLambda:
		return "aws-hybrid"
	case ShapeKubernetes:
		switch {
		case in.AWS != nil:
			return "k8s-aws"
		case in.OpenBao != nil:
			return "k8s-openbao"
		}
		return "k8s-minimal"
	}
	return "server"
}

func (in *Installation) policyFile() string {
	if in.PolicyFile != "" {
		return in.PolicyFile
	}
	switch in.Shape {
	case ShapeLambda:
		return LambdaPolicyFile
	case ShapeKubernetes:
		return KubernetesPolicyFile
	}
	return ServerPolicyFile
}

func (in *Installation) functionName() string {
	if in.AWS != nil && in.AWS.FunctionName != "" {
		return in.AWS.FunctionName
	}
	return DefaultFunctionName
}

// service is the service document the installation says.
func (in *Installation) service() (*internal.Sluis, error) {
	s := &internal.Sluis{}
	s.APIVersion = internal.APIVersion("sluis")
	s.IssuerURL = strings.TrimSpace(in.Issuer.URL)
	s.Release = in.Release
	s.Instance = in.Instance
	s.Cluster = in.Cluster
	s.InCluster = in.Shape == ShapeKubernetes
	s.Preset = in.preset()
	s.Policy = &internal.PolicyRef{File: in.policyFile()}
	s.PublicRootURL = in.Issuer.RootURL
	if s.PublicRootURL == "" {
		s.PublicRootURL = s.IssuerURL
	}
	s.PublicURL = in.Issuer.ConsoleURL
	if s.PublicURL == "" {
		s.PublicURL = strings.TrimSuffix(s.PublicRootURL, "/") + "/console"
	}
	s.SecureCookies = copyOf(in.Issuer.SecureCookies)
	s.GroupsScoping = in.Issuer.GroupsScoping
	s.Log = copyOf(in.Log)
	s.Lifetimes = copyOf(in.Lifetimes)
	s.Freshness = copyOf(in.Freshness)
	s.Directory = copyOf(in.Directory)
	s.Login = copyOf(in.Login)
	s.Console = copyOf(in.Console)
	s.OAuthClient = copyOf(in.OAuthClient)
	s.Valkey = copyOf(in.Valkey)
	s.Audit = copyOf(in.Audit)
	if in.Cloudflare != nil {
		c := in.Cloudflare.Cloudflare
		s.Cloudflare = &c
	}
	s.Recovery = copyOf(in.Recovery)
	s.SigningKey = copyOf(in.SigningKey)
	s.Keys = copyOf(in.Keys)
	if in.Exchange != nil && in.Exchange.Audience != "" {
		s.Exchange = &internal.Exchange{Audience: in.Exchange.Audience}
	}

	var errs []error
	if err := in.secrets(s); err != nil {
		errs = append(errs, err)
	}
	if err := in.signing(s); err != nil {
		errs = append(errs, err)
	}
	adapters, err := in.adapters()
	if err != nil {
		errs = append(errs, err)
	}
	s.Adapters = adapters
	if err := in.checkAdapters(s, adapters); err != nil {
		errs = append(errs, err)
	}
	s.Controllers = in.serviceControllers()
	return s, errors.Join(errs...)
}

// secrets writes `secrets` and the recovery password's name, which shape lambda
// fixes.
func (in *Installation) secrets(s *internal.Sluis) error {
	s.Secrets = copyOf(in.Secrets)
	if in.Shape != ShapeLambda {
		return nil
	}
	root := SSMRoot(in.Instance)
	want := internal.Secrets{Source: "ssm", Root: root, Region: in.AWS.Region}
	if got := s.Secrets; got != nil {
		var errs []error
		if got.Source != "" && got.Source != want.Source {
			errs = append(errs, fmt.Errorf("secrets.source is %q, and shape lambda reads its secrets from ssm", got.Source))
		}
		if got.Root != "" && got.Root != want.Root {
			errs = append(errs, fmt.Errorf("secrets.root is %q, and the installation's root is %s", got.Root, root))
		}
		if got.Region != "" && got.Region != want.Region {
			errs = append(errs, fmt.Errorf("secrets.region is %q, and aws.region is %q", got.Region, want.Region))
		}
		if len(errs) > 0 {
			return errors.Join(errs...)
		}
		want.Endpoint, want.Refresh, want.KMSKeyID = got.Endpoint, got.Refresh, got.KMSKeyID
		want.Layout, want.Grace = got.Layout, got.Grace
	}
	s.Secrets = &want
	if s.Recovery == nil {
		s.Recovery = &internal.Recovery{}
	}
	if got := s.Recovery.LoginSecret; got != "" && got != RecoveryPasswordName {
		return fmt.Errorf("recovery.passwordSecret is %q, and shape lambda names it %s", got, RecoveryPasswordName)
	}
	s.Recovery.LoginSecret = RecoveryPasswordName
	return nil
}

// signing names the state secret of a KMS signer, which layout v3 fixes.
func (in *Installation) signing(s *internal.Sluis) error {
	k := s.SigningKey
	if k == nil {
		return nil
	}
	if k.KMS != nil && k.KMSWrapped != nil {
		return errors.New("signingKey names kms and kmsWrapped: they are exclusive")
	}
	if k.KMS != nil {
		if k.KMS.StateSecret == "" {
			k.KMS.StateSecret = StateSecretName
		}
		if in.AWS != nil && k.KMS.Region == "" {
			k.KMS.Region = in.AWS.Region
		}
	}
	if w := k.KMSWrapped; w != nil {
		// The ring's wrapping key is `keys.sign`; unnamed, it is the one the
		// Pulumi library's grants are written for.
		if s.Keys == nil && w.KeyID == "" {
			s.Keys = &keys.Config{Adapter: "kms", Keys: map[keys.Purpose]keys.Entry{
				keys.Sign: {Key: DefaultSignKeyAlias(in.Instance)},
			}}
		}
		if w.StateSecret == "" {
			w.StateSecret = StateSecretName
		}
		if in.AWS != nil && w.Region == "" {
			w.Region = in.AWS.Region
		}
	}
	return nil
}

// adapters is `adapters`: what the installation names, and for each concern it
// does not, what the AWS resources and the OpenBao settings stand for.
func (in *Installation) adapters() (map[string]internal.AdapterChoice, error) {
	out := map[string]internal.AdapterChoice{}
	for name, choice := range in.Adapters {
		choice.Settings = maps.Clone(choice.Settings)
		out[name] = choice
	}
	named := func(concern string) bool { _, ok := in.Adapters[concern]; return ok }
	set := func(concern, adapter string, settings map[string]any) {
		if named(concern) {
			return
		}
		for k, v := range settings {
			if v == "" {
				delete(settings, k)
			}
		}
		out[concern] = internal.AdapterChoice{Adapter: adapter, Settings: settings}
	}
	region := ""
	if a := in.AWS; a != nil {
		region = a.Region
		if a.Table != "" {
			set("state", "dynamodb", map[string]any{"table": a.Table, "region": region})
		}
		if a.Bucket != "" {
			set("blobs", "s3", map[string]any{"bucket": a.Bucket, "region": region})
		}
		if a.AuditQueueURL != "" {
			set("audit", "sqs", map[string]any{"queueURL": a.AuditQueueURL, "region": region})
		}
	}
	switch {
	case in.OpenBao != nil:
		o := in.OpenBao
		auth := map[string]any{"method": o.Auth.Method, "role": o.Auth.Role, "mount": o.Auth.Mount, "tokenFile": o.Auth.TokenFile}
		for k, v := range auth {
			if v == "" {
				delete(auth, k)
			}
		}
		set("secrets", "openbao", map[string]any{
			"address": o.Address, "caFile": o.CAFile, "mount": o.Mount, "namespace": o.Namespace, "root": o.Root, "auth": auth,
		})
	case in.AWS != nil && in.Shape != ShapeServer:
		set("secrets", "ssm", map[string]any{"root": SSMRoot(in.Instance), "region": region})
	}
	if in.Shape == ShapeLambda {
		// A run-now invokes this very function, for both controllers.
		fn := in.functionName() + ":" + LiveAlias
		set("trigger", "invoke", map[string]any{"github": fn, "slack": fn})
		if c := out["trigger"]; named("trigger") && c.Adapter == "invoke" && len(c.Settings) == 0 {
			c.Settings = map[string]any{"github": fn, "slack": fn}
			out["trigger"] = c
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// serviceControllers is the service document's `controllers`: each section that
// is present is a controller that is on.
func (in *Installation) serviceControllers() *internal.Controllers {
	if in.Controllers == nil || (in.Controllers.GitHub == nil && in.Controllers.Slack == nil) {
		return nil
	}
	out := &internal.Controllers{}
	if g := in.Controllers.GitHub; g != nil {
		c := copyOf(&g.GitHubController)
		out.GitHub = c
	}
	if g := in.Controllers.Slack; g != nil {
		c := copyOf(&g.SlackController)
		out.Slack = c
	}
	return out
}

// policyDocument is the policy document the installation says.
func (in *Installation) policyDocument() (*internal.PolicyDocument, error) {
	var tables Access
	if in.Access != nil {
		// A copy, so that a check which normalises a table leaves the
		// installation as it was.
		raw, err := in.Access.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("access: %w", err)
		}
		if err := tables.UnmarshalJSON(raw); err != nil {
			return nil, fmt.Errorf("access: %w", err)
		}
	}
	doc := internal.NewPolicyDocument(tables.Policy)
	if x := in.Exchange; x != nil {
		doc.Exchange = x.policyExchange()
	}
	if in.Apps != nil {
		apps := in.Apps.PolicyApps
		doc.Apps = &apps
		if apps.GitHub != nil {
			g := *apps.GitHub
			g.RunnerTiers = sortedSet(g.RunnerTiers)
			doc.Apps.GitHub = &g
		}
	}
	if c := in.Controllers; c != nil {
		pc := &internal.PolicyControllers{}
		if c.GitHub != nil && len(c.GitHub.EnabledOrgs) > 0 {
			pc.GitHub = &internal.ControllersGitHub{EnabledOrgs: sortedSet(c.GitHub.EnabledOrgs)}
		}
		if c.Slack != nil && len(c.Slack.EnabledWorkspaces) > 0 {
			pc.Slack = &internal.ControllersSlack{EnabledWorkspaces: sortedSet(c.Slack.EnabledWorkspaces)}
		}
		if pc.GitHub != nil || pc.Slack != nil {
			doc.Controllers = pc
		}
	}
	if c := in.Cloudflare; c != nil && len(c.Grants) > 0 {
		doc.CloudflareGrants = &internal.PolicyCloudflare{Grants: slices.Clone(c.Grants)}
	}
	return doc, nil
}

func (x *Exchange) policyExchange() *internal.PolicyExchange {
	out := &internal.PolicyExchange{}
	for _, c := range x.Clusters {
		out.Clusters = append(out.Clusters, internal.FederatedCluster{Name: c.Name, Issuer: c.Issuer, JWKSURI: c.JWKSURI})
	}
	if a := x.AWS; a != nil {
		f := &internal.AWSFederation{Audience: a.Audience, MaxAge: copyOf(a.MaxAge)}
		for _, acc := range a.Accounts {
			f.Accounts = append(f.Accounts, internal.AWSAccount{
				Account: acc.Account, Name: acc.Name, Issuer: acc.Issuer, JWKSURI: acc.JWKSURI, OrgID: acc.OrgID, Algs: slices.Clone(acc.Algs),
			})
		}
		out.AWS = f
	}
	if g := x.GitHub; g != nil {
		out.GitHub = &internal.ExchangeGitHub{Owners: sortedSet(g.Owners)}
	}
	if len(out.Clusters) == 0 && out.AWS == nil && out.GitHub == nil {
		return nil
	}
	return out
}

// sortedSet is a list that means a set, in the one order: the output of two
// renders of one set is the same bytes.
func sortedSet(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	sort.Strings(out)
	return slices.Compact(out)
}

// copyOf is a deep copy of a document section, through JSON: the renderer
// completes what it writes and must never change what it was given.
func copyOf[T any](v *T) *T {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err) // a document section always marshals
	}
	out := new(T)
	if err := json.Unmarshal(raw, out); err != nil {
		panic(err)
	}
	return out
}

// encode writes a document as YAML: `apiVersion` first, every other key sorted
// at every level, two-space indent, block style. The document is first taken
// through JSON, which is how every type here spells itself.
func encode(doc any) ([]byte, error) {
	var raw []byte
	var err error
	switch d := doc.(type) {
	case *internal.PolicyDocument:
		return d.Encode()
	default:
		raw, err = json.Marshal(doc)
	}
	if err != nil {
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	normalise(&root, true)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// normalise puts a node read from JSON into the canonical layout: block
// style, and the keys of every mapping sorted (apiVersion first, at the root).
func normalise(n *yaml.Node, root bool) {
	n.Style = 0
	for _, c := range n.Content {
		normalise(c, root && n.Kind == yaml.DocumentNode)
	}
	if n.Kind != yaml.MappingNode {
		return
	}
	type pair struct{ k, v *yaml.Node }
	pairs := make([]pair, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		pairs = append(pairs, pair{n.Content[i], n.Content[i+1]})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		if root {
			if a, b := pairs[i].k.Value == "apiVersion", pairs[j].k.Value == "apiVersion"; a != b {
				return a
			}
		}
		return pairs[i].k.Value < pairs[j].k.Value
	})
	n.Content = n.Content[:0]
	for _, p := range pairs {
		n.Content = append(n.Content, p.k, p.v)
	}
}

// verify loads both documents as the service does: the schema, the decode and,
// for the policy, the semantic checks.
func verify(service, policy []byte) error {
	dir, err := os.MkdirTemp("", "sluis-render-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	write := func(name string, body []byte) (string, error) {
		p := filepath.Join(dir, name)
		return p, os.WriteFile(p, body, 0o600)
	}
	var errs []error
	svcFile, err := write("sluis.yaml", service)
	if err != nil {
		return err
	}
	svc, err := internal.Load[internal.Sluis](svcFile)
	if err != nil {
		errs = append(errs, fmt.Errorf("the service document: %w", err))
	}
	polFile, err := write("policy.yaml", policy)
	if err != nil {
		return err
	}
	pol, err := internal.Load[internal.PolicyDocument](polFile)
	if err != nil {
		errs = append(errs, fmt.Errorf("the policy document: %w", err))
	}
	if svc != nil && pol != nil {
		// A grant for a preset nobody declared would read as a right nobody can use.
		if err := internal.CheckCloudflare(svc.Cloudflare, pol); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// overrides are the adapters a named adapter may be, per concern, when it is
// not the preset's own: the ones a platform really chooses between. Anything
// else contradicts the preset and is refused, not carried into the document.
var overrides = map[string][]string{
	"state":    {"dynamodb"},
	"blobs":    {"s3", "off"},
	"secrets":  {"ssm", "openbao"},
	"signing":  {"kms", "kms-wrapped", "file"},
	"trigger":  {"invoke", "dynamodb", "http"},
	"schedule": {"eventbridge", "ticker"},
	"audit":    {"sqs", "connect", "log"},
}

// credentialWords make a settings key one that would hold a credential's value.
var credentialWords = []string{"token", "password", "passwd", "secret", "credential", "privatekey", "apikey", "accesskey"}

// checkAdapters holds the adapter table the document will carry to what the
// shape and the preset say, and its settings to the rule that a document never
// carries a credential: a key that names one holds a NAME (`...Secret`) or a
// path (`...File`), never a value.
func (in *Installation) checkAdapters(s *internal.Sluis, table map[string]internal.AdapterChoice) error {
	var errs []error
	preset := port.PresetTable(port.Preset(s.Preset))
	for _, concern := range slices.Sorted(maps.Keys(table)) {
		choice := table[concern]
		if want := preset[port.Concern(concern)]; want != "" && choice.Adapter != want && !slices.Contains(overrides[concern], choice.Adapter) {
			errs = append(errs, fmt.Errorf("adapters.%s is %q, which contradicts preset %s (%s): an override is one of %v",
				concern, choice.Adapter, s.Preset, want, overrides[concern]))
		}
		for _, bad := range credentialKeys(choice.Settings, "adapters."+concern+".settings") {
			errs = append(errs, fmt.Errorf("%s names a credential: a document carries the NAME of a secret (...Secret) or a file (...File), never a value", bad))
		}
		if choice.Adapter == "openbao" {
			if addr, _ := choice.Settings["address"].(string); !strings.HasPrefix(addr, "https://") {
				errs = append(errs, fmt.Errorf("adapters.%s.settings.address must be an https URL: a login token crosses this connection", concern))
			}
		}
	}
	if in.Shape == ShapeLambda {
		if c, ok := table["secrets"]; ok && c.Adapter != "ssm" {
			errs = append(errs, fmt.Errorf("adapters.secrets is %q: shape lambda keeps its secrets in ssm", c.Adapter))
		}
		if c, ok := table["signing"]; ok && c.Adapter != "kms-wrapped" && c.Adapter != "kms" {
			errs = append(errs, fmt.Errorf("adapters.signing is %q: shape lambda signs with kms-wrapped or kms", c.Adapter))
		}
		if in.OpenBao != nil {
			errs = append(errs, errors.New("openbao is set and shape lambda keeps its secrets in ssm"))
		}
	}
	if c := table["secrets"]; c.Adapter == "openbao" && s.Secrets != nil && s.Secrets.Source == "ssm" {
		errs = append(errs, errors.New("secrets.source is ssm and adapters.secrets is openbao: a document names one place its secrets are"))
	}
	return errors.Join(errs...)
}

// credentialKeys are the paths of the keys under v that would hold a
// credential's value: not a name (`...Secret`) and not a path (`...File`).
func credentialKeys(v any, at string) []string {
	var out []string
	switch t := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(t)) {
			lower := strings.ToLower(k)
			if !strings.HasSuffix(lower, "file") && !strings.HasSuffix(lower, "secret") {
				for _, w := range credentialWords {
					if strings.Contains(lower, w) {
						out = append(out, at+"."+k)
						break
					}
				}
			}
			out = append(out, credentialKeys(t[k], at+"."+k)...)
		}
	case []any:
		for i, x := range t {
			out = append(out, credentialKeys(x, fmt.Sprintf("%s[%d]", at, i))...)
		}
	}
	return out
}

// ConsoleAudience is the audience an AWS role's web identity token is minted
// for to be a bearer at the console: `console.awsAudience`, else the console's
// URL (the service's own default).
func ConsoleAudience(in *Installation) string {
	if in.Console != nil && in.Console.AWSAudience != "" {
		return in.Console.AWSAudience
	}
	if in.Issuer.ConsoleURL != "" {
		return in.Issuer.ConsoleURL
	}
	root := in.Issuer.RootURL
	if root == "" {
		root = strings.TrimSpace(in.Issuer.URL)
	}
	return strings.TrimSuffix(root, "/") + "/console"
}
