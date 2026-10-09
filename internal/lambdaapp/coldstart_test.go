package lambdaapp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/lambdaapp"
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

// coldStart opens the function once against fake, with an installation on
// layout v4 that reads its configuration from SSM: the recovery password, the
// and three confidential clients' secrets.
func coldStart(t *testing.T, fake *ssmFake) error {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "example")
	t.Setenv("AWS_REGION", "eu-central-1")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))

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
secrets: {source: ssm, root: /sluis/example, layout: v4, endpoint: `+srv.URL+`}
recovery: {enabled: true, passwordSecret: recovery/password}
adapters:
  secrets: {adapter: ssm, settings: {endpoint: `+srv.URL+`}}
  state: {adapter: memory}
  blobs: {adapter: memory}
  trigger: {adapter: memory}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	fn, err := lambdaapp.Open(context.Background(), func(k string) string {
		if k == config.EnvConfig {
			return file
		}
		return ""
	})
	if err != nil {
		return err
	}
	t.Cleanup(fn.Close)
	// The first request is what a herd sends: it reads what Open left to it.
	_, err = fn.Handler.Handle(context.Background(), json.RawMessage(`{"version":"2.0","rawPath":"/.well-known/openid-configuration",`+
		`"requestContext":{"http":{"method":"GET","path":"/.well-known/openid-configuration"}},"headers":{"host":"access.example"}}`))
	return err
}

func fixtureParams() map[string]string {
	p := map[string]string{"/sluis/example/internal/config/recovery/password": "correct horse battery staple"}
	for _, id := range []string{"grafana", "argocd", "vault"} {
		p["/sluis/example/internal/config/clients/"+id+"/secret"] = "s3cret-" + id
	}
	for i := range 12 {
		p[fmt.Sprintf("/sluis/example/internal/config/unrelated/%02d", i)] = "x"
	}
	return p
}

// reset forgets the calls counted so far.
func (f *ssmFake) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls, f.log = map[string]int{}, nil
}

// A cold start reads SSM once for the console's session key and once for each
// secret it needs to start (here the recovery password), by name, and nothing
// else: every new environment of a herd makes these calls, and SSM throttles a
// herd. No call lists a prefix, so the parameters nobody asks for (the fixture
// has fifteen) are never read. Before this a start read the whole of
// internal/config/ in pages.
func TestAColdStartReadsOnlyItsConfigurationAndSessionKeyFromSSM(t *testing.T) {
	fake := newSSMFake(fixtureParams())
	// The installation's first start creates the session key; the herd's are
	// the starts after it.
	if err := coldStart(t, fake); err != nil {
		t.Fatalf("first start: %v", err)
	}
	fake.reset()
	if err := coldStart(t, fake); err != nil {
		t.Fatalf("cold start: %v", err)
	}
	total, by := fake.count()
	if total != 2 || by["GetParameter"] != 2 {
		t.Errorf("SSM calls per cold start = %d %v, want 2 GetParameter: %q", total, by, fake.log)
	}
	for _, call := range fake.log {
		if !strings.Contains(call, "/config/recovery/password ") && !strings.Contains(call, "/console/session-key ") {
			t.Errorf("a cold start called %q", call)
		}
	}
}

// A throttled SSM is retried, with jittered backoff, before the start gives
// up: the SDK's default of three attempts is what failed the cold starts of a
// herd and turned them into 500s.
func TestAColdStartOutlastsAThrottledSSM(t *testing.T) {
	fake := newSSMFake(fixtureParams())
	if err := coldStart(t, fake); err != nil {
		t.Fatalf("first start: %v", err)
	}
	fake.reset()
	fake.throttle = 4
	if err := coldStart(t, fake); err != nil {
		t.Fatalf("cold start: %v", err)
	}
	if total, _ := fake.count(); total != 4+2 {
		t.Errorf("SSM calls = %d, want the 4 throttled and the 2 of a start: %q", total, fake.log)
	}
}
