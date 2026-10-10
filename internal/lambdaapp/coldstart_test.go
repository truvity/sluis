package lambdaapp_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/lambdaapp"
	_ "github.com/truvity/sluis/internal/lambdaapp/issuerfn"
)

// ssmFake is SSM Parameter Store over its own wire protocol (JSON 1.1, the
// operation in X-Amz-Target), so a cold start is measured through the real
// clients, retryers and all. It counts every call by operation.
type ssmFake struct {
	mu     sync.Mutex
	params map[string]string
	calls  map[string]int
	log    []string
	// throttle, while above zero, answers that many calls with
	// ThrottlingException first.
	throttle int
}

func newSSMFake(params map[string]string) *ssmFake {
	return &ssmFake{params: params, calls: map[string]int{}}
}

func (f *ssmFake) count() (total int, by map[string]int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	by = map[string]int{}
	for k, v := range f.calls {
		by[k], total = v, total+v
	}
	return total, by
}

func (f *ssmFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	op := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AmazonSSM.")
	body, _ := io.ReadAll(r.Body)
	var in struct {
		Name, Path, NextToken, Value string
		Names                        []string
		MaxResults                   int
		Overwrite                    bool
	}
	_ = json.Unmarshal(body, &in)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[op]++
	f.log = append(f.log, op+" "+in.Path+in.Name+" "+in.NextToken)
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(status int, kind string) {
		reply(status, map[string]string{"__type": kind, "message": kind})
	}
	if f.throttle > 0 {
		f.throttle--
		fail(http.StatusBadRequest, "ThrottlingException")
		return
	}
	param := func(name string) map[string]any {
		return map[string]any{"Name": name, "Value": f.params[name], "Type": "SecureString", "Version": 1}
	}
	switch op {
	case "GetParametersByPath":
		prefix := strings.TrimSuffix(in.Path, "/") + "/"
		var names []string
		for name := range f.params {
			if strings.HasPrefix(name, prefix) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		start, _ := strconv.Atoi(in.NextToken)
		size := in.MaxResults
		if size == 0 {
			size = 10
		}
		out := map[string]any{"Parameters": []any{}}
		var page []any
		for i := start; i < len(names) && i < start+size; i++ {
			page = append(page, param(names[i]))
		}
		if page != nil {
			out["Parameters"] = page
		}
		if start+size < len(names) {
			out["NextToken"] = strconv.Itoa(start + size)
		}
		reply(http.StatusOK, out)
	case "GetParameter":
		name, _, _ := strings.Cut(in.Name, ":")
		if _, ok := f.params[name]; !ok {
			fail(http.StatusBadRequest, "ParameterNotFound")
			return
		}
		reply(http.StatusOK, map[string]any{"Parameter": param(name)})
	case "GetParameters":
		var found []any
		var missing []string
		for _, n := range in.Names {
			if _, ok := f.params[n]; ok {
				found = append(found, param(n))
			} else {
				missing = append(missing, n)
			}
		}
		reply(http.StatusOK, map[string]any{"Parameters": found, "InvalidParameters": missing})
	case "PutParameter":
		if _, ok := f.params[in.Name]; ok && !in.Overwrite {
			fail(http.StatusBadRequest, "ParameterAlreadyExists")
			return
		}
		f.params[in.Name] = in.Value
		reply(http.StatusOK, map[string]any{"Version": 1, "Tier": "Standard"})
	case "DeleteParameter":
		delete(f.params, in.Name)
		reply(http.StatusOK, map[string]any{})
	default:
		fail(http.StatusBadRequest, "UnsupportedOperation")
	}
}

// layout names a storage layout the cold start is measured on, with the SSM
// names that layout gives the secrets the fixture installation holds.
type layout struct {
	name      string
	recovery  string // the recovery password
	client    string // a prefix: <prefix><id>
	suffix    string // after the client id
	session   string // the console's session key
	unrelated string
	// encode is how the layout keeps a secret's value.
	encode func(string) string
}

var (
	layoutV4 = layout{
		name: "v4", recovery: "/sluis/example/internal/config/recovery/password",
		client: "/sluis/example/internal/config/clients/", suffix: "/secret",
		session: "/sluis/example/internal/credentials/console/session-key", unrelated: "/sluis/example/internal/config/unrelated/",
		encode: func(v string) string { return v },
	}
	layoutV5 = layout{
		name: "v5", recovery: "/sluis/example/internal/oidc/recovery-password",
		client: "/sluis/example/internal/oidc/clients/", suffix: "",
		session: "/sluis/example/internal/oidc/console-session-key", unrelated: "/sluis/example/internal/oidc/unrelated/",
		// Layout v5 keeps opaque bytes as {"value": "<base64>"}.
		encode: func(v string) string { return `{"value":"` + base64.StdEncoding.EncodeToString([]byte(v)) + `"}` },
	}
)

// coldStart opens the function once against fake, with an installation on the
// layout that reads its configuration from SSM: the recovery password and
// three confidential clients' secrets. It then makes the request a herd sends
// first, the discovery document, which needs no secret.
func coldStart(t *testing.T, fake *ssmFake, l layout) (*lambdaapp.Function, error) {
	t.Helper()
	return coldStartWith(t, fake, l, "")
}

// coldStartWith is coldStart over a document with extra top-level settings.
func coldStartWith(t *testing.T, fake *ssmFake, l layout, extra string) (*lambdaapp.Function, error) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "example")
	t.Setenv("AWS_REGION", "eu-central-1")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_PROFILE", "")

	dir := t.TempDir()
	clients := ""
	for _, id := range []string{"grafana", "argocd", "vault"} {
		clients += fmt.Sprintf("  %s: { kind: confidential, secret: clients/%s/secret, requires: [all:access-roster:operator] }\n", id, id)
	}
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(`
version: 1
groups:
  all:access-roster:operator: { members: [platform@north.example] }
  all:access-roster:viewer: {}
lifetimes: { default: 12h }
clients:
  console: { kind: public, requires: [all:access-roster:operator], redirects: ["https://access.example/console/callback"] }
`+clients), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "sluis.yaml")
	if err := os.WriteFile(file, []byte(`apiVersion: sluis.truvity.github.io/sluis/v3
issuerURL: https://access.example
publicURL: https://access.example/console
policy: {file: `+filepath.Join(dir, "policy.yaml")+`}
secrets: {source: ssm, root: /sluis/example, layout: `+l.name+`, endpoint: `+srv.URL+`}
recovery: {enabled: true, passwordSecret: recovery/password}
adapters:
  secrets: {adapter: ssm, settings: {endpoint: `+srv.URL+`}}
  state: {adapter: memory}
  blobs: {adapter: memory}
  trigger: {adapter: memory}
`+extra), 0o600); err != nil {
		t.Fatal(err)
	}
	fn, err := lambdaapp.Open(context.Background(), func(k string) string {
		if k == config.EnvConfig {
			return file
		}
		return ""
	})
	if err != nil {
		return nil, err
	}
	t.Cleanup(fn.Close)
	// The first request is what a herd sends: it needs nothing Open left to it.
	_, err = fn.Handler.Handle(context.Background(), json.RawMessage(`{"version":"2.0","rawPath":"/.well-known/openid-configuration",`+
		`"requestContext":{"http":{"method":"GET","path":"/.well-known/openid-configuration"}},"headers":{"host":"access.example"}}`))
	return fn, err
}

