//nolint:lll // fixtures are one-line tables
package rosterapp_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	githubroster "github.com/truvity/sluis/internal/githubroster/app"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/lambdaapp/lambdatest"
	"github.com/truvity/sluis/internal/modserve"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/rosterapp"
)

const (
	k8sIssuerSubject = "system:serviceaccount:sluis:sluis-issuer"
	k8sToken         = "projected-issuer-token"
)

// On Kubernetes the GitHub module is a Deployment of its own (`github.serve`,
// internal/modserve) and the issuer reaches its Service with `github.remote.url`
// and a projected ServiceAccount token. This wires both sides in one process, the
// listener over a faked TokenReview and the issuer over the real HTTP dial, and
// proves the issuer holds no GitHub store and still reaches the module for the
// console and for the mint.
func TestTheIssuerReachesAKubernetesGitHubModuleAndHoldsNoGitHubStore(t *testing.T) {
	h := lambdatest.NewHarness(t)
	log := slog.New(slog.DiscardHandler)
	dir := t.TempDir()
	policyFile := e2eWrite(t, dir, "policy.yaml", `apiVersion: sluis.truvity.github.io/policy/v2
groups:
  all:access-roster:operator: { members: [`+e2eLinkUser+`] }
  all:access-roster:viewer: {}
github: { `+e2eOrg+`: { members: [all:access-roster:operator] } }
apps:
  github:
    apps:
      - id: renovate
        purpose: catalogue
        org: `+e2eOrg+`
        permissions: { contents: read, metadata: read }
        grants:
          - { group: all:access-roster:operator, repositories: ["*"], permissions: { contents: read } }
lifetimes: { default: 12h }
clients:
  console: { kind: public, requires: [all:access-roster:operator], redirects: ["`+e2eMount+`/callback"] }
`)

	// The module: its own store, the real mint and console servers, behind modserve
	// and a TokenReview that accepts only the issuer's projected token for the
	// module's audience.
	set := memory.New().Set()
	base := portstore.New(set)
	apps := portstore.NewGitHubCatalogueApps(base)
	if err := apps.Put(context.Background(), catalogueapp.Record{ID: "renovate", Org: e2eOrg, AppID: 44, AppSlug: "acme-renovate", InstallationID: 42, ConnectedBy: e2eLinkUser}, e2ePEM); err != nil {
		t.Fatal(err)
	}
	if err := portstore.NewGitHubOrgs(base).PutLinkApp(context.Background(), linkApp(), linkCredential()); err != nil {
		t.Fatal(err)
	}
	srv := githubroster.MintServer(apps, h.GitHub.Client())
	doc, pol := policyOf(t, policyFile)
	registerGitHubConsole(srv, log, doc, pol, base, set, h.GitHub.Client())

	var mu sync.Mutex
	var reviewed []string
	review := func(_ context.Context, token string, audiences []string) (string, error) {
		mu.Lock()
		reviewed = append(reviewed, strings.Join(audiences, ",")+"|"+token)
		mu.Unlock()
		if len(audiences) == 1 && audiences[0] == "github" && token == k8sToken {
			return k8sIssuerSubject, nil
		}
		return "", http.ErrNoCookie
	}
	module := httptest.NewServer(modserve.Mux(srv, modserve.Reviewer(review, "github"), []string{k8sIssuerSubject}))
	t.Cleanup(module.Close)

	// The issuer: github.remote names the Service, and the projected token is a
	// file. Nothing else is remote, and no GitHub table is the issuer's.
	h.SSM.Put("/sluis/"+lambdatest.Instance+"/internal/oidc/console-session-key",
		`{"value":"`+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))+`"}`)
	tokenFile := e2eWrite(t, dir, "token", k8sToken)
	svc := `apiVersion: sluis.truvity.github.io/sluis/v3
issuerURL: ` + e2eIssuerURL + `
publicURL: ` + e2eMount + `
policy: {file: ` + policyFile + `}
secrets: {source: ssm, root: /sluis/` + lambdatest.Instance + `, layout: v5, endpoint: ` + h.SSM.URL() + `}
recovery: {enabled: true, passwordSecret: recovery/password}
adapters:
  secrets: {adapter: ssm, settings: {endpoint: ` + h.SSM.URL() + `}}
  state: {adapter: memory}
  blobs: {adapter: memory}
  trigger: {adapter: memory}
github: {remote: {url: ` + module.URL + `, tokenFile: ` + tokenFile + `}}
`
	cfg, err := rosterapp.Load(e2eWrite(t, dir, "sluis.yaml", svc))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Directory.GitHubRemote {
		t.Fatal("github.remote left the GitHub stores to the issuer's process")
	}
	app, err := rosterapp.New(context.Background(), cfg, log)
	if err != nil {
		t.Fatalf("an issuer with github.remote did not start: %v", err)
	}
	t.Cleanup(app.Close)
	r := &e2eRig{t: t, handler: app.Handler()}

	t.Run("a console page is the module's over modserve", func(t *testing.T) {
		code, out := r.rpc(r.session(), "/directoryroster.v1.GitHubService/GetGitHubStatus", map[string]any{})
		if code != http.StatusOK || !strings.Contains(asJSON(out), `"id":"renovate"`) {
			t.Errorf("GetGitHubStatus = %d %v, want the module's catalogue App", code, out)
		}
	})

	t.Run("the mint is the module's over modserve", func(t *testing.T) {
		minted, err := app.Issuer().MintGitHubToken(context.Background(), []string{"all:access-roster:operator"},
			issuer.GitHubTokenRequest{App: "renovate", Permissions: map[string]string{"contents": "read"}})
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		if minted.Token != h.GitHub.Token || minted.Installation != 42 {
			t.Errorf("minted %+v, want the fake GitHub's token for installation 42", minted.MintedToken)
		}
	})

	t.Run("every call was reviewed for the module's audience", func(t *testing.T) {
		mu.Lock()
		defer mu.Unlock()
		if len(reviewed) < 2 {
			t.Fatalf("the TokenReview saw %d calls, want the console's and the mint's", len(reviewed))
		}
		for _, got := range reviewed {
			if got != "github|"+k8sToken {
				t.Errorf("a call was reviewed as %q, want the projected token for audience github", got)
			}
		}
	})

	t.Run("the issuer opened no GitHub store", func(t *testing.T) {
		stores := app.Stores()
		if _, ok := stores.Ports.Peer(port.ModuleGitHub); ok {
			t.Error("the issuer holds a view of the GitHub table")
		}
		if n := countRecordsIn(t, stores.Ports.State, port.ModuleGitHub); n != 0 {
			t.Errorf("the issuer's table holds %d GitHub records", n)
		}
		if n := countRecords(t, set, port.ModuleGitHub); n == 0 {
			t.Error("the module's own table holds no GitHub record: the test proves nothing")
		}
		for _, name := range h.SSM.Reads() {
			if strings.Contains(name, "/github/") {
				t.Errorf("the issuer read the GitHub parameter %s", name)
			}
		}
	})
}

func asJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
