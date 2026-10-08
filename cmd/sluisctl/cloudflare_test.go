package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvity/sluis/tokens"
)

// fakeCloudflareIssuer answers the refresh, the Cloudflare exchange and the
// grants listing. It counts the exchanges.
type fakeCloudflareIssuer struct {
	URL       string
	exchanges atomic.Int32
	lastForm  atomic.Value // map[string][]string
	lifetime  time.Duration
}

func newFakeCloudflareIssuer(t *testing.T) *fakeCloudflareIssuer {
	t.Helper()
	f := &fakeCloudflareIssuer{lifetime: 15 * time.Minute}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/.access/grants":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"grants": []map[string]any{{"audience": "aws:123456789012:admin", "kind": "exchange", "through": []string{"all:infra"}}},
				"cloudflare": []map[string]any{
					{"preset": "dns-example", "description": "DNS edits", "r2": false, "lifetime_seconds": 900},
					{"preset": "r2-archive", "description": "archive", "r2": true, "endpoint": "https://acct.r2.cloudflarestorage.com", "lifetime_seconds": 900},
				},
			})
		case r.Form.Get("grant_type") == "refresh_token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "the-sign-in", "refresh_token": "a-refresh"})
		case r.Form.Get("requested_token_type") == tokens.TypeCloudflareToken:
			f.exchanges.Add(1)
			f.lastForm.Store(map[string][]string(r.Form))
			preset := strings.TrimPrefix(r.Form.Get("audience"), "cloudflare:")
			life := f.lifetime
			body := map[string]any{
				"issued_token_type": tokens.TypeCloudflareToken, "token_type": "N_A",
				"expires_on": time.Now().Add(life).UTC().Format(time.RFC3339),
			}
			switch preset {
			case "dns-example":
				body["access_token"], body["token"] = "cf-token", "cf-token"
			case "r2-archive":
				body["access_token"], body["access_key_id"] = "key-id", "key-id"
				body["secret_access_key"], body["endpoint"] = "s3-secret", "https://acct.r2.cloudflarestorage.com"
			default:
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "invalid_target", "error_description": "no Cloudflare preset of that name is granted to this proof",
				})
				return
			}
			_ = json.NewEncoder(w).Encode(body)
		default:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_target"})
		}
	}))
	t.Cleanup(server.Close)
	f.URL = server.URL
	return f
}

func TestCloudflareTokenPrintsTheEnvironmentLineOrJSON(t *testing.T) {
	issuer := newFakeCloudflareIssuer(t)
	signedInHome(t, issuer.URL)

	env := captureStdout(t, func() error { return run([]string{"cloudflare", "token", "dns-example", "--issuer", issuer.URL}) })
	if env != "CLOUDFLARE_API_TOKEN=cf-token\n" {
		t.Errorf("env = %q", env)
	}
	// The flag may come before the preset.
	out := captureStdout(t, func() error {
		return run([]string{"cloudflare", "token", "--issuer", issuer.URL, "--format", "json", "dns-example"})
	})
	var got map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["token"] != "cf-token" {
		t.Fatalf("json = %q (%v)", out, err)
	}
	if _, err := time.Parse(time.RFC3339, got["expires_on"]); err != nil {
		t.Errorf("expires_on = %q", got["expires_on"])
	}
	if n := issuer.exchanges.Load(); n != 1 {
		t.Errorf("exchanged %d times for two reads of one credential, want 1 (the cache)", n)
	}
}

func TestCloudflareRefusalsAndMisuse(t *testing.T) {
	issuer := newFakeCloudflareIssuer(t)
	signedInHome(t, issuer.URL)

	err := run([]string{"cloudflare", "token", "nope", "--issuer", issuer.URL})
	if codeFor(err) != exitNotGranted {
		t.Errorf("a refused preset exits %d (%v), want %d", codeFor(err), err, exitNotGranted)
	}
	for _, args := range [][]string{
		{"cloudflare"},
		{"cloudflare", "other"},
		{"cloudflare", "token"},
		{"cloudflare", "token", "a", "b"},
		{"cloudflare", "token", "a:b"},
		{"cloudflare", "token", "a", "--format", "yaml"},
	} {
		if err = run(args); codeFor(err) != exitUsage {
			t.Errorf("%v exits %d (%v), want usage", args, codeFor(err), err)
		}
	}
	// A token command on an R2 preset says which command to use, and prints nothing.
	out, err := captureStdoutErr(t, func() error { return run([]string{"cloudflare", "token", "r2-archive", "--issuer", issuer.URL}) })
	if codeFor(err) != exitUsage || out != "" || !strings.Contains(err.Error(), "cloudflare r2 r2-archive") {
		t.Errorf("token on an R2 preset = %q, %v", out, err)
	}
}

