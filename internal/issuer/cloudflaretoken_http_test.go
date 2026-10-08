package issuer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/tokens"
)

// jobVerifier stands in for GitHub's keys: a `job:<workflow file>` token is a
// run of example-org/app on main from that workflow file.
type jobVerifier struct{}

func (jobVerifier) Verify(ctx context.Context, token, tokenType string) (issuer.Proof, error) {
	file, ok := strings.CutPrefix(token, "job:")
	if !ok {
		return fakeVerifier{}.Verify(ctx, token, tokenType)
	}
	ref := "example-org/app/.github/workflows/" + file + "@refs/heads/main"
	return issuer.Proof{GitHub: &policy.GitHubClaims{
		Repository: "example-org/app", Owner: "example-org", Ref: "refs/heads/main", RefType: "branch",
		EventName: "push", Workflow: "Release", WorkflowRef: ref, JobWorkflowRef: ref,
	}}, nil
}

// cloudflarePolicy declares the CI job as a group, pinned to one workflow file
// on main. Its display name, `Release`, is the same for every workflow in the
// fixture: it grants nothing.
var cloudflarePolicy = strings.Replace(cliPolicy, "groups:\n",
	"groups:\n  all:ci:release:\n    matchers:\n"+
		"      - github: { repository: example-org/app, ref: refs/heads/main, job_workflow_ref: \""+releaseWorkflow+"\" }\n", 1)

// fakeMinter applies the policy's real grants and mints nothing: it records
// who asked, for what, and for how long.
type fakeMinter struct {
	grants *config.PolicyCloudflare
	calls  []fakeMintCall
	err    error
}

type fakeMintCall struct {
	preset   string
	caller   minter.Caller
	lifetime time.Duration
}

var fakePresets = map[string]minter.PresetInfo{
	"dns": {Name: "dns", Description: "DNS edits", Lifetime: 15 * time.Minute, Rotation: 5 * time.Minute},
	"r2": {Name: "r2", Description: "archive bucket", R2: true, Endpoint: "https://acct.r2.cloudflarestorage.com",
		Lifetime: 15 * time.Minute, Rotation: 5 * time.Minute},
}

func (f *fakeMinter) MintFor(_ context.Context, preset string, c minter.Caller, lifetime time.Duration) (*minter.Minted, error) {
	f.calls = append(f.calls, fakeMintCall{preset, c, lifetime})
	info, ok := fakePresets[preset]
	if !ok {
		return nil, fmt.Errorf("%w: %q", minter.ErrUnknownPreset, preset)
	}
	if !f.grants.Allows(preset, c.Groups) {
		return nil, fmt.Errorf("%w: %s", minter.ErrNotGranted, preset)
	}
	if f.err != nil {
		return nil, f.err
	}
	if lifetime == 0 {
		lifetime = info.Lifetime
	}
	out := &minter.Minted{Preset: preset, ExpiresOn: time.Now().Add(lifetime).UTC().Truncate(time.Second), R2: info.R2}
	if info.R2 {
		out.AccessKeyID, out.SecretAccessKey, out.Endpoint = "key-id", "secret-value", info.Endpoint
	} else {
		out.Token = "cf-token-value"
	}
	return out, nil
}

func (f *fakeMinter) Granted(c minter.Caller) []minter.PresetInfo {
	var out []minter.PresetInfo
	for _, name := range []string{"dns", "r2"} {
		if f.grants.Allows(name, c.Groups) {
			out = append(out, fakePresets[name])
		}
	}
	return out
}

type cloudflareIssuer struct {
	server *httptest.Server
	iss    *issuer.Issuer
	minter *fakeMinter
}

