package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	internal "github.com/truvity/sluis/internal/config"
)

// The modules a Lambda installation runs as functions of their own, by the name
// the installation's `aws.functions` and [ModuleDocuments.Modules] give them.
// The issuer is the other function, and its document is
// [ModuleDocuments.Issuer].
const (
	ModuleGitHub     = "github"
	ModuleGitHubMint = "github-mint"
	ModuleSlack      = "slack"
	ModuleGoogle     = "google"
	ModuleCloudflare = "cloudflare"
	// ModuleIssuer names the issuer for [ModuleSecrets]; it has no entry in
	// [ModuleDocuments.Modules].
	ModuleIssuer = "issuer"
	// ModuleBackup names the backup function for [FunctionNameOf]; it has no
	// entry in [ModuleDocuments.Modules].
	ModuleBackup = "backup"
)

// Modules are the module functions, in a fixed order.
func Modules() []string {
	return []string{ModuleCloudflare, ModuleGitHub, ModuleGitHubMint, ModuleGoogle, ModuleSlack}
}

// FunctionNameOf is the name of a function: `aws.functions.<module>`, else
// `sluis-<instance>-<module>`. The module is one of [Modules], [ModuleIssuer]
// (the issuer, whose name `aws.functionName` also sets, as it did before the
// default was `sluis-<instance>-issuer`; an installation with no instance is
// `sluis`, [DefaultFunctionName]) or [ModuleBackup].
func FunctionNameOf(in *Installation, module string) string {
	if in.AWS != nil {
		if n := in.AWS.Functions[module]; n != "" {
			return n
		}
		if module == ModuleIssuer && in.AWS.FunctionName != "" {
			return in.AWS.FunctionName
		}
	}
	if module == ModuleIssuer && in.Instance == "" {
		return DefaultFunctionName
	}
	return "sluis-" + in.Instance + "-" + module
}

// moduleTable is the table of the module a function reads: the mint-only
// function reads the GitHub module's.
func moduleTable(module string) string {
	if module == ModuleGitHubMint {
		return ModuleGitHub
	}
	return module
}

// ModuleOptions say what an installation cannot: the names the infrastructure
// gave.
type ModuleOptions struct {
	// Tables are the DynamoDB table of each module of layout v5, by the module's
	// name (oidc, github, slack, cloudflare, google, backup). A module not listed
	// has `sluis-<instance>-<module>`.
	Tables map[string]string
}

// ModuleDocuments are the documents of a Lambda installation that runs a
// function per module.
type ModuleDocuments struct {
	// Issuer is the issuer's service document (sluis/v3): [Render]'s with the
	// modules it calls elsewhere, `github.remote`, `slack.remote`, `google.remote`
	// and `cloudflare.remote`, naming the functions; what the modules own
	// (`controllers`, `directory`, the Cloudflare accounts and presets) is not in it.
	Issuer []byte
	// Policy is the policy document (policy/v2) every function that decides by
	// it reads from the same file.
	Policy []byte
	// Modules are the service documents of the module functions, by
	// [Modules]: controller-github/v2 (github, and the mint-only function,
	// which reads the same document), controller-slack/v2, sluis-google/v1 and
	// sluis/v3 with `cloudflare.serve`. The Cloudflare module has one only when
	// the installation declares a preset: a module with nothing to do has no
	// function.
	Modules map[string][]byte
}

var functionNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// RenderModules writes the documents of an installation that runs a function
// per module: [Render]'s two documents, with the issuer's calling the modules
// by their functions, and a service document for each module that holds what
// is the module's own and nothing else.
//
// A module document opens only its own table (`ports.dynamodb.tables` has the
// module's alone, since the readiness probe describes each table it lists), reads
// its secrets as layout v5 (`secrets.layout: v5`), and names the same policy
// file as the issuer's, where the module reads one. The documents hold no
// secret, and the secrets they name are disjoint but for the blob store's
// credential (see [ModuleSecrets]).
//
// It is deterministic and leaves in alone. Every document it returns has been
// loaded as the module's function loads it.
func RenderModules(in *Installation, opts ModuleOptions) (*ModuleDocuments, error) {
	if in == nil {
		return nil, errors.New("render: no installation")
	}
	if err := in.check(); err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	if err := in.checkModules(); err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	svc, err := in.service()
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	pol, err := in.policyDocument()
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	out := &ModuleDocuments{Modules: map[string][]byte{}}
	if out.Policy, err = encode(pol); err != nil {
		return nil, fmt.Errorf("render: the policy document: %w", err)
	}
	if out.Issuer, err = encode(in.issuerOf(svc)); err != nil {
		return nil, fmt.Errorf("render: the issuer's document: %w", err)
	}
	mod := moduleBuilder{in: in, svc: svc, opts: opts}
	for _, m := range Modules() {
		doc, err := mod.document(m)
		if err != nil {
			return nil, fmt.Errorf("render: the %s document: %w", m, err)
		}
		if doc == nil {
			continue
		}
		if out.Modules[m], err = encode(doc); err != nil {
			return nil, fmt.Errorf("render: the %s document: %w", m, err)
		}
	}
	if err := verifyModules(out); err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	return out, nil
}

// checkModules is what the module functions ask of an installation beyond
// [Installation.check].
func (in *Installation) checkModules() error {
	var errs []error
	if in.Shape != ShapeLambda {
		errs = append(errs, fmt.Errorf("shape is %q: the module functions are Lambda functions", in.Shape))
	}
	if in.AWS != nil && in.AWS.Table != "" {
		errs = append(errs, errors.New("aws.table is the layout v4 table: the module functions run on layout v5, whose tables are ports.dynamodb.tables"))
	}
	if s := in.Secrets; s != nil && s.Layout != "" && s.Layout != internal.SecretsLayoutV5 {
		errs = append(errs, fmt.Errorf("secrets.layout is %q: the module functions read layout v5", s.Layout))
	}
	if c := in.Cloudflare; c != nil && c.Remote != nil {
		errs = append(errs, errors.New("cloudflare.remote is written by the renderer, from aws.functions: leave it out"))
	}
	if in.AWS == nil {
		return errors.Join(errs...)
	}
	names := map[string]string{in.functionName(): ModuleIssuer}
	known := append([]string{ModuleIssuer, ModuleBackup}, Modules()...)
	for _, m := range slices.Sorted(maps.Keys(in.AWS.Functions)) {
		if !slices.Contains(known, m) {
			errs = append(errs, fmt.Errorf("aws.functions names %q, which is not a function (%s)", m, strings.Join(known, ", ")))
			continue
		}
		if n := in.AWS.Functions[m]; !functionNamePattern.MatchString(n) {
			errs = append(errs, fmt.Errorf("aws.functions.%s %q is letters, digits, - and _, at most 64", m, n))
		}
	}
	for _, m := range append([]string{ModuleBackup}, Modules()...) {
		n := FunctionNameOf(in, m)
		if other, dup := names[n]; dup {
			errs = append(errs, fmt.Errorf("the function name %q is both the %s function's and the %s function's", n, other, m))
		}
		names[n] = m
	}
	return errors.Join(errs...)
}

// issuerOf is the issuer's service document: the installation's, calling the
// modules by their functions. What the modules own is theirs and not here: the
// controllers, the declared workspaces and the Cloudflare accounts and presets.
func (in *Installation) issuerOf(svc *internal.Sluis) *internal.Sluis {
	s := *svc
	route := func(m string) *internal.ModuleRoute {
		return &internal.ModuleRoute{Remote: &internal.ModuleRemote{Function: FunctionNameOf(in, m)}}
	}
	s.GitHub, s.Slack, s.Google = route(ModuleGitHub), route(ModuleSlack), route(ModuleGoogle)
	s.Controllers = nil
	s.Directory = nil
	s.Cloudflare = nil
	if in.Cloudflare.Declared() {
		s.Cloudflare = &internal.Cloudflare{Remote: &internal.CloudflareRemote{Function: FunctionNameOf(in, ModuleCloudflare)}}
	}
	// A run-now starts the controller's own function, not the issuer's.
	if t, ok := s.Adapters["trigger"]; ok && t.Adapter == "invoke" {
		adapters := maps.Clone(s.Adapters)
		adapters["trigger"] = internal.AdapterChoice{Adapter: "invoke", Settings: map[string]any{
			"github": FunctionNameOf(in, ModuleGitHub), "slack": FunctionNameOf(in, ModuleSlack),
		}}
		s.Adapters = adapters
	}
	return &s
}

