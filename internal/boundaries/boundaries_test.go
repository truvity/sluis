// Package boundaries pins which packages of the service may import which
// (docs/decisions/0071). It has no code: the module split moves files, and
// these rules say what the move must not undo. A rule here holds today; a
// rule for a boundary that does not exist yet is added in the change that
// makes it.
package boundaries

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const mod = "github.com/truvity/sluis/"

// imports is every package of the root module and the packages it imports
// directly, tests excluded.
func imports(t *testing.T) map[string][]string {
	t.Helper()
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}} {{join .Imports \" \"}}", "./...")
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	m := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		m[f[0]] = f[1:]
	}
	// A sweep that found little listed the wrong thing.
	if len(m) < 100 {
		t.Fatalf("the sweep found %d packages: it listed the wrong thing", len(m))
	}
	return m
}

// under reports whether path is prefix or below it.
func under(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

type rule struct {
	name string
	// from selects the importing packages (a path or a directory prefix, relative to the module).
	from []string
	// except are importers exempt from the rule.
	except []string
	// forbid are the imported paths (or directory prefixes) the importers may not have.
	forbid []string
	// sees are importers the rule must find at least one of: a rule that matched nothing checks nothing.
	sees string
}

func TestModuleBoundaries(t *testing.T) {
	pkgs := imports(t)

	// The directories of the provider modules: each is the only holder of its
	// provider's credentials once split, so none reaches into the issuer, the
	// shared server or another provider.
	github := []string{"internal/githubroster", "internal/githubapp"}
	slack := []string{"internal/slackroster", "internal/slackapp"}
	cloudflare := []string{"internal/cloudflare"}
	modules := []string{"internal/module/github", "internal/module/slack", "internal/module/issuer", "internal/module/cloudflare"}
	front := []string{"internal/issuer", "internal/issuerapp", "internal/hub", "internal/hublocal", "internal/server",
		"internal/rosterapp", "internal/lambdaapp"}

	rules := []rule{
		{
			// The signing-key adapters (a KMS, a Transit, a local key) belong to the
			// signer, internal/signer, which opens the one backend a document names.
			// The key interface (storage/keys) may be imported anywhere: it is a
			// type, not a credential.
			name:   "only the signer imports the signing-key adapters",
			from:   []string{"internal", "cmd", "identity", "policy", "config", "deploy"},
			except: []string{"internal/signer"},
			forbid: []string{"storage/keys/kms", "storage/keys/local", "storage/keys/transit"},
			sees:   "internal/signer",
		},
		{
			// ADR 0071 decision 3: the signer parses no request. It imports no
			// front end, server or console session package of ours, and no HTTP
			// router, cookie, session or OIDC protocol library.
			name: "the signer imports no HTTP server, cookie, session or OIDC protocol package",
			from: []string{"internal/signer"},
			forbid: slices.Concat(front, []string{"internal/consoleauth", "internal/app", "internal/connector", "internal/access",
				"internal/kube", "internal/valkey",
				"github.com/zitadel", "github.com/gorilla", "github.com/go-chi", "connectrpc.com", "golang.org/x/oauth2",
				"net/http/cookiejar", "net/http/httptest", "net/http/httputil", "net/http/pprof"}),
			sees: "internal/signer",
		},
		{
			name:   "the GitHub module imports neither the front end nor another provider",
			from:   github,
			forbid: append(append(append([]string{}, front...), slack...), cloudflare...),
			sees:   "internal/githubroster/app",
		},
		{
			name:   "the Slack module imports neither the front end nor another provider",
			from:   slack,
			forbid: append(append(append([]string{}, front...), github...), cloudflare...),
			sees:   "internal/slackroster/app",
		},
		{
			name:   "the Cloudflare module imports neither the front end nor another provider",
			from:   cloudflare,
			forbid: append(append(append([]string{}, front...), github...), slack...),
			sees:   "internal/cloudflare/app",
		},
		{
			// The contract every role implements: it knows no role.
			name:   "the module contract imports no role",
			from:   []string{"internal/module"},
			except: []string{"internal/module/issuer", "internal/module/github", "internal/module/slack", "internal/module/cloudflare"},
			forbid: slices.Concat(front, github, slack, cloudflare, modules),
			sees:   "internal/module",
		},
		{
			name:   "the GitHub module wraps only the GitHub role",
			from:   []string{"internal/module/github"},
			forbid: slices.Concat(front, slack, cloudflare, []string{"internal/module/slack", "internal/module/issuer", "internal/module/cloudflare"}),
			sees:   "internal/module/github",
		},
		{
			name:   "the Slack module wraps only the Slack role",
			from:   []string{"internal/module/slack"},
			forbid: slices.Concat(front, github, cloudflare, []string{"internal/module/github", "internal/module/issuer", "internal/module/cloudflare"}),
			sees:   "internal/module/slack",
		},
		{
			name: "the issuer module wraps no provider module",
			from: []string{"internal/module/issuer"},
			forbid: []string{"internal/module/github", "internal/module/slack", "internal/module/cloudflare",
				"internal/githubroster", "internal/slackroster", "internal/cloudflare"},
			sees: "internal/module/issuer",
		},
		{
			name:   "the Cloudflare module wraps only the Cloudflare role",
			from:   []string{"internal/module/cloudflare"},
			forbid: slices.Concat(front, github, slack, []string{"internal/module/github", "internal/module/slack", "internal/module/issuer"}),
			sees:   "internal/module/cloudflare",
		},
		{
			// The transport between modules knows no module: it carries bytes.
			name:   "the module-call transport imports no role",
			from:   []string{"internal/modcall"},
			forbid: slices.Concat(front, github, slack, cloudflare, modules, []string{"internal/module"}),
			sees:   "internal/modcall/lambdacall",
		},
		{
			// A role is assembled by its module and the main, never the reverse.
			name:   "no role imports the module packages that wrap it",
			from:   append(append(append([]string{}, front...), github...), append(slack, cloudflare...)...),
			forbid: []string{"internal/module"},
			sees:   "internal/issuer",
		},
		{
			// The issuer front end is a library the processes compose; it does not
			// compose them.
			name: "the issuer library imports no process, server or provider controller",
			from: []string{"internal/issuer"},
			forbid: []string{"internal/issuerapp", "internal/server", "internal/rosterapp", "internal/lambdaapp", "internal/kube", "internal/valkey",
				"internal/githubroster/app", "internal/githubroster/controller", "internal/slackroster"},
			sees: "internal/issuer",
		},
	}

	for _, r := range rules {
		t.Run(r.name, func(t *testing.T) {
			seen := false
			for pkg, deps := range pkgs {
				rel := strings.TrimPrefix(pkg, mod)
				if !anyUnder(rel, r.from) {
					continue
				}
				if rel == r.sees || under(rel, r.sees) {
					seen = true
				}
				if anyUnder(rel, r.except) {
					continue
				}
				for _, d := range deps {
					drel := strings.TrimPrefix(d, mod)
					// Module paths are matched relative to the module; third-party
					// and standard-library paths are matched whole.
					if anyUnder(drel, r.forbid) {
						t.Errorf("%s imports %s", rel, drel)
					}
				}
			}
			if !seen {
				t.Errorf("the rule found no package under %s: it checked nothing", r.sees)
			}
		})
	}
}

func anyUnder(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if under(path, p) {
			return true
		}
	}
	return false
}