// authenticate makes the token request of a confidential client with a refresh
// token that is not one: the client authenticates (so the issuer reads its
// secret) and the grant is refused, which is the status returned.
func authenticate(t *testing.T, fn *lambdaapp.Function, id, secret string) int {
	t.Helper()
	discovery, err := fn.Handler.Handle(context.Background(), json.RawMessage(`{"version":"2.0","rawPath":"/.well-known/openid-configuration",`+
		`"requestContext":{"http":{"method":"GET","path":"/.well-known/openid-configuration"}},"headers":{"host":"access.example"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Token string `json:"token_endpoint"`
	}
	if err = json.Unmarshal([]byte(discovery.(events.APIGatewayV2HTTPResponse).Body), &doc); err != nil || doc.Token == "" {
		t.Fatalf("discovery: %v %v", err, discovery)
	}
	tokenURL, err := url.Parse(doc.Token)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"nothing"}}.Encode()
	event, err := json.Marshal(map[string]any{
		"version": "2.0", "rawPath": tokenURL.Path, "rawQueryString": "",
		"requestContext": map[string]any{"http": map[string]any{"method": "POST", "path": tokenURL.Path}},
		"headers": map[string]string{
			"host": "access.example", "content-type": "application/x-www-form-urlencoded",
			"authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+secret)),
		},
		"body": form,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := fn.Handler.Handle(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := out.(events.APIGatewayV2HTTPResponse)
	if !ok {
		t.Fatalf("response = %T", out)
	}
	if resp.StatusCode == http.StatusUnauthorized || strings.Contains(resp.Body, "invalid_client") {
		t.Fatalf("the client did not authenticate: %d %s", resp.StatusCode, resp.Body)
	}
	return resp.StatusCode
}

func fixtureParams(l layout) map[string]string {
	p := map[string]string{l.recovery: l.encode("correct horse battery staple")}
	for _, id := range []string{"grafana", "argocd", "vault"} {
		p[l.client+id+l.suffix] = l.encode("s3cret-" + id)
	}
	for i := range 12 {
		p[fmt.Sprintf("%s%02d", l.unrelated, i)] = "x"
	}
	return p
}

// reset forgets the calls counted so far.
func (f *ssmFake) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls, f.log = map[string]int{}, nil
}

// A cold start makes no SSM call at all, on either layout: not to list a
// prefix, not to read a parameter by name, whatever the installation holds.
// Every new environment of a herd makes a cold start, and SSM throttles a herd.
// The first request (the discovery document) needs no secret either. Each
// secret is read by the request that needs it.
func TestAColdStartMakesNoSSMCall(t *testing.T) {
	for _, l := range []layout{layoutV4, layoutV5} {
		t.Run(l.name, func(t *testing.T) {
			fake := newSSMFake(fixtureParams(l))
			if _, err := coldStart(t, fake, l); err != nil {
				t.Fatalf("cold start: %v", err)
			}
			if total, by := fake.count(); total != 0 {
				t.Errorf("SSM calls per cold start = %d %v, want none: %q", total, by, fake.log)
			}
		})
	}
}

// The counter is not blind: the request that needs a secret reads it, by name,
// once, and the next request within the cache reads nothing.
func TestAClientsSecretIsReadWhenItAuthenticates(t *testing.T) {
	for _, l := range []layout{layoutV4, layoutV5} {
		t.Run(l.name, func(t *testing.T) {
			fake := newSSMFake(fixtureParams(l))
			fn, err := coldStart(t, fake, l)
			if err != nil {
				t.Fatal(err)
			}
			authenticate(t, fn, "grafana", "s3cret-grafana")
			total, by := fake.count()
			if total != 1 || by["GetParameter"] != 1 || !strings.Contains(fake.log[0], l.client+"grafana"+l.suffix) {
				t.Errorf("the first authentication made %d calls %v, want one GetParameter of grafana's secret: %q", total, by, fake.log)
			}
			authenticate(t, fn, "grafana", "s3cret-grafana")
			if total, _ = fake.count(); total != 1 {
				t.Errorf("a second authentication within the cache made %d calls in all, want 1: %q", total, fake.log)
			}
		})
	}
}

// A throttled SSM is retried, with jittered backoff, before the read gives up:
// the SDK's default of three attempts is what failed the cold starts of a herd
// and turned them into 500s. The retry now serves the first request that reads.
func TestAReadOutlastsAThrottledSSM(t *testing.T) {
	l := layoutV4
	fake := newSSMFake(fixtureParams(l))
	fn, err := coldStart(t, fake, l)
	if err != nil {
		t.Fatalf("cold start: %v", err)
	}
	fake.reset()
	fake.throttle = 4
	authenticate(t, fn, "grafana", "s3cret-grafana")
	if total, _ := fake.count(); total != 4+1 {
		t.Errorf("SSM calls = %d, want the 4 throttled and the 1 read: %q", total, fake.log)
	}
}

// kmsFake is KMS over its own wire protocol (JSON 1.1, the operation in
// X-Amz-Target), reached through AWS_ENDPOINT_URL_KMS, so the wrapped ring is
// measured through the real client. Encrypt and Decrypt are enough for it: a
// ciphertext is a handle to the plaintext and the context it was made under.
type kmsFake struct {
	mu    sync.Mutex
	blobs map[string]kmsBlob
	calls map[string]int
}

type kmsBlob struct {
	plaintext []byte
	context   map[string]string
}

func newKMSFake() *kmsFake { return &kmsFake{blobs: map[string]kmsBlob{}, calls: map[string]int{}} }

func (f *kmsFake) count() (total int, by map[string]int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	by = map[string]int{}
	for k, v := range f.calls {
		by[k], total = v, total+v
	}
	return total, by
}

func (f *kmsFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	op := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "TrentService.")
	body, _ := io.ReadAll(r.Body)
	var in struct {
		KeyID             string `json:"KeyId"`
		Plaintext         []byte
		CiphertextBlob    []byte
		EncryptionContext map[string]string
	}
	_ = json.Unmarshal(body, &in)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[op]++
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	switch op {
	case "Encrypt":
		handle := fmt.Sprintf("blob-%d", len(f.blobs))
		f.blobs[handle] = kmsBlob{plaintext: in.Plaintext, context: in.EncryptionContext}
		_ = json.NewEncoder(w).Encode(map[string]any{"CiphertextBlob": []byte(handle), "KeyId": in.KeyID})
	case "Decrypt":
		blob, ok := f.blobs[string(in.CiphertextBlob)]
		if !ok || fmt.Sprint(blob.context) != fmt.Sprint(in.EncryptionContext) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"__type": "InvalidCiphertextException", "message": "no"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Plaintext": blob.plaintext, "KeyId": in.KeyID})
	default:
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"__type": "UnsupportedOperation", "message": op})
	}
}

// wrappedLayout adds the state secret of the KMS-wrapped signing to a layout's
// fixture, and says where it lives.
func wrappedFixture(l layout) (params map[string]string, stateSecret string) {
	params = fixtureParams(l)
	stateSecret = "/sluis/example/internal/config/issuer/state-secret"
	if l.name == "v5" {
		stateSecret = "/sluis/example/internal/oidc/state-secret"
	}
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i*7 + 1)
	}
	params[stateSecret] = l.encode(base64.StdEncoding.EncodeToString(raw))
	return params, stateSecret
}

const wrappedSettings = `
instance: example
keys: {adapter: kms, sign: alias/sluis-example-sign}
signingKey:
  kmsWrapped: {stateSecret: issuer/state-secret}
`

// coldStartWrapped is a cold start of a function that signs with KMS-wrapped
// keys, over fakes of SSM and KMS, followed by the discovery request.
func coldStartWrapped(t *testing.T, l layout) (*lambdaapp.Function, *ssmFake, *kmsFake, string) {
	t.Helper()
	params, stateSecret := wrappedFixture(l)
	fake, kmsF := newSSMFake(params), newKMSFake()
	kmsSrv := httptest.NewServer(kmsF)
	t.Cleanup(kmsSrv.Close)
	t.Setenv("AWS_ENDPOINT_URL_KMS", kmsSrv.URL)
	fn, err := coldStartWith(t, fake, l, wrappedSettings)
	if err != nil {
		t.Fatalf("cold start: %v", err)
	}
	return fn, fake, kmsF, stateSecret
}

func keySet(t *testing.T, fn *lambdaapp.Function) (int, string) {
	t.Helper()
	out, err := fn.Handler.Handle(context.Background(), json.RawMessage(`{"version":"2.0","rawPath":"/keys",`+
		`"requestContext":{"http":{"method":"GET","path":"/keys"}},"headers":{"host":"access.example"}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(events.APIGatewayV2HTTPResponse)
	return resp.StatusCode, resp.Body
}

// With KMS-wrapped signing a cold start, and the discovery request after it,
// make no SSM call and no KMS call: the state secret is the seed the signing
// keys are built over, and opening them calls KMS, so both wait for the first
// request that needs a key. That is the request for the key set (/keys) or the
// first token signed; the discovery document lists the configured algorithms
// and does not.
func TestAWrappedColdStartMakesNoSSMOrKMSCall(t *testing.T) {
	for _, l := range []layout{layoutV4, layoutV5} {
		t.Run(l.name, func(t *testing.T) {
			_, fake, kmsF, _ := coldStartWrapped(t, l)
			if total, by := fake.count(); total != 0 {
				t.Errorf("SSM calls per cold start = %d %v, want none: %q", total, by, fake.log)
			}
			if total, by := kmsF.count(); total != 0 {
				t.Errorf("KMS calls per cold start = %d %v, want none", total, by)
			}
		})
	}
}

// The request that needs a key reads the state secret once, by name, and opens
// the keys; nothing after it within the cache reads or calls again.
func TestTheFirstKeySetReadsTheStateSecretOnce(t *testing.T) {
	for _, l := range []layout{layoutV4, layoutV5} {
		t.Run(l.name, func(t *testing.T) {
			fn, fake, kmsF, stateSecret := coldStartWrapped(t, l)
			if code, body := keySet(t, fn); code != http.StatusOK || !strings.Contains(body, `"keys"`) {
				t.Fatalf("/keys = %d %s", code, body)
			}
			total, by := fake.count()
			if total != 1 || by["GetParameter"] != 1 || !strings.Contains(fake.log[0], stateSecret) {
				t.Errorf("the first /keys made %d SSM calls %v, want one GetParameter of the state secret: %q", total, by, fake.log)
			}
			opened, _ := kmsF.count()
			if opened == 0 {
				t.Error("the first /keys made no KMS call: the keys were not opened")
			}
			keySet(t, fn)
			if again, _ := fake.count(); again != 1 {
				t.Errorf("a second /keys made %d SSM calls in all, want 1: %q", again, fake.log)
			}
			if again, _ := kmsF.count(); again != opened {
				t.Errorf("a second /keys made %d KMS calls in all, want %d", again, opened)
			}
		})
	}
}

// Concurrent first requests for a key share one read of the state secret and
// one opening of the keys.
func TestConcurrentFirstKeySetsReadTheStateSecretOnce(t *testing.T) {
	for _, l := range []layout{layoutV4, layoutV5} {
		t.Run(l.name, func(t *testing.T) {
			single, _, singleKMS, _ := coldStartWrapped(t, l)
			keySet(t, single)
			want, _ := singleKMS.count()

			fn, fake, kmsF, stateSecret := coldStartWrapped(t, l)
			var wg sync.WaitGroup
			var failed atomic.Int32
			start := make(chan struct{})
			for range 12 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					if code, _ := keySet(t, fn); code != http.StatusOK {
						failed.Add(1)
					}
				}()
			}
			close(start)
			wg.Wait()
			if failed.Load() != 0 {
				t.Errorf("%d of 12 concurrent /keys failed", failed.Load())
			}
			if total, by := fake.count(); total != 1 || by["GetParameter"] != 1 || !strings.Contains(fake.log[0], stateSecret) {
				t.Errorf("12 concurrent first /keys made %d SSM calls %v, want one: %q", total, by, fake.log)
			}
			if got, _ := kmsF.count(); got != want {
				t.Errorf("12 concurrent first /keys made %d KMS calls, one makes %d", got, want)
			}
		})
	}
}

// A client that authenticates and is refused its grant signs nothing, so it
// reads its own secret and not the state secret.
func TestAnAuthenticationDoesNotOpenTheKeys(t *testing.T) {
	l := layoutV4
	fn, fake, kmsF, _ := coldStartWrapped(t, l)
	authenticate(t, fn, "grafana", "s3cret-grafana")
	if total, by := fake.count(); total != 1 || !strings.Contains(fake.log[0], l.client+"grafana"+l.suffix) {
		t.Errorf("the authentication made %d SSM calls %v, want one read of the client's secret: %q", total, by, fake.log)
	}
	if total, by := kmsF.count(); total != 0 {
		t.Errorf("the authentication made %d KMS calls %v, want none", total, by)
	}
}
