// Package boundaries pins which packages of the service may import which
// (docs/decisions/0071). It has no code: the module split moves files, and
// these rules say what the move must not undo. A rule here holds today; a
// rule for a boundary that does not exist yet is added in the change that
// makes it.
package boundaries

import (
	"os/exec"
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
	modules := []string{"internal/module/github", "internal/module/slack", "internal/module/issuer"}
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
			sees:   "internal/cloudflare/minter",
		},
		{
			// The contract every role implements: it knows no role.
			name:   "the module contract imports no role",
			from:   []string{"internal/module"},
			except: []string{"internal/module/issuer", "internal/module/github", "internal/module/slack"},
			forbid: slices.Concat(front, github, slack, cloudflare, modules),
			sees:   "internal/module",
		},
		{
			name:   "the GitHub module wraps only the GitHub role",
			from:   []string{"internal/module/github"},
			forbid: append(append(append([]string{}, front...), slack...), append(cloudflare, "internal/module/slack", "internal/module/issuer")...),
			sees:   "internal/module/github",
		},
		{
			name:   "the Slack module wraps only the Slack role",
			from:   []string{"internal/module/slack"},
			forbid: append(append(append([]string{}, front...), github...), append(cloudflare, "internal/module/github", "internal/module/issuer")...),
			sees:   "internal/module/slack",
		},
		{
			name:   "the issuer module wraps no provider module",
			from:   []string{"internal/module/issuer"},
			forbid: []string{"internal/module/github", "internal/module/slack", "internal/githubroster", "internal/slackroster"},
			sees:   "internal/module/issuer",
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