// The adapters are imported by the signer, so the rule above would pass on a
// tree where nobody imports them: this holds the sweep to seeing the one
// legitimate import.
func TestTheSignerSeesTheKeyAdapter(t *testing.T) {
	deps := imports(t)[mod+"internal/signer"]
	for _, d := range deps {
		if d == mod+"storage/keys/kms" {
			return
		}
	}
	t.Fatalf("internal/signer does not import storage/keys/kms: the signer moved, update the rule")
}

// minterPackages are the Cloudflare minter and its real client: whoever imports
// them can mint Cloudflare credentials.
var minterPackages = []string{"internal/cloudflare/minter", "internal/cloudflare/cfapi"}

// minterWiring are the packages outside the Cloudflare module that import the
// minter today, because the issuer's process still carries it (the console, the
// on-demand exchange and the blob credentials). Each goes when the exchange
// becomes an Invoke boundary to the Cloudflare module (docs/decisions/0071
// D93 a); an entry nobody uses any more is an error, so the list only shrinks.
var minterWiring = map[string]string{
	"internal/rosterapp": "assembles the minter into the one process of `serve`",
	"internal/server":    "the console's view of the minter (CloudflareSTS)",
	"internal/store":     "the blob credentials of a preset (credentials.preset)",
}

// minterImporters reports the problems with who imports the minter: a package
// outside the Cloudflare packages that is not listed in wiring, and a listed one
// that does not import it.
func minterImporters(pkgs map[string][]string, wiring map[string]string) []string {
	var problems []string
	used := map[string]bool{}
	for pkg, deps := range pkgs {
		rel := strings.TrimPrefix(pkg, mod)
		if anyUnder(rel, []string{"internal/cloudflare", "internal/module/cloudflare"}) {
			continue
		}
		for _, d := range deps {
			drel := strings.TrimPrefix(d, mod)
			if d == drel || !anyUnder(drel, minterPackages) {
				continue
			}
			if _, ok := wiring[rel]; ok {
				used[rel] = true
				continue
			}
			problems = append(problems, rel+" imports "+drel)
		}
	}
	for rel := range wiring {
		if !used[rel] {
			problems = append(problems, rel+" is listed as minter wiring and no longer imports the minter: remove it from the list")
		}
	}
	slices.Sort(problems)
	return problems
}