func serveCloudflare(t *testing.T, withMinter bool) cloudflareIssuer {
	t.Helper()
	declared, err := policy.Parse([]byte(cloudflarePolicy))
	if err != nil {
		t.Fatalf("parse the policy: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("policy set: %v", err)
	}
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set, adaDirectory(), issuer.NewMemoryState())
	fake := &fakeMinter{grants: &config.PolicyCloudflare{Grants: []config.CloudflareGrant{
		{Group: "mgmt:k8s:admin", Presets: []string{"dns"}},
		{Group: "all:ci:release", Presets: []string{"dns", "r2"}},
	}}}
	if withMinter {
		iss.UseCloudflare(fake)
	}
	storage, err := issuer.NewStorage(iss, jobVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	h, err := handler(iss, storage)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	return cloudflareIssuer{server: server, iss: iss, minter: fake}
}

func (c cloudflareIssuer) ask(t *testing.T, client string, form url.Values) (int, map[string]any, http.Header) {
	t.Helper()
	base := url.Values{
		"grant_type":           {string(oidc.GrantTypeTokenExchange)},
		"subject_token_type":   {string(oidc.JWTTokenType)},
		"requested_token_type": {tokens.TypeCloudflareToken},
	}
	maps.Copy(base, form)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, c.server.URL+"/token", strings.NewReader(base.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if client != "" {
		request.SetBasicAuth(url.QueryEscape(client), "")
	}
	response, err := c.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var body map[string]any
	if err = json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return response.StatusCode, body, response.Header
}

// A CI job in a group the grants name is minted a token under its
// own name, pinned by the verified token's workflow file.
func TestAGrantedJobIsMintedACloudflareToken(t *testing.T) {
	c := serveCloudflare(t, true)

	status, body, header := c.ask(t, "cloudflare:dns", url.Values{
		"subject_token": {"job:release.yml"}, "audience": {"cloudflare:dns"}, "lifetime": {"300"},
	})
	if status != http.StatusOK {
		t.Fatalf("mint = %d %v", status, body)
	}
	if body["access_token"] != "cf-token-value" || body["token"] != "cf-token-value" ||
		body["issued_token_type"] != tokens.TypeCloudflareToken || body["token_type"] != "N_A" {
		t.Errorf("body = %v", body)
	}
	if expires, _ := body["expires_in"].(float64); expires <= 0 || expires > 300 {
		t.Errorf("expires_in = %v, want at most the 300s asked for", body["expires_in"])
	}
	if _, err := time.Parse(time.RFC3339, fmt.Sprint(body["expires_on"])); err != nil {
		t.Errorf("expires_on = %v", body["expires_on"])
	}
	if header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", header.Get("Cache-Control"))
	}
	if len(c.minter.calls) != 1 {
		t.Fatalf("minter asked %d times", len(c.minter.calls))
	}
	got := c.minter.calls[0]
	if got.preset != "dns" || got.caller.Groups[0] != "all:ci:release" || got.lifetime != 5*time.Minute ||
		got.caller.Actor.ID != "github:example-org/app" {
		t.Errorf("minter was asked %+v", got)
	}

	// An R2 preset answers with the S3 credential.
	status, body, _ = c.ask(t, "", url.Values{"subject_token": {"job:release.yml"}, "audience": {"cloudflare:r2"}})
	if status != http.StatusOK {
		t.Fatalf("r2 = %d %v", status, body)
	}
	if body["access_key_id"] != "key-id" || body["secret_access_key"] != "secret-value" ||
		body["endpoint"] != "https://acct.r2.cloudflarestorage.com" || body["token"] != nil {
		t.Errorf("r2 body = %v", body)
	}
}

// Another workflow of the same repository is not the granted job, and a
// refusal does not say whether the preset exists.
func TestAnotherJobAndAnUnknownPresetAreInvalidTarget(t *testing.T) {
	c := serveCloudflare(t, true)
	var descriptions []string
	for _, tc := range []struct{ token, preset string }{{"job:test.yml", "dns"}, {"job:release.yml", "no-such"}, {"job:test.yml", "no-such"}} {
		status, body, _ := c.ask(t, "", url.Values{"subject_token": {tc.token}, "audience": {"cloudflare:" + tc.preset}})
		if status != http.StatusBadRequest || body["error"] != "invalid_target" {
			t.Errorf("%s for %s = %d %v, want invalid_target", tc.token, tc.preset, status, body)
		}
		descriptions = append(descriptions, fmt.Sprint(body["error_description"]))
	}
	if descriptions[0] != descriptions[1] || descriptions[1] != descriptions[2] {
		t.Errorf("the refusals tell presets apart: %q", descriptions)
	}
}

// A person's sign-in is a proof by the rules it is for every exchange, and the
// groups it holds are what the grants read.
func TestASignInIsMintedACloudflareTokenByItsGroups(t *testing.T) {
	c := serveCloudflare(t, true)
	signedIn := signedInTokens(t, c.server, c.iss, "cli")
	access, _ := signedIn["access_token"].(string)
	form := url.Values{
		"subject_token": {access}, "subject_token_type": {string(oidc.AccessTokenType)}, "audience": {"cloudflare:dns"},
	}
	status, body, _ := c.ask(t, "cli", form)
	if status != http.StatusOK {
		t.Fatalf("a sign-in = %d %v", status, body)
	}
	if got := c.minter.calls[0].caller; got.Actor.ID != "ada@north.example" {
		t.Errorf("caller = %+v", got)
	}
	// Not R2: only the job holds it.
	form.Set("audience", "cloudflare:r2")
	if status, body, _ = c.ask(t, "cli", form); status != http.StatusBadRequest || body["error"] != "invalid_target" {
		t.Errorf("r2 for a person = %d %v, want invalid_target", status, body)
	}
	// The sign-in presented by anybody else is not a proof.
	if status, body, _ = c.ask(t, "", form); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Errorf("a sign-in presented as nobody = %d %v, want invalid_grant", status, body)
	}
}

