package secrets_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/truvity/sluis/internal/secrets"
)

func TestTheEnvSourceReadsSLUISSECRETName(t *testing.T) {
	if got := secrets.EnvName("providers/google/default/client-secret"); got != "SLUIS_SECRET_PROVIDERS_GOOGLE_DEFAULT_CLIENT_SECRET" {
		t.Errorf("EnvName = %q", got)
	}
	t.Setenv("SLUIS_SECRET_VALKEY_PASSWORD", "hunter2")
	if v, err := (secrets.Env{}).Get(context.Background(), "valkey/password"); err != nil || v != "hunter2" {
		t.Fatalf("%q %v", v, err)
	}
	_, err := (secrets.Env{}).Get(context.Background(), "recovery/password")
	if !errors.Is(err, secrets.ErrNotFound) || !strings.Contains(err.Error(), "SLUIS_SECRET_RECOVERY_PASSWORD") {
		t.Errorf("an unset name: %v", err)
	}
}

// A file is read on every use: a rotation is seen without a restart.
func TestTheFileSourceReadsEveryUse(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "clients", "console", "secret")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	src := secrets.File{Root: dir}
	for _, want := range []string{"one", "two"} {
		if err := os.WriteFile(p, []byte(want+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if v, err := src.Get(context.Background(), secrets.ClientSecret("console")); err != nil || v != want {
			t.Fatalf("%q %v, want %q", v, err, want)
		}
	}
	for _, bad := range []string{"../etc/passwd", "a//b", "/abs", "a/../b", ""} {
		if _, err := src.Get(context.Background(), bad); err == nil || errors.Is(err, secrets.ErrNotFound) {
			t.Errorf("%q: %v, want a refusal of the name", bad, err)
		}
	}
	if _, err := src.Get(context.Background(), "absent"); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("absent: %v", err)
	}
}

// fakeSSM answers GetParameter, counting the calls; it has no other method, so
// a prefix sweep cannot compile against it.
type fakeSSM struct {
	mu      sync.Mutex
	params  map[string]string
	calls   int
	byName  map[string]int
	fail    bool
	gate    chan struct{} // when set, a read waits for it to close
	decrypt bool
}

func (f *fakeSSM) GetParameter(
	_ context.Context, in *awsssm.GetParameterInput, _ ...func(*awsssm.Options),
) (*awsssm.GetParameterOutput, error) {
	f.mu.Lock()
	f.calls++
	if f.byName == nil {
		f.byName = map[string]int{}
	}
	f.byName[aws.ToString(in.Name)]++
	fail, gate := f.fail, f.gate
	v, ok := f.params[aws.ToString(in.Name)]
	f.decrypt = aws.ToBool(in.WithDecryption)
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if fail {
		return nil, errors.New("throttled")
	}
	if !ok {
		return nil, &types.ParameterNotFound{}
	}
	return &awsssm.GetParameterOutput{Parameter: &types.Parameter{Name: in.Name, Value: aws.String(v)}}, nil
}

