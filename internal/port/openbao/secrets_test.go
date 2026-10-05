package openbao_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/openbao"
	"github.com/truvity/sluis/internal/port/porttest"
	"github.com/truvity/sluis/internal/port/secretsexport"
)

func secretsAdapter(t *testing.T, f *fake, root string) *openbao.Secrets {
	t.Helper()
	srv := newServer(t, f)
	s, err := openbao.NewSecrets(openbao.SecretsConfig{
		Config: openbao.Config{
			Address: srv, Namespace: "kernel",
			Auth: openbao.Auth{
				Method: openbao.MethodJWT, Mount: "jwt-kernel", Role: "sluis-writer",
				Token: func(context.Context) (string, error) { return "a-jwt", nil },
			},
		},
		Root: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The suite of the port, against the fake KV.
func TestSecretsConformance(t *testing.T) {
	porttest.RunSecrets(t, func(t *testing.T) port.Secrets { return secretsAdapter(t, newFake(), "sluis") })
}

func TestSecretsLayout(t *testing.T) {
	f := newFake()
	s := secretsAdapter(t, f, "sluis")
	ctx := context.Background()
	for p, want := range map[string]string{
		"credentials/slack/a/token": "sluis/private/credentials/slack/a/token",
		"config/valkey":             "sluis/private/config/valkey",
		"export/slack/alerts":       "sluis/export/slack/alerts",
		"exports/slack":             "sluis/private/exports/slack",
		"export":                    "sluis/private/export",
	} {
		if _, err := s.Put(ctx, p, []byte("x")); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.read("kernel", want); !ok {
			t.Errorf("%q: nothing at %q", p, want)
		}
	}
	// A root with an instance in it is the same layout one level down.
	g := newFake()
	if _, err := secretsAdapter(t, g, "sluis/kernel").Put(ctx, "export/a", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, ok := g.read("kernel", "sluis/kernel/export/a"); !ok {
		t.Error("root sluis/kernel: nothing at sluis/kernel/export/a")
	}
}

func TestSecretsValueShapes(t *testing.T) {
	f := newFake()
	s := secretsAdapter(t, f, "sluis")
	ctx := context.Background()
	// A private secret is one `value` field, so an operator can seed it.
	if _, err := s.Put(ctx, "config/valkey/password", []byte("pw")); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.read("kernel", "sluis/private/config/valkey/password"); len(got) != 1 || got["value"] != "pw" {
		t.Errorf("private secret fields: %v", got)
	}
	// Bytes that are not text are base64.
	if _, err := s.Put(ctx, "credentials/k/id/pem", []byte{0xff, 0x00}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.read("kernel", "sluis/private/credentials/k/id/pem"); len(got) != 1 || got["value_b64"] != "/wA=" {
		t.Errorf("binary fields: %v", got)
	}
	// An export of properties is stored as them, for a consumer's ESO, and
	// reads back the same bytes.
	obj := []byte(`{"botToken":"xoxb-1","signingSecret":"<s>"}`)
	if _, err := s.Put(ctx, "export/slack-apps/alerts", obj); err != nil {
		t.Fatal(err)
	}
	got, _ := f.read("kernel", "sluis/export/slack-apps/alerts")
	if len(got) != 2 || got["botToken"] != "xoxb-1" || got["signingSecret"] != "<s>" {
		t.Errorf("export fields: %v", got)
	}
	back, err := s.Get(ctx, "export/slack-apps/alerts")
	if err != nil || !bytes.Equal(back.Value, obj) {
		t.Errorf("export read back as %s (%v)", back.Value, err)
	}
	// Anything else under export/ keeps its bytes, and so does an object that
	// looks like the one-field form.
	for _, v := range []string{`{"b":"2","a":"1"}`, `{"value":"x"}`, `{"value_b64":"x"}`, `["a"]`, `{"n":1}`, "plain", `{}`} {
		if _, err := s.Put(ctx, "export/odd", []byte(v)); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Get(ctx, "export/odd"); err != nil || string(got.Value) != v {
			t.Errorf("%s read back as %s (%v)", v, got.Value, err)
		}
	}
}

func TestSecretsCompareAndSwapIsTheServers(t *testing.T) {
	f := newFake()
	s := secretsAdapter(t, f, "sluis")
	ctx := context.Background()
	v1, _ := s.Put(ctx, "credentials/a", []byte("1"))
	// Another writer lands between a reader's Get and its write: the server's
	// check-and-set refuses the late one, which a read-then-write would not.
	if _, err := s.Put(ctx, "credentials/a", []byte("2")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutIfVersion(ctx, "credentials/a", []byte("late"), v1); !errors.Is(err, port.ErrConflict) {
		t.Fatalf("a late writer: %v, want ErrConflict", err)
	}
	if _, err := s.PutIfVersion(ctx, "credentials/a", []byte("x"), "not-a-version"); !errors.Is(err, port.ErrConflict) {
		t.Fatalf("a version of another adapter: %v, want ErrConflict", err)
	}
	if _, err := s.PutIfVersion(ctx, "credentials/b", []byte("x"), ""); err != nil {
		t.Fatal(err)
	}
}

func TestSecretsPerExportNamespace(t *testing.T) {
	f := newFake()
	s := secretsAdapter(t, f, "sluis")
	ns, ok := port.Secrets(s).(port.NamespacedSecrets)
	if !ok {
		t.Fatal("the openbao secrets adapter has no namespaces")
	}
	devel, err := ns.In("devel")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = devel.Put(context.Background(), "export/github-runner-app/preview/truvity", []byte(`{"a":"1"}`)); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.read("devel", "sluis/export/github-runner-app/preview/truvity"); !ok {
		t.Error("nothing in the devel namespace")
	}
	if _, ok := f.read("kernel", "sluis/export/github-runner-app/preview/truvity"); ok {
		t.Error("the secret also landed in the default namespace")
	}
	if f.logins != 1 {
		t.Errorf("%d logins, want 1 (devel only: nothing was written in kernel)", f.logins)
	}
}

func TestSecretsErrors(t *testing.T) {
	f := newFake()
	s := secretsAdapter(t, f, "sluis")
	ctx := context.Background()
	f.down = true
	if _, err := s.Get(ctx, "credentials/a"); !errors.Is(err, port.ErrUnavailable) {
		t.Errorf("a sealed server: %v, want ErrUnavailable", err)
	}
	f.down = false
	f2 := newFake()
	f2.loginErr = http.StatusForbidden
	s2 := secretsAdapter(t, f2, "sluis")
	_, err := s2.Put(ctx, "credentials/a", []byte("a-very-secret-value"))
	if err == nil || errors.Is(err, port.ErrUnavailable) || strings.Contains(err.Error(), "a-very-secret-value") || strings.Contains(err.Error(), "a-jwt") {
		t.Errorf("a refused login: %v", err)
	}
}

func TestNewSecretsRefusesABadRoot(t *testing.T) {
	cfg := openbao.SecretsConfig{Config: openbao.Config{Address: "https://openbao.example", Auth: openbao.Auth{Method: "kubernetes", Role: "r"}}}
	for _, root := range []string{"sluis", "sluis/kernel"} {
		cfg.Root = root
		if _, err := openbao.NewSecrets(cfg); err != nil {
			t.Errorf("root %q: %v", root, err)
		}
	}
	for _, root := range []string{"", "/sluis", "sluis/", "sluis//x", "sluis/private", "export", "sluis/export/x", "a b"} {
		cfg.Root = root
		if _, err := openbao.NewSecrets(cfg); err == nil {
			t.Errorf("root %q accepted", root)
		}
	}
}

func TestSecretsAreRegistered(t *testing.T) {
	d, ok := port.Default.Lookup(port.ConcernSecrets, "openbao")
	if !ok || d.Status != port.StatusImplemented || d.Factory == nil || !d.SecretStore {
		t.Fatalf("secrets/openbao: %+v", d)
	}
	built, err := d.Factory(context.Background(), port.Settings{
		"address": "https://openbao.example", "namespace": "kernel", "root": "sluis",
		"auth": map[string]any{"method": "jwt", "mount": "jwt-kernel", "role": "sluis", "tokenFile": "/var/run/openbao/token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := built.(port.Secrets); !ok {
		t.Fatalf("built a %T", built)
	}
	if _, err = d.Factory(context.Background(), port.Settings{"address": "https://x", "insecure": true}); err == nil {
		t.Error("an unknown setting (insecure) was accepted")
	}
}

// The default export destination (no ports.export) honours an export entry's
// namespace on this adapter: the runner App of the preview tier lands in the
// devel namespace as properties a consumer reads, and the rest in the default.
func TestTheSecretsExportHonoursAnEntrysNamespace(t *testing.T) {
	f := newFake()
	e := secretsexport.New(secretsAdapter(t, f, "sluis"))
	ctx := context.Background()
	props := map[string]string{"github-app-id": "1", "github-private-key": "k"}
	if err := e.Put(ctx, port.ExportTarget{Namespace: "devel", Path: "github-runner-app/preview/truvity"}, props, port.ExportPatch); err != nil {
		t.Fatal(err)
	}
	if err := e.Put(ctx, port.ExportTarget{Path: "slack-apps/alerts"}, map[string]string{"bot_token": "x"}, port.ExportPatch); err != nil {
		t.Fatal(err)
	}
	got, ok := f.read("devel", "sluis/export/github-runner-app/preview/truvity")
	if !ok || got["github-app-id"] != "1" || got["github-private-key"] != "k" {
		t.Errorf("devel: %v %v", got, ok)
	}
	if got, ok = f.read("kernel", "sluis/export/slack-apps/alerts"); !ok || got["bot_token"] != "x" {
		t.Errorf("kernel: %v %v", got, ok)
	}
	// A patch keeps the other properties.
	more := map[string]string{"github-installation-id": "2"}
	if err := e.Put(ctx, port.ExportTarget{Namespace: "devel", Path: "github-runner-app/preview/truvity"}, more, port.ExportPatch); err != nil {
		t.Fatal(err)
	}
	if got, _ = f.read("devel", "sluis/export/github-runner-app/preview/truvity"); len(got) != 3 {
		t.Errorf("after a patch: %v", got)
	}
}