// The AWS SDKs read exactly this: Version the number one, the field names as
// written, an RFC 3339 expiry, and no SessionToken.
func TestCloudflareR2IsACredentialProcessAnswer(t *testing.T) {
	issuer := newFakeCloudflareIssuer(t)
	signedInHome(t, issuer.URL)

	out := captureStdout(t, func() error { return run([]string{"cloudflare", "r2", "r2-archive", "--issuer", issuer.URL}) })
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %q", out)
	}
	if got["Version"] != float64(1) || got["AccessKeyId"] != "key-id" || got["SecretAccessKey"] != "s3-secret" {
		t.Errorf("answer = %v", got)
	}
	if _, has := got["SessionToken"]; has {
		t.Errorf("answer has a SessionToken: %v", got)
	}
	if _, err := time.Parse(time.RFC3339, got["Expiration"].(string)); err != nil {
		t.Errorf("Expiration = %v", got["Expiration"])
	}
	if len(got) != 4 {
		t.Errorf("answer has fields beyond Version, AccessKeyId, SecretAccessKey and Expiration: %v", got)
	}
	// An API token preset is not an R2 one.
	if err := run([]string{"cloudflare", "r2", "dns-example", "--issuer", issuer.URL}); codeFor(err) != exitUsage {
		t.Errorf("r2 on a token preset exits %d (%v)", codeFor(err), err)
	}
}

func TestCloudflareLifetimeIsAskedForAndKeysTheCache(t *testing.T) {
	issuer := newFakeCloudflareIssuer(t)
	signedInHome(t, issuer.URL)

	_ = captureStdout(t, func() error {
		return run([]string{"cloudflare", "token", "dns-example", "--issuer", issuer.URL, "--lifetime", "5m"})
	})
	form := issuer.lastForm.Load().(map[string][]string)
	if form["lifetime"][0] != "300" || form["audience"][0] != "cloudflare:dns-example" || form["requested_token_type"][0] != tokens.TypeCloudflareToken {
		t.Errorf("form = %v", form)
	}
	// Another lifetime is another credential.
	_ = captureStdout(t, func() error { return run([]string{"cloudflare", "token", "dns-example", "--issuer", issuer.URL}) })
	if n := issuer.exchanges.Load(); n != 2 {
		t.Errorf("exchanged %d times for two lifetimes, want 2", n)
	}
}

// The cache is renewed with a third of the lifetime left, not before and not
// after, and is readable by this account alone.
func TestCloudflareCacheRenewsWithAThirdOfTheLifetimeLeft(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := cloudflareCachePath("https://issuer.example", "cli", "dns-example", 0)
	if err != nil {
		t.Fatal(err)
	}
	minted := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	cred := tokens.CloudflareCredential{Token: "cf-token", ExpiresOn: minted.Add(15 * time.Minute)}
	if err = writeCloudflareCache(path, cred, minted); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache file = %v, %v, want 0600", info, err)
	}
	for _, tc := range []struct {
		at   time.Duration
		want bool
	}{
		{0, true}, {9 * time.Minute, true}, {10*time.Minute - time.Second, true},
		{10 * time.Minute, false}, {14 * time.Minute, false}, {16 * time.Minute, false},
	} {
		if _, ok := readCloudflareCache(path, minted.Add(tc.at)); ok != tc.want {
			t.Errorf("at +%s: served = %v, want %v", tc.at, ok, tc.want)
		}
	}
	// A damaged file is a miss, not an error.
	if err = os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readCloudflareCache(path, minted); ok {
		t.Error("a damaged cache was served")
	}
}

func TestCloudflareExchangesAgainOnceTheCacheIsDue(t *testing.T) {
	issuer := newFakeCloudflareIssuer(t)
	issuer.lifetime = 90 * time.Second // the renewal point is 60s in
	signedInHome(t, issuer.URL)

	read := func() string {
		return captureStdout(t, func() error { return run([]string{"cloudflare", "token", "dns-example", "--issuer", issuer.URL}) })
	}
	read()
	read()
	if n := issuer.exchanges.Load(); n != 1 {
		t.Fatalf("exchanged %d times, want 1", n)
	}
	// Age the cached credential: move its mint time back past the renewal point.
	path, _ := cloudflareCachePath(issuer.URL, "", "dns-example", 0)
	var cached cloudflareCached
	body, err := os.ReadFile(path)
	if err != nil {
		// The client id is whatever loadConfig defaults to; find the one file.
		files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.json"))
		if len(files) != 1 {
			t.Fatalf("cache files = %v (%v)", files, err)
		}
		path = files[0]
		body, _ = os.ReadFile(path)
	}
	_ = json.Unmarshal(body, &cached)
	cached.ExpiresOn = time.Now().Add(20 * time.Second)
	body, _ = json.Marshal(cached)
	if err = os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	read()
	if n := issuer.exchanges.Load(); n != 2 {
		t.Errorf("exchanged %d times after the credential was due, want 2", n)
	}
}