func (f *fakeSSM) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Building the source reads nothing; a name is read on its first use, one
// parameter per name, decrypted, and again only when its copy is older than the
// refresh.
func TestTheSSMSourceReadsOneNamePerCallAndRefreshes(t *testing.T) {
	f := &fakeSSM{params: map[string]string{
		"/sluis/example/internal/config/issuer/state-secret":                "seed",
		"/sluis/example/internal/config/providers/google/default/client-id": "id",
		"/sluis/example/private/credentials/github-org/acme/key":            "not config",
		"/sluis/other/internal/config/issuer/state-secret":                  "another installation's",
	}}
	now := time.Unix(0, 0)
	src := &secrets.SSM{API: f, Root: "/sluis/example", Refresh: 5 * time.Minute, Now: func() time.Time { return now }}
	ctx := context.Background()
	if f.count() != 0 {
		t.Fatalf("building the source made %d calls", f.count())
	}
	if v, err := src.Get(ctx, "issuer/state-secret"); err != nil || v != "seed" {
		t.Fatalf("%q %v", v, err)
	}
	if f.count() != 1 || !f.decrypt {
		t.Errorf("one name, one decrypted read: %d calls, decrypt %v", f.count(), f.decrypt)
	}
	if v, err := src.Get(ctx, "providers/google/default/client-id"); err != nil || v != "id" {
		t.Fatalf("%q %v", v, err)
	}
	if f.count() != 2 {
		t.Errorf("a second name is one more call: %d", f.count())
	}
	if _, err := src.Get(ctx, "credentials/github-org/acme/key"); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("a credential is not configuration: %v", err)
	}
	f.mu.Lock()
	f.params["/sluis/example/internal/config/issuer/state-secret"] = "rotated"
	f.mu.Unlock()
	if v, _ := src.Get(ctx, "issuer/state-secret"); v != "seed" {
		t.Errorf("read again before the refresh: %q", v)
	}
	now = now.Add(6 * time.Minute)
	if v, _ := src.Get(ctx, "issuer/state-secret"); v != "rotated" {
		t.Errorf("not read again after the refresh: %q", v)
	}
	if got := f.byName["/sluis/example/internal/config/providers/google/default/client-id"]; got != 1 {
		t.Errorf("a name nobody asked for again was read again: %d", got)
	}
	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()
	now = now.Add(6 * time.Minute)
	if v, err := src.Get(ctx, "issuer/state-secret"); err != nil || v != "rotated" {
		t.Errorf("a failed read lost the copy: %q %v", v, err)
	}
	if _, err := secrets.NewSSM(ctx, "/sluis", "", "", 0); err == nil {
		t.Error("a v2 root was accepted: an instance root is /sluis/<instance>")
	}
}

// Callers that arrive together for one name make one read.
func TestTheSSMSourceReadsOneNameOnceForConcurrentCallers(t *testing.T) {
	f := &fakeSSM{params: map[string]string{"/sluis/example/internal/config/a": "1"}, gate: make(chan struct{})}
	src := &secrets.SSM{API: f, Root: "/sluis/example"}
	const n = 16
	var wg sync.WaitGroup
	results := make(chan string, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := src.Get(context.Background(), "a")
			if err != nil {
				results <- "error: " + err.Error()
				return
			}
			results <- v
		}()
	}
	// Let every caller reach the source before the read returns.
	deadline := time.Now().Add(5 * time.Second)
	for f.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(f.gate)
	wg.Wait()
	close(results)
	for r := range results {
		if r != "1" {
			t.Errorf("a caller got %q", r)
		}
	}
	if f.count() != 1 {
		t.Errorf("%d concurrent callers made %d reads, want 1", n, f.count())
	}
}

// An absent name is remembered as absent for a short while, then asked again,
// so a parameter created after a miss is seen.
func TestTheSSMSourceRemembersAnAbsentNameBriefly(t *testing.T) {
	f := &fakeSSM{params: map[string]string{}}
	now := time.Unix(0, 0)
	src := &secrets.SSM{API: f, Root: "/sluis/example", Now: func() time.Time { return now }}
	ctx := context.Background()
	for range 5 {
		if _, err := src.Get(ctx, "late"); !errors.Is(err, secrets.ErrNotFound) {
			t.Fatalf("absent: %v", err)
		}
	}
	if f.count() != 1 {
		t.Errorf("an absent name was asked %d times within the back-off", f.count())
	}
	f.mu.Lock()
	f.params["/sluis/example/internal/config/late"] = "now"
	f.mu.Unlock()
	now = now.Add(time.Minute)
	if v, err := src.Get(ctx, "late"); err != nil || v != "now" {
		t.Errorf("a name created after a miss: %q %v", v, err)
	}
	f.mu.Lock()
	f.params["/sluis/example/internal/config/empty"] = ""
	f.mu.Unlock()
	if _, err := src.Get(ctx, "empty"); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("an empty parameter is not a secret: %v", err)
	}
}