type moduleBuilder struct {
	in   *Installation
	svc  *internal.Sluis
	opts ModuleOptions
}

// document is the module's service document, or nil for a module that has
// nothing to do.
func (b moduleBuilder) document(module string) (any, error) {
	switch module {
	case ModuleGitHub, ModuleGitHubMint:
		return b.github(module)
	case ModuleSlack:
		return b.slack()
	case ModuleGoogle:
		return b.google()
	case ModuleCloudflare:
		return b.cloudflare()
	}
	return nil, fmt.Errorf("%q is not a module", module)
}

// secrets are the module's: the installation's `secrets`, on layout v5.
func (b moduleBuilder) secrets() (*internal.Secrets, error) {
	s := copyOf(b.svc.Secrets)
	if s == nil {
		return nil, errors.New("the installation has no secrets section")
	}
	s.Layout = internal.SecretsLayoutV5
	return s, nil
}

// ports open the module's own table and no other.
func (b moduleBuilder) ports(module string) *internal.Ports {
	table := moduleTable(module)
	name := b.opts.Tables[table]
	if name == "" {
		name = "sluis-" + b.in.Instance + "-" + table
	}
	region := ""
	if b.in.AWS != nil {
		region = b.in.AWS.Region
	}
	return &internal.Ports{Adapter: "dynamodb", DynamoDB: &internal.DynamoDB{Tables: map[string]string{table: name}, Region: region}}
}

func lambdaPlatform() *internal.Platform {
	return &internal.Platform{AWS: true, Runtime: "lambda"}
}

// adapters are what a module that notifies nobody takes from the installation:
// its audit sink. The trigger is the in-process one: the preset's `invoke`
// starts a controller function, and a module's function is started by events.
func (b moduleBuilder) adapters() map[string]internal.AdapterChoice {
	out := map[string]internal.AdapterChoice{"trigger": {Adapter: "memory"}}
	if a, ok := b.svc.Adapters["audit"]; ok {
		a.Settings = maps.Clone(a.Settings)
		out["audit"] = a
	}
	return out
}

// consoleProof is how a controller proves itself to the console on Lambda: the
// function role's web identity token for the console's audience.
func (b moduleBuilder) consoleProof(c *internal.RosterConsole) *internal.RosterConsole {
	if c != nil {
		return c
	}
	return &internal.RosterConsole{Auth: &internal.RosterConsoleAuth{AWS: &internal.RosterConsoleAWS{Audience: ConsoleAudience(b.in)}}}
}

// controllerOf makes the Roster a controller's section and the process's shared
// keys say, with the keys of a module function.
func (b moduleBuilder) rosterOf(r *internal.Roster, withConsole bool) error {
	var err error
	if r.Secrets, err = b.secrets(); err != nil {
		return err
	}
	r.GitHub, r.Slack, r.Google = nil, nil, nil
	r.Platform = lambdaPlatform()
	r.Adapters = b.adapters()
	if r.Audit != nil && r.Audit.Writer == "" && r.Audit.TokenFile == "" {
		r.Audit = nil
	}
	if withConsole {
		r.Console = b.consoleProof(r.Console)
	}
	return nil
}

func (b moduleBuilder) shared() *internal.Sluis {
	t := *b.svc
	t.GitHub, t.Slack, t.Google, t.Controllers = nil, nil, nil, nil
	t.Ports, t.Platform, t.Adapters = nil, nil, nil
	return &t
}

func (b moduleBuilder) github(module string) (any, error) {
	t := b.shared()
	g := internal.GitHubController{}
	if c := b.in.Controllers; c != nil && c.GitHub != nil {
		g = *copyOf(&c.GitHub.GitHubController)
	}
	t.Controllers = &internal.Controllers{GitHub: &g}
	doc := t.GitHubController()
	// The mint-only function asks nothing of the console.
	if err := b.rosterOf(&doc.Roster, module == ModuleGitHub); err != nil {
		return nil, err
	}
	doc.Ports = b.ports(module)
	return doc, nil
}

