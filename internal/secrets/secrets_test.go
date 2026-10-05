package secrets_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

type fakeSSM struct {
	params map[string]string
	calls  int
	fail   bool
}

func (f *fakeSSM) GetParametersByPath(
	_ context.Context, in *awsssm.GetParametersByPathInput, _ ...func(*awsssm.Options),
) (*awsssm.GetParametersByPathOutput, error) {
	f.calls++
	if f.fail {
		return nil, errors.New("throttled")
	}
	if !aws.ToBool(in.WithDecryption) || !aws.ToBool(in.Recursive) {
		return nil, errors.New("want a recursive, decrypted read")
	}
	// One parameter per page, to prove the paging.
	var names []string
	for n := range f.params {
		if strings.HasPrefix(n, aws.ToString(in.Path)+"/") {
			names = append(names, n)
		}
	}
	start := 0
	if in.NextToken != nil {
		for i, n := range sortStrings(names) {
			if n == *in.NextToken {
				start = i
			}
		}
	}
	names = sortStrings(names)
	if start >= len(names) {
		return &awsssm.GetParametersByPathOutput{}, nil
	}
	out := &awsssm.GetParametersByPathOutput{Parameters: []types.Parameter{{Name: aws.String(names[start]), Value: aws.String(f.params[names[start]])}}}
	if start+1 < len(names) {
		out.NextToken = aws.String(names[start+1])
	}
	return out, nil
}

func sortStrings(s []string) []string {
	out := append([]string{}, s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// The SSM source reads the whole prefix at once, every page, and again only
// when its copy is older than the refresh; a read that fails keeps the copy.
func TestTheSSMSourceReadsThePrefixAndRefreshes(t *testing.T) {
	f := &fakeSSM{params: map[string]string{
		"/sluis/hive/private/config/issuer/state-secret":                "seed",
		"/sluis/hive/private/config/providers/google/default/client-id": "id",
		"/sluis/hive/private/credentials/github-org/acme/key":           "not config",
		"/sluis/other/private/config/issuer/state-secret":               "another installation's",
	}}
	now := time.Unix(0, 0)
	src := &secrets.SSM{API: f, Root: "/sluis/hive", Refresh: 5 * time.Minute, Now: func() time.Time { return now }}
	ctx := context.Background()
	if v, err := src.Get(ctx, "issuer/state-secret"); err != nil || v != "seed" {
		t.Fatalf("%q %v", v, err)
	}
	if v, err := src.Get(ctx, "providers/google/default/client-id"); err != nil || v != "id" {
		t.Fatalf("%q %v", v, err)
	}
	if f.calls != 2 {
		t.Errorf("two pages, one read: %d calls", f.calls)
	}
	if _, err := src.Get(ctx, "credentials/github-org/acme/key"); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("a credential is not configuration: %v", err)
	}
	f.params["/sluis/hive/private/config/issuer/state-secret"] = "rotated"
	if v, _ := src.Get(ctx, "issuer/state-secret"); v != "seed" {
		t.Errorf("read again before the refresh: %q", v)
	}
	now = now.Add(6 * time.Minute)
	if v, _ := src.Get(ctx, "issuer/state-secret"); v != "rotated" {
		t.Errorf("not read again after the refresh: %q", v)
	}
	f.fail = true
	now = now.Add(6 * time.Minute)
	if v, err := src.Get(ctx, "issuer/state-secret"); err != nil || v != "rotated" {
		t.Errorf("a failed read lost the copy: %q %v", v, err)
	}
	if _, err := secrets.NewSSM(ctx, "/sluis", "", "", 0); err == nil {
		t.Error("a v2 root was accepted: layout v3 is /sluis/<instance>")
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
