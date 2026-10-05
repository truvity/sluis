package ssm_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/porttest"
	"github.com/truvity/sluis/internal/port/ssm"
)

func newSecrets(t *testing.T, f *fakeAPI, cfg ssm.Config) *ssm.Secrets {
	t.Helper()
	if cfg.Root == "" {
		cfg.Root = "/sluis/test"
	}
	s, err := ssm.NewWithAPI(f, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The suite of the port, against the fake.
func TestConformance(t *testing.T) {
	porttest.RunSecrets(t, func(t *testing.T) port.Secrets { return newSecrets(t, newFake(), ssm.Config{}) })
}

func TestLayout(t *testing.T) {
	f := newFake()
	s := newSecrets(t, f, ssm.Config{})
	ctx := context.Background()
	for p, want := range map[string]string{
		"app/token":             "/sluis/test/private/app/token",
		"export/slack/alerts":   "/sluis/test/export/slack/alerts",
		"exports/slack":         "/sluis/test/private/exports/slack",
		"export":                "/sluis/test/private/export",
		"private/export/nested": "/sluis/test/private/private/export/nested",
	} {
		if _, err := s.Put(ctx, p, []byte("x")); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.params[want]; !ok {
			t.Errorf("%q: no parameter %q in %v", p, want, keys(f))
		}
	}
	got, err := s.List(ctx, "export")
	if err != nil || !slices.Equal(got, []string{"export/slack/alerts"}) {
		t.Fatalf("List export = %v, %v", got, err)
	}
}

func TestACustomRoot(t *testing.T) {
	f := newFake()
	s := newSecrets(t, f, ssm.Config{Root: "/acme/sluis"})
	if _, err := s.Put(context.Background(), "export/a", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.params["/acme/sluis/export/a"]; !ok {
		t.Fatalf("parameters: %v", keys(f))
	}
	for _, root := range []string{"sluis", "/sluis/", "/a b"} {
		_ = root
	}
	if _, err := ssm.NewWithAPI(f, ssm.Config{}); err == nil {
		t.Error("no root was accepted: layout v3 has no default")
	}
	for _, root := range []string{"sluis", "/sluis/", "/a b"} {
		if _, err := ssm.NewWithAPI(f, ssm.Config{Root: root}); err == nil {
			t.Errorf("root %q accepted", root)
		}
	}
}

func TestEverythingIsASecureStringUnderTheConfiguredKey(t *testing.T) {
	f := newFake()
	s := newSecrets(t, f, ssm.Config{KMSKeyID: "alias/sluis"})
	if _, err := s.Put(context.Background(), "a", []byte("x")); err != nil {
		t.Fatal(err)
	}
	in := f.puts[0]
	if in.Type != types.ParameterTypeSecureString || in.KeyId == nil || *in.KeyId != "alias/sluis" {
		t.Fatalf("put %+v", in)
	}
	f2 := newFake()
	if _, err := newSecrets(t, f2, ssm.Config{}).Put(context.Background(), "a", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if f2.puts[0].KeyId != nil {
		t.Fatalf("the AWS-managed key is the default: KeyId = %q", *f2.puts[0].KeyId)
	}
}

// A value above the standard tier's 4 KiB is stored, and one that fits is not
// put in the advanced tier by the adapter.
func TestTiers(t *testing.T) {
	f := newFake()
	s := newSecrets(t, f, ssm.Config{})
	ctx := context.Background()
	if _, err := s.Put(ctx, "small", bytes.Repeat([]byte("x"), 4096)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, "big", bytes.Repeat([]byte("x"), 4097)); err != nil {
		t.Fatal(err)
	}
	if got := f.params["/sluis/test/private/small"].tier; got != types.ParameterTierStandard {
		t.Errorf("a 4 KiB value is tier %s", got)
	}
	if got := f.params["/sluis/test/private/big"].tier; got != types.ParameterTierAdvanced {
		t.Errorf("a 4 KiB + 1 value is tier %s", got)
	}
	for _, in := range f.puts {
		if in.Tier == types.ParameterTierAdvanced {
			t.Errorf("the adapter asked for the advanced tier outright")
		}
	}
}

func TestTextIsStoredAsIsAndBinaryIsMarked(t *testing.T) {
	f := newFake()
	s := newSecrets(t, f, ssm.Config{})
	ctx := context.Background()
	pem := "-----BEGIN KEY-----\nabc\n-----END KEY-----\n"
	for path, in := range map[string]string{
		"export/pem":    pem,
		"export/marked": "sluis-b64:looks-like-the-marker",
		"export/bin":    "a\x00b\xff",
		"export/empty":  "",
	} {
		if _, err := s.Put(ctx, path, []byte(in)); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		got, err := s.Get(ctx, path)
		if err != nil || string(got.Value) != in {
			t.Errorf("%s: read %q (%v), want %q", path, got.Value, err, in)
		}
	}
	if v := f.params["/sluis/test/export/pem"].value; v != pem {
		t.Errorf("a text export is stored as %q, which a consumer cannot read as it is", v)
	}
	if v := f.params["/sluis/test/export/bin"].value; !strings.HasPrefix(v, "sluis-b64:") {
		t.Errorf("a binary value is stored as %q", v)
	}
}

func TestABinaryValueThatDoesNotFitEncodedIsTooLarge(t *testing.T) {
	s := newSecrets(t, newFake(), ssm.Config{})
	if _, err := s.Put(context.Background(), "bin", bytes.Repeat([]byte{0}, 7000)); !errors.Is(err, port.ErrTooLarge) {
		t.Fatalf("7000 binary bytes: %v, want ErrTooLarge", err)
	}
}

func TestListPagesAndSorts(t *testing.T) {
	s := newSecrets(t, newFake(), ssm.Config{})
	ctx := context.Background()
	var want []string
	for i := 0; i < 37; i++ {
		p := fmt.Sprintf("app/s%02d", i)
		want = append(want, p)
		if _, err := s.Put(ctx, p, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Put(ctx, "export/z", []byte("x")); err != nil {
		t.Fatal(err)
	}
	got, err := s.List(ctx, "app")
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("List app = %v (%v)", got, err)
	}
	all, err := s.List(ctx, "")
	if err != nil || len(all) != 38 || !slices.IsSorted(all) {
		t.Fatalf("List = %d names, sorted %v (%v)", len(all), slices.IsSorted(all), err)
	}
}

// Creation is a single atomic call; a conditional write is a read and a write
// and a writer in between is overwritten. The test pins both, so the
// documentation cannot drift from the behaviour.
func TestCompareAndSwapSemantics(t *testing.T) {
	f := newFake()
	s := newSecrets(t, f, ssm.Config{})
	ctx := context.Background()

	if _, err := s.PutIfVersion(ctx, "a", []byte("1"), ""); err != nil {
		t.Fatal(err)
	}
	if *f.puts[0].Overwrite {
		t.Fatal("create sent Overwrite=true")
	}
	if _, err := s.PutIfVersion(ctx, "a", []byte("2"), ""); !errors.Is(err, port.ErrConflict) {
		t.Fatalf("create over a live secret: %v", err)
	}

	v, _ := s.Get(ctx, "a")
	f.afterGet = func() { // another writer lands after the version was read
		f.afterGet = nil
		if _, err := s.Put(ctx, "a", []byte("rival")); err != nil {
			t.Error(err)
		}
	}
	if _, err := s.PutIfVersion(ctx, "a", []byte("mine"), v.Version); err != nil {
		t.Fatalf("PutIfVersion: %v", err)
	}
	if got, _ := s.Get(ctx, "a"); string(got.Value) != "mine" {
		t.Fatalf("value %q: the documented outcome of the race is last writer wins", got.Value)
	}
}

type failing struct {
	*fakeAPI
	err error
}

func (f failing) GetParameter(context.Context, *awsssm.GetParameterInput, ...func(*awsssm.Options)) (*awsssm.GetParameterOutput, error) {
	return nil, f.err
}

func TestAnAWSFailureIsUnavailableNotAbsent(t *testing.T) {
	s, err := ssm.NewWithAPI(failing{newFake(), errors.New("AccessDeniedException")}, ssm.Config{Root: "/sluis/test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), "a"); !errors.Is(err, port.ErrUnavailable) || errors.Is(err, port.ErrNotFound) {
		t.Fatalf("Get: %v, want ErrUnavailable", err)
	}
}

func TestRegistered(t *testing.T) {
	d, ok := port.Default.Lookup(port.ConcernSecrets, "ssm")
	if !ok || d.Status != port.StatusImplemented || !d.SecretStore || !d.Requires.AWS || d.Factory == nil {
		t.Fatalf("descriptor: %+v", d)
	}
	if !d.Works(port.RuntimeKubernetes) || !d.Works(port.RuntimeLambda) || d.Works(port.RuntimeProcess) {
		t.Fatalf("runtimes: %v", d.Runtimes)
	}
	if _, err := d.Factory(context.Background(), port.Settings{"bogus": 1}); err == nil {
		t.Fatal("an unknown setting was accepted")
	}
}

func keys(f *fakeAPI) []string {
	var k []string
	for n := range f.params {
		k = append(k, n)
	}
	slices.Sort(k)
	return k
}