// --file reads what the secrets operator projected and signs in to nothing:
// there is no session in this home, and no issuer to ask.
func TestCloudflareR2FromTheProjectedDocument(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	expires := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	good := write("good.json", `{"schema":"cloudflare/v1","access_key_id":"key-id","secret_access_key":"s3-secret",`+
		`"endpoint":"https://acct.r2.cloudflarestorage.com","expires_on":"`+expires+`"}`)

	out := captureStdout(t, func() error { return run([]string{"cloudflare", "r2", "r2-archive", "--file", good}) })
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got["Version"] != float64(1) || got["AccessKeyId"] != "key-id" || got["SecretAccessKey"] != "s3-secret" || got["Expiration"] != expires {
		t.Errorf("answer = %v", got)
	}

	for name, body := range map[string]string{
		"expired":      `{"schema":"cloudflare/v1","access_key_id":"k","secret_access_key":"s","expires_on":"2020-01-01T00:00:00Z"}`,
		"wrong schema": `{"schema":"github/v1","access_key_id":"k","secret_access_key":"s","expires_on":"` + expires + `"}`,
		"not json":     `nope`,
		"bad expiry":   `{"schema":"cloudflare/v1","access_key_id":"k","secret_access_key":"s","expires_on":"soon"}`,
	} {
		out, err := captureStdoutErr(t, func() error {
			return run([]string{"cloudflare", "r2", "r2-archive", "--file", write(name+".json", body)})
		})
		if err == nil || out != "" {
			t.Errorf("%s: printed %q, err %v, want an error and no credential", name, out, err)
		}
	}
	if err := run([]string{"cloudflare", "r2", "r2-archive", "--file", filepath.Join(dir, "missing.json")}); err == nil {
		t.Error("a missing file was served")
	}
	// A token document is not an R2 one.
	token := write("token.json", `{"schema":"cloudflare/v1","token":"t","expires_on":"`+expires+`"}`)
	if err := run([]string{"cloudflare", "r2", "dns-example", "--file", token}); codeFor(err) != exitUsage {
		t.Errorf("a token document as R2 exits %d (%v)", codeFor(err), err)
	}
}

// The profile block is what a person's ~/.aws/config gets, byte for byte: R2
// refuses the SDK's default checksums and virtual-host addressing.
func TestR2AWSProfilesGolden(t *testing.T) {
	got, names := r2AWSProfiles("/usr/local/bin/sluisctl", "https://access.example.com", []cloudflareGrant{
		{Preset: "dns-example"},
		{Preset: "r2-archive", R2: true, Endpoint: "https://acct.r2.cloudflarestorage.com"},
		{Preset: "r2-eu", R2: true, Endpoint: "https://acct.eu.r2.cloudflarestorage.com"},
		{Preset: "r2-broken", R2: true},
	})
	const want = `
[profile r2-archive@r2]
credential_process = /usr/local/bin/sluisctl cloudflare r2 r2-archive --issuer https://access.example.com
endpoint_url = https://acct.r2.cloudflarestorage.com
region = auto
request_checksum_calculation = when_required
response_checksum_validation = when_required
s3 =
  addressing_style = path

[profile r2-eu@r2]
credential_process = /usr/local/bin/sluisctl cloudflare r2 r2-eu --issuer https://access.example.com
endpoint_url = https://acct.eu.r2.cloudflarestorage.com
region = auto
request_checksum_calculation = when_required
response_checksum_validation = when_required
s3 =
  addressing_style = path
`
	if got != want {
		t.Errorf("profiles:\n%s\nwant:\n%s", got, want)
	}
	if strings.Join(names, ",") != "r2-archive@r2,r2-eu@r2" {
		t.Errorf("names = %v", names)
	}
}

func TestAWSConfigWritesAnR2ProfilePerGrantedPreset(t *testing.T) {
	issuer := newFakeCloudflareIssuer(t)
	home := signedInHome(t, issuer.URL)
	config := filepath.Join(home, "aws-config")
	t.Setenv("AWS_CONFIG_FILE", config)

	out := captureStdout(t, func() error { return run([]string{"aws-config", "--issuer", issuer.URL}) })
	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[profile admin@123456789012]", "[profile r2-archive@r2]",
		"cloudflare r2 r2-archive --issuer " + issuer.URL,
		"endpoint_url = https://acct.r2.cloudflarestorage.com", "region = auto",
		"addressing_style = path",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("aws config lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(string(body), "dns-example") {
		t.Errorf("a token preset has an AWS profile:\n%s", body)
	}
	if !strings.Contains(out, "profile r2-archive@r2") {
		t.Errorf("stdout = %q", out)
	}
}

func TestWhoamiListsTheGrantedPresets(t *testing.T) {
	issuer := newFakeCloudflareIssuer(t)
	signedInHome(t, issuer.URL)

	out := captureStdout(t, func() error { return run([]string{"whoami", "--issuer", issuer.URL}) })
	for _, want := range []string{
		"aws:123456789012:admin", "Cloudflare presets",
		"dns-example", "token", "DNS edits",
		"r2-archive", "r2", "https://acct.r2.cloudflarestorage.com",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("whoami lacks %q:\n%s", want, out)
		}
	}
}