// A converted v1 document reads each secret where v1 said it was, before the
// source behind it.
func TestALegacySourceReadsWhereV1SaidFirst(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "state")
	if err := os.WriteFile(file, []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "console"), []byte("client\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLD_VALKEY", "pw")
	t.Setenv("SLUIS_SECRET_OTHER", "next")
	src := secrets.Legacy{
		Locations: map[string]secrets.Location{"issuer/state-secret": {File: file}, "valkey/password": {Env: "OLD_VALKEY"}},
		ClientDir: dir, Next: secrets.Env{},
	}
	ctx := context.Background()
	for name, want := range map[string]string{
		"issuer/state-secret": "seed", "valkey/password": "pw", "clients/console/secret": "client", "other": "next",
	} {
		if v, err := src.Get(ctx, name); err != nil || v != want {
			t.Errorf("%s: %q %v, want %q", name, v, err, want)
		}
	}
}

// A failed read is not tried again for a while, and a copy that cannot be read
// again is served only for so long: then the source fails closed.
func TestTheSSMSourceBacksOffAndFailsClosedWhenTooStale(t *testing.T) {
	f := &fakeSSM{params: map[string]string{"/sluis/example/internal/config/a": "1"}}
	now := time.Unix(0, 0)
	src := &secrets.SSM{API: f, Root: "/sluis/example", Refresh: time.Minute, MaxStale: time.Hour, Now: func() time.Time { return now }}
	ctx := context.Background()
	if _, err := src.Get(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	f.fail = true
	now = now.Add(2 * time.Minute)
	if v, err := src.Get(ctx, "a"); err != nil || v != "1" {
		t.Fatalf("the copy was not served: %q %v", v, err)
	}
	calls := f.count()
	for range 5 {
		_, _ = src.Get(ctx, "a")
	}
	if f.count() != calls {
		t.Errorf("SSM was asked again within the back-off: %d calls", f.count()-calls)
	}
	now = now.Add(2 * time.Hour)
	if _, err := src.Get(ctx, "a"); err == nil {
		t.Error("a copy older than MaxStale was served")
	}
	f.fail = false
	now = now.Add(time.Minute)
	if v, err := src.Get(ctx, "a"); err != nil || v != "1" {
		t.Errorf("a read again after the back-off: %q %v", v, err)
	}
}

func TestANameIsNotEchoedAndTwoNamesMayNotShareAVariable(t *testing.T) {
	err := secrets.Check("hunter2 ../x")
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("Check: %v", err)
	}
	if err := secrets.CheckEnvNames([]string{"a/b", "a-b"}); err == nil || !strings.Contains(err.Error(), "SLUIS_SECRET_A_B") {
		t.Errorf("a collision: %v", err)
	}
	if err := secrets.CheckEnvNames([]string{"a/b", "a/b", "c"}); err != nil {
		t.Errorf("one name twice is no collision: %v", err)
	}
}

func TestARootNamedPrivateOrExportIsRefused(t *testing.T) {
	for _, root := range []string{"/sluis/private", "/sluis/export", "/sluis/x/export"} {
		if _, err := secrets.NewSSM(context.Background(), root, "", "", 0); err == nil {
			t.Errorf("%s was accepted", root)
		}
	}
}

// The names are read under internal/config, never private/config.
func TestSSMReadsTheInternalConfig(t *testing.T) {
	ctx := context.Background()
	api := &fakeSSM{params: map[string]string{
		"/sluis/x/private/config/a":  "old-a",
		"/sluis/x/private/config/b":  "old-b",
		"/sluis/x/internal/config/a": "v4-a",
		"/sluis/x/internal/config/c": "v4-c",
	}}
	src := &secrets.SSM{API: api, Root: "/sluis/x"}
	for name, value := range map[string]string{"a": "v4-a", "b": "", "c": "v4-c"} {
		got, err := src.Get(ctx, name)
		if value == "" {
			if !errors.Is(err, secrets.ErrNotFound) {
				t.Errorf("%s = %q, %v; want absent", name, got, err)
			}
			continue
		}
		if err != nil || got != value {
			t.Errorf("%s = %q, %v; want %q", name, got, err, value)
		}
	}
	if err := secrets.CheckRoot("/sluis/internal"); err == nil {
		t.Error("an instance named internal was accepted")
	}
	if err := secrets.CheckRoot("/sluis/external"); err == nil {
		t.Error("an instance named external was accepted")
	}
}