func (b moduleBuilder) slack() (any, error) {
	t := b.shared()
	g := internal.SlackController{}
	if c := b.in.Controllers; c != nil && c.Slack != nil {
		g = *copyOf(&c.Slack.SlackController)
	}
	t.Controllers = &internal.Controllers{Slack: &g}
	doc := t.SlackController()
	if err := b.rosterOf(&doc.Roster, true); err != nil {
		return nil, err
	}
	doc.Ports = b.ports(ModuleSlack)
	return doc, nil
}

// google is the Google module's document: the workspaces the installation
// declares, the intervals of its `freshness`, the table of its own and the
// bucket the snapshots are kept in. The OAuth client that reopens a workspace
// connected by consent is not written: the issuer's sign-in client is the
// issuer's, and the module declares no secret of the issuer's.
func (b moduleBuilder) google() (any, error) {
	doc := &internal.SluisGoogle{APIVersion: internal.GoogleAPIVersion, Instance: b.in.Instance, Log: copyOf(b.svc.Log),
		Platform: lambdaPlatform(), Preset: b.svc.Preset}
	var err error
	if doc.Secrets, err = b.secrets(); err != nil {
		return nil, err
	}
	doc.Ports = b.ports(ModuleGoogle)
	if a := b.in.AWS; a != nil && a.Bucket != "" {
		doc.Ports.Blob = &internal.PortsBlob{Adapter: "s3", S3: &internal.PortsBlobS3{Bucket: a.Bucket, Region: a.Region}}
	}
	if d := b.in.Directory; d != nil {
		doc.Google.Workspaces = slices.Clone(d.Workspaces)
	}
	if f := b.in.Freshness; f != nil {
		doc.Google.RefreshInterval = copyOf(f.RefreshInterval)
		doc.Google.FreshnessWindow = copyOf(f.FreshnessWindow)
		doc.Google.ProbeInterval = copyOf(f.ProbeInterval)
	}
	return doc, nil
}

// cloudflare is the Cloudflare module's document (sluis/v3 with
// `cloudflare.serve`): the accounts and presets, the policy that grants them
// and the storage the credentials are kept in. nil where no preset is declared.
func (b moduleBuilder) cloudflare() (any, error) {
	if !b.in.Cloudflare.Declared() {
		return nil, nil
	}
	c := &internal.Sluis{}
	c.APIVersion = internal.APIVersion("sluis")
	c.IssuerURL = b.svc.IssuerURL
	c.Release, c.Instance = b.svc.Release, b.svc.Instance
	c.Policy = copyOf(b.svc.Policy)
	c.Log = copyOf(b.svc.Log)
	c.Platform, c.Preset, c.Adapters = lambdaPlatform(), b.svc.Preset, b.adapters()
	c.Ports = b.ports(ModuleCloudflare)
	var err error
	if c.Secrets, err = b.secrets(); err != nil {
		return nil, err
	}
	cf := b.in.Cloudflare.Cloudflare
	cf.Remote = nil
	c.Cloudflare = copyOf(&cf)
	c.Cloudflare.Serve = &internal.CloudflareServe{}
	return c, nil
}