func TestCloudflareRequestsAreRefusedBeforeAnyProofIsRead(t *testing.T) {
	c := serveCloudflare(t, true)
	ok := func() url.Values {
		return url.Values{"subject_token": {"job:release.yml"}, "audience": {"cloudflare:dns"}}
	}
	for name, mutate := range map[string]func(url.Values){
		"actor token":   func(f url.Values) { f.Set("actor_token", "x") },
		"two audiences": func(f url.Values) { f.Add("audience", "cloudflare:r2") },
		"no subject":    func(f url.Values) { f.Del("subject_token") },
		"lifetime text": func(f url.Values) { f.Set("lifetime", "soon") },
		"lifetime zero": func(f url.Values) { f.Set("lifetime", "0") },
	} {
		form := ok()
		mutate(form)
		status, body, _ := c.ask(t, "", form)
		want := "invalid_request"
		if name == "two audiences" {
			want = "invalid_target"
		}
		if status != http.StatusBadRequest || body["error"] != want {
			t.Errorf("%s = %d %v, want %s", name, status, body, want)
		}
	}
	if len(c.minter.calls) != 0 {
		t.Errorf("the minter was asked %d times by malformed requests", len(c.minter.calls))
	}

	c.minter.err = fmt.Errorf("%w: asked 24h", minter.ErrLifetime)
	if status, body, _ := c.ask(t, "", ok()); status != http.StatusBadRequest || body["error"] != "invalid_request" {
		t.Errorf("a lifetime the minter refuses = %d %v", status, body)
	}
	c.minter.err = fmt.Errorf("create the token: secret detail")
	status, body, _ := c.ask(t, "", ok())
	if status != http.StatusInternalServerError || body["error"] != "server_error" || strings.Contains(fmt.Sprint(body), "secret detail") {
		t.Errorf("a Cloudflare failure = %d %v, want server_error without the detail", status, body)
	}
}

func TestWithoutAMinterEveryCloudflareRequestIsInvalidTarget(t *testing.T) {
	c := serveCloudflare(t, false)
	status, body, _ := c.ask(t, "", url.Values{"subject_token": {"job:release.yml"}, "audience": {"cloudflare:dns"}})
	if status != http.StatusBadRequest || body["error"] != "invalid_target" {
		t.Errorf("= %d %v", status, body)
	}
}

// `/.access/grants` lists the presets the caller's groups open, for whoami and
// aws-config, and says nothing of them where the service mints none.
func TestGrantsListsTheCloudflarePresetsTheGroupsOpen(t *testing.T) {
	for _, with := range []bool{true, false} {
		c := serveCloudflare(t, with)
		access, _ := signedInTokens(t, c.server, c.iss, "cli")["access_token"].(string)
		request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, c.server.URL+issuer.GrantsPath, nil)
		request.Header.Set("Authorization", "Bearer "+access)
		resp, err := c.server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var answer struct {
			Cloudflare []issuer.CloudflareGrant `json:"cloudflare"`
		}
		err = json.NewDecoder(resp.Body).Decode(&answer)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("grants = %d %v", resp.StatusCode, err)
		}
		switch {
		case with && (len(answer.Cloudflare) != 1 || answer.Cloudflare[0].Preset != "dns" || answer.Cloudflare[0].Lifetime != 900):
			t.Errorf("cloudflare = %+v, want the dns preset", answer.Cloudflare)
		case !with && len(answer.Cloudflare) != 0:
			t.Errorf("cloudflare = %+v, want none", answer.Cloudflare)
		}
	}
}