func TestOnlyTheCloudflarePackagesAndTheIssuerWiringImportTheMinter(t *testing.T) {
	for _, p := range minterImporters(imports(t), minterWiring) {
		t.Error(p)
	}
}

func TestTheMinterExemptionsAreHeldToUse(t *testing.T) {
	pkgs := map[string][]string{
		mod + "internal/cloudflare/app": {mod + "internal/cloudflare/minter"},
		mod + "internal/rosterapp":      {mod + "internal/cloudflare/minter"},
		mod + "internal/hub":            {mod + "internal/cloudflare/cfapi"},
	}
	got := minterImporters(pkgs, map[string]string{"internal/rosterapp": "x", "internal/server": "y"})
	want := []string{
		"internal/hub imports internal/cloudflare/cfapi",
		"internal/server is listed as minter wiring and no longer imports the minter: remove it from the list",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The ring, its wrapped-key generation and the KMS keys live in internal/signer.
// The issuer still holds the keys of the library's own mint paths
// (signer.SigningKey, signer.KeyRings and signer.WrappedSigning, which
// Storage.UseWrappedSigning takes), and nothing else of the ring's internals:
// a package-level import rule cannot say that, so this reads the issuer's
// sources and refuses a reference to the names below.
func TestTheIssuerNamesNoRingInternals(t *testing.T) {
	internals := []string{"KeyRing", "KeyRingStatus", "NewKeyRing", "NewWrappedSigning", "WrappedConfig", "WrappedLease",
		"KMSAPI", "KMSSigningKey", "KMSSigningKeyFor", "KMSKeyRefs", "EncryptionContext", "ParseSigningKey"}
	files, err := filepath.Glob("../issuer/*.go")
	if err != nil || len(files) < 20 {
		t.Fatalf("the sweep found %d issuer files (%v): it listed the wrong thing", len(files), err)
	}
	seen := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "signer" {
				seen++
				if slices.Contains(internals, sel.Sel.Name) {
					t.Errorf("%s names signer.%s: the ring's internals belong to internal/signer", f, sel.Sel.Name)
				}
			}
			return true
		})
	}
	if seen == 0 {
		t.Error("the issuer references nothing of the signer: the rule checked nothing")
	}
}