// verifyModules loads every document as the function that reads it does.
func verifyModules(d *ModuleDocuments) error {
	var errs []error
	if err := withFiles(map[string][]byte{"sluis": d.Issuer, "policy": d.Policy}, func(f map[string]string) error {
		if _, err := internal.Load[internal.Sluis](f["sluis"]); err != nil {
			return fmt.Errorf("the issuer's document: %w", err)
		}
		if _, err := internal.Load[internal.PolicyDocument](f["policy"]); err != nil {
			return fmt.Errorf("the policy document: %w", err)
		}
		return nil
	}); err != nil {
		errs = append(errs, err)
	}
	for _, m := range Modules() {
		if doc, ok := d.Modules[m]; ok {
			if err := ValidateModule(m, doc, d.Policy); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// ValidateModule loads a module's service document as its function does: the
// schema, the decode and the checks a schema cannot make. policy, when given, is
// the policy document the Cloudflare module's grants are held to.
func ValidateModule(module string, doc, policy []byte) error {
	err := withFiles(map[string][]byte{"sluis": doc, "policy": policy}, func(f map[string]string) error {
		switch module {
		case ModuleGitHub, ModuleGitHubMint:
			_, err := internal.Load[internal.ControllerGitHub](f["sluis"])
			return err
		case ModuleSlack:
			_, err := internal.Load[internal.ControllerSlack](f["sluis"])
			return err
		case ModuleGoogle:
			_, err := internal.LoadGoogle(f["sluis"])
			return err
		case ModuleCloudflare:
			svc, err := internal.Load[internal.Sluis](f["sluis"])
			if err != nil {
				return err
			}
			if svc.Cloudflare == nil || svc.Cloudflare.Serve == nil {
				return errors.New("the document does not set cloudflare.serve: it is not the Cloudflare module's")
			}
			if len(policy) == 0 {
				return nil
			}
			pol, err := internal.Load[internal.PolicyDocument](f["policy"])
			if err != nil {
				return err
			}
			return internal.CheckCloudflare(svc.Cloudflare, pol)
		}
		return fmt.Errorf("%q is not a module function", module)
	})
	if err != nil {
		return fmt.Errorf("the %s document: %w", module, err)
	}
	return nil
}

// ModuleSecrets are the secrets a module's function declares and checks, from
// its document: the kinds the function reads and no other module's. The GitHub
// and Slack modules declare none (an App's credential is made at run time), the
// Google module its workspaces' keys, the Cloudflare module its accounts'
// minters, and the issuer the rest (the recovery password, the state secret, the
// sign-in client, the policy's client secrets, the blob store's credential). The
// blob store's credential is the one both the issuer and the Google module read.
func ModuleSecrets(module string, doc, policy []byte) ([]DeclaredSecret, error) {
	var out []DeclaredSecret
	err := withFiles(map[string][]byte{"sluis": doc, "policy": policy}, func(f map[string]string) error {
		var pol *PolicyDocument
		if len(policy) > 0 {
			p, err := internal.Load[internal.PolicyDocument](f["policy"])
			if err != nil {
				return err
			}
			pol = p
		}
		switch module {
		case ModuleIssuer:
			svc, err := internal.Load[internal.Sluis](f["sluis"])
			if err != nil {
				return err
			}
			out = internal.DeclaredSecrets(&svc.Serve, pol)
		case ModuleGitHub, ModuleGitHubMint, ModuleSlack:
			// An App's credential is made at run time and no document names it.
		case ModuleGoogle:
			g, err := internal.LoadGoogle(f["sluis"])
			if err != nil {
				return err
			}
			for _, w := range g.Google.Workspaces {
				out = append(out, DeclaredSecret{Kind: internal.SecretWorkspaceKey, Subject: w.ID, Name: w.KeySecret})
			}
			// The snapshots are kept in the blob store, whose credential the issuer reads too.
			if p := g.Ports; p != nil && p.Blob != nil && p.Blob.S3 != nil && p.Blob.S3.CredentialsRef != "" {
				out = append(out, DeclaredSecret{Kind: internal.SecretBlobCredentials, Name: p.Blob.S3.CredentialsRef, Ref: true})
			}
		case ModuleCloudflare:
			svc, err := internal.Load[internal.Sluis](f["sluis"])
			if err != nil {
				return err
			}
			for _, d := range internal.DeclaredSecrets(&svc.Serve, nil) {
				if d.Kind == internal.SecretMinter {
					out = append(out, d)
				}
			}
		default:
			return fmt.Errorf("%q is not a module", module)
		}
		return nil
	})
	return out, err
}

// withFiles writes the documents to a directory of their own and runs fn on
// the paths, leaving no file behind. An empty document is not written.
func withFiles(docs map[string][]byte, fn func(map[string]string) error) error {
	dir, err := os.MkdirTemp("", "sluis-modules-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	paths := map[string]string{}
	for name, body := range docs {
		if len(body) == 0 {
			continue
		}
		p := filepath.Join(dir, name+".yaml")
		if err := os.WriteFile(p, body, 0o600); err != nil {
			return err
		}
		paths[name] = p
	}
	return fn(paths)
}
