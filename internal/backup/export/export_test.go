package export_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/backup/export"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
	statemem "github.com/truvity/sluis/storage/state/memory"
)

var ctx = context.Background()

var epoch = time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)

func clock() time.Time { return epoch }

// recipient is the identity every test backup is encrypted to.
var recipient = func() *age.X25519Identity {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		panic(err)
	}
	return id
}()

func params(t *testing.T, id string) backup.Params {
	return backup.Params{Instance: "example", ID: id, Layout: backup.LayoutV5, Creator: "export test",
		Now: clock, Recipients: []string{recipient.Recipient().String()}, TempDir: t.TempDir()}
}

func identities(context.Context) ([]age.Identity, error) { return []age.Identity{recipient}, nil }

// records is the record count of a module's section in a manifest.
func records(m *backup.Manifest, mod port.Module, s backup.Section) int64 {
	e := m.Module(mod)
	switch {
	case e == nil:
		return 0
	case s == backup.State:
		return e.State.Records
	case s == backup.Secrets:
		return e.Secrets.Records
	}
	return e.Blobs.Count
}

// fanout is the Router's routing over the memory stores: a prefix in one
// module's family reads that table, any other reads every table.
type fanout struct {
	mods *memory.Modules
	// failAfter, when set, makes the read of failPrefix fail after that many
	// records, once.
	failPrefix string
	failAfter  int
	failed     bool
}

func (f *fanout) ExportState(c context.Context, prefix string, fn func(port.Exported) error) error {
	stores := port.Modules()
	if m, ok := port.LocateModule5(prefix); ok {
		stores = []port.Module{m}
	}
	n := 0
	for _, m := range stores {
		err := f.mods.Store(m).ExportState(c, prefix, func(x port.Exported) error {
			if f.failAfter > 0 && !f.failed && prefix == f.failPrefix && n == f.failAfter {
				f.failed = true
				return errors.New("simulated interruption")
			}
			n++
			return fn(x)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (f *fanout) ExportIndex(c context.Context, prefix string, fn func(port.Exported) error) error {
	return f.mods.Store(port.ModuleOIDC).ExportIndex(c, prefix, fn)
}

type install struct {
	mods    *memory.Modules
	secrets *secretstore.StoresV5
	blob    *memory.Blobs
	// want is the expected record count per module and section.
	want map[string]int
	// planted are the values that must not appear outside a sealed chunk.
	planted []string
}

func (in *install) src(f *fanout) export.Source {
	if f == nil {
		f = &fanout{mods: in.mods}
	}
	return export.Source{State: f, Index: f, Secrets: in.secrets, Blob: in.blob}
}

func (in *install) count(m port.Module, s backup.Section, n int) {
	in.want[fmt.Sprintf("%s/%s", m, s)] += n
}

// newInstall seeds a full installation on layout v5: records in each module's
// table, secrets of every module, and blobs, together with the records a backup
// must leave out.
func newInstall(t *testing.T) *install {
	t.Helper()
	in := &install{
		mods: memory.NewModules(memory.WithClock(clock)),
		want: map[string]int{},
	}
	in.blob = memory.New().Blobs()
	root := statemem.New()
	in.secrets = secretstore.FromStoreV5(root, "")

	put := func(m port.Module, key string, ttl time.Duration, marker string) {
		t.Helper()
		if ttl == 0 && !port.Permanent(key) {
			ttl = 365 * 24 * time.Hour
		}
		val := fmt.Sprintf(`{"id":%q,"marker":%q}`, key, marker)
		if _, err := in.mods.Store(m).Put(ctx, key, []byte(val), ttl); err != nil {
			t.Fatal(err)
		}
		if marker != "" {
			in.planted = append(in.planted, marker)
		}
	}
	kept := func(m port.Module, key string, ttl time.Duration) {
		put(m, key, ttl, "STATE-MARKER-"+key)
		in.count(m, backup.State, 1)
	}
	// oidc: many tokens (a unit larger than a chunk), a keyring, the console key.
	for i := range 25 {
		kept(port.ModuleOIDC, fmt.Sprintf("tok.j%02d", i), time.Hour+time.Duration(i)*time.Minute)
	}
	kept(port.ModuleOIDC, "keyring.ES384:kid1", 0)
	kept(port.ModuleOIDC, "issuer:keyring:entry:ES384:kid1", 0)
	kept(port.ModuleOIDC, "rec.console.session-key", 0)
	kept(port.ModuleOIDC, "ses.ada.s1", 2*time.Hour)
	kept(port.ModuleOIDC, "rt.h1", 24*time.Hour)
	kept(port.ModuleOIDC, "issuer:kms:state-secret-fingerprint", 0)
	for _, set := range []struct{ key, member string }{
		{"issuer:sessions-of:ada@acme.example", "s1"},
		{"issuer:sessions-of:ada@acme.example", "s2"},
		{"issuer:sessions-for:console", "s1"},
		{"issuer:sso", "all-1"},
	} {
		if err := in.mods.Store(port.ModuleOIDC).Add(ctx, set.key, set.member, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	in.count(port.ModuleOIDC, backup.State, 3) // three distinct sets

	kept(port.ModuleGitHub, "gh.org.acme", 0)
	kept(port.ModuleGitHub, "gh.link.299386", 0)
	kept(port.ModuleGitHub, "app.gh.link", 0)
	kept(port.ModuleGitHub, "app.gh.cat.renovate", 0)
	kept(port.ModuleGitHub, "app.gh.runner.stable.acme", 0)
	kept(port.ModuleGitHub, "gate.github.acme.confirm", time.Hour)
	kept(port.ModuleSlack, "ws.slack.T01", 0)
	kept(port.ModuleSlack, "app.slack.cat.alerts", 0)
	kept(port.ModuleSlack, "rec.slack.channel.acme.ops", 0)
	kept(port.ModuleSlack, "cache.slack.user.acme.U1", time.Hour)
	kept(port.ModuleGoogle, "ws.dir.google.C01ipl6j0", 0)

	// What a backup leaves out: the maintenance record of every module, the
	// controllers' leases, notifications, gates, caches and dedupe records, and a key
	// no family names.
	for _, m := range port.Modules() {
		put(m, "rec.maintenance", 0, "")
	}
	put(port.ModuleGitHub, "lease.github-tick:acme", time.Minute, "")
	put(port.ModuleSlack, "lease.slack-tick:acme", time.Minute, "")
	put(port.ModuleGoogle, "notify.acme", time.Minute, "")
	put(port.ModuleOIDC, "gate.other.thing", time.Hour, "")
	put(port.ModuleOIDC, "cache.deadbeef.groups", time.Hour, "")
	put(port.ModuleOIDC, "dedupe.abc", time.Hour, "")
	put(port.ModuleOIDC, "anything.else", 0, "")

	// Secrets: nested apps/<id>/<ref> paths, the 8 KiB edge, and an external one.
	secret := func(m port.Module, ns, name, value string) {
		t.Helper()
		st := in.secrets.InternalStore(secretstore.Module(m))
		if ns == export.External {
			st = in.secrets.ExternalStore(secretstore.Module(m))
		}
		doc, err := state.Raw().Marshal([]byte(value))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Put(ctx, name, doc, ""); err != nil {
			t.Fatal(err)
		}
		in.planted = append(in.planted, value, base64.StdEncoding.EncodeToString([]byte(value)))
		in.count(m, backup.Secrets, 1)
	}
	secret(port.ModuleOIDC, export.Internal, "state-secret", "SECRET-VALUE-oidc-state-0001")
	secret(port.ModuleOIDC, export.Internal, "signin/google/client-secret", "SECRET-VALUE-oidc-signin-0002")
	secret(port.ModuleOIDC, export.Internal, "clients/web", "SECRET-VALUE-oidc-client-0003")
	secret(port.ModuleOIDC, export.External, "web", "SECRET-VALUE-oidc-external-0004")
	secret(port.ModuleGitHub, export.Internal, "apps/app1/r1", "SECRET-VALUE-github-app-0005")
	secret(port.ModuleGitHub, export.Internal, "apps/app1/r2", "SECRET-VALUE-github-app-0006")
	secret(port.ModuleGitHub, export.Internal, "links/ada/r1", "SECRET-VALUE-github-link-0007")
	secret(port.ModuleGitHub, export.External, "app1", "SECRET-VALUE-github-external-0008")
	secret(port.ModuleSlack, export.Internal, "workspaces/T01/r1", "SECRET-VALUE-slack-ws-0009")
	secret(port.ModuleCloudflare, export.Internal, "main/minter", "SECRET-VALUE-cloudflare-0010")
	secret(port.ModuleGoogle, export.Internal, "workspaces/C01/key", "SECRET-VALUE-google-"+strings.Repeat("k", 8<<10-len("SECRET-VALUE-google-")))
	// The backup module's own secret (the restore identity) is never exported.
	secret(port.ModuleBackup, export.Internal, "restore-identity", "SECRET-VALUE-backup-0012")
	in.count(port.ModuleBackup, backup.Secrets, -1)

	// Blobs: reports of two modules, and a snapshot that is a cache.
	blob := func(m port.Module, name string, body []byte) {
		t.Helper()
		if _, err := in.blob.Write(ctx, name, body); err != nil {
			t.Fatal(err)
		}
		if m != "" {
			in.count(m, backup.Blobs, 1)
		}
	}
	blob(port.ModuleGitHub, "github/reports/acme", []byte(`{"status":"ok"}`))
	blob(port.ModuleGitHub, "github/reports/empty", nil)
	blob(port.ModuleSlack, "slack/reports/acme", []byte(`{"status":"ok"}`))
	blob("", "google/C01ipl6j0", []byte("snapshot"))
	return in
}

func runExport(t *testing.T, in *install, store port.Blob, id string, f *fanout) *backup.Manifest {
	t.Helper()
	w, err := backup.NewWriter(params(t, id))
	if err != nil {
		t.Fatal(err)
	}
	a, err := export.Run(ctx, w, in.src(f), export.Options{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Remove()
	if err := backup.Publish(ctx, store, a); err != nil {
		t.Fatal(err)
	}
	return a.Manifest
}

// dump is every record of a backup, by "<module>/<section>"; a blob is its
// name and body.
func dump(t *testing.T, store port.Blob, id string) (map[string][]string, *backup.Manifest) {
	t.Helper()
	r, err := backup.Open(ctx, store, identities, "example", id, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	out := map[string][]string{}
	for _, m := range port.Modules() {
		for _, s := range []backup.Section{backup.State, backup.Secrets} {
			if err := r.Records(ctx, s, m, func(b []byte) error {
				out[fmt.Sprintf("%s/%s", m, s)] = append(out[fmt.Sprintf("%s/%s", m, s)], string(b))
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.Blobs(ctx, m, func(name string, b []byte) error {
			out[fmt.Sprintf("%s/blobs", m)] = append(out[fmt.Sprintf("%s/blobs", m)], name+"="+string(b))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return out, r.Manifest()
}

func TestExportFullInstallation(t *testing.T) {
	in := newInstall(t)
	store := memory.New().Blobs()
	var logs bytes.Buffer
	w, err := backup.NewWriter(params(t, "full"))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	a, err := export.Run(ctx, w, in.src(nil), export.Options{Now: clock, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Remove()
	if err := backup.Publish(ctx, store, a); err != nil {
		t.Fatal(err)
	}
	recs, m := dump(t, store, "full")

	for _, mod := range port.Modules() {
		for _, s := range backup.Sections() {
			name := fmt.Sprintf("%s/%s", mod, s)
			if got := len(recs[name]); got != in.want[name] {
				t.Errorf("%s: %d records, want %d", name, got, in.want[name])
			}
			if got := records(m, mod, s); got != int64(in.want[name]) {
				t.Errorf("%s: manifest says %d records, want %d", name, got, in.want[name])
			}
		}
	}
	if m.Module(port.ModuleBackup) != nil {
		t.Error("the backup module, which contributes nothing, is in the manifest")
	}

	// Every record decodes, carries a lifetime that is absolute, and none is a
	// record a backup leaves out.
	types := map[string]int{}
	for name, list := range recs {
		_, s, _ := strings.Cut(name, "/")
		if s == "blobs" {
			types["blob"] += len(list)
			continue
		}
		for _, raw := range list {
			rec, err := export.Decode(backup.Section(s), []byte(raw))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			switch r := rec.(type) {
			case export.State:
				types["state"]++
				if !bytes.Contains(r.Value, []byte("STATE-MARKER-"+r.Key)) {
					t.Errorf("%s: the value of %q did not survive", name, r.Key)
				}
				for _, bad := range []string{"rec.maintenance", "lease.", "notify.", "gate.other", "cache.deadbeef", "dedupe.", "anything.else"} {
					if strings.HasPrefix(r.Key, bad) {
						t.Errorf("%s: %q was exported", name, r.Key)
					}
				}
				if r.Key == "tok.j00" && r.Expires != epoch.Add(time.Hour).Format(time.RFC3339Nano) {
					t.Errorf("tok.j00 expires %q, want one hour after the export", r.Expires)
				}
				if r.Key == "rec.console.session-key" && r.Expires != "" {
					t.Errorf("a permanent record has the lifetime %q", r.Expires)
				}
			case export.Index:
				types["index"]++
				if r.Key == "issuer:sessions-of:ada@acme.example" && strings.Join(r.Members, ",") != "s1,s2" {
					t.Errorf("members %v", r.Members)
				}
			case export.Secret:
				types["secret"]++
				if strings.Contains(raw, "SECRET-VALUE-backup") {
					t.Error("the restore identity was exported")
				}
			}
		}
	}
	if types["state"] != 42 || types["index"] != 3 || types["secret"] != 11 || types["blob"] != 3 {
		t.Errorf("record types %v", types)
	}

	// The backup holds nothing in the clear: not a secret, not a State value.
	names, err := store.List(ctx, "backup/")
	if err != nil || len(names) != 2 {
		t.Fatalf("archive objects %v, %v", names, err)
	}
	for _, name := range names {
		o, err := store.Read(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range in.planted {
			if bytes.Contains(o.Body, []byte(p)) {
				t.Errorf("%s holds a planted value in the clear", name)
			}
		}
	}
	// And the run says no value in its log.
	for _, p := range in.planted {
		if strings.Contains(logs.String(), p) {
			t.Errorf("the log holds a planted value")
		}
	}
	if logs.Len() == 0 {
		t.Error("the run logged nothing")
	}
	// The planted values are in the encrypted archive, so the scan can find them.
	found := 0
	for _, list := range recs {
		for _, raw := range list {
			for _, p := range in.planted {
				if strings.Contains(raw, p) {
					found++
					break
				}
			}
		}
	}
	if found < 11 {
		t.Errorf("the decrypted records hold %d planted values; the scan proves nothing", found)
	}
}

func TestALargeBlobIsOneFile(t *testing.T) {
	in := newInstall(t)
	body := make([]byte, 20<<20+12345)
	for i := range body {
		body[i] = byte(i*31 + i>>7)
	}
	if _, err := in.blob.Write(ctx, "slack/reports/big", body); err != nil {
		t.Fatal(err)
	}
	store := memory.New().Blobs()
	m := runExport(t, in, store, "big", nil)
	if e := m.Module(port.ModuleSlack); e == nil || e.Blobs.Count != 2 || e.Blobs.Bytes != int64(len(body))+15 {
		t.Errorf("slack blobs %+v", e)
	}
	r, err := backup.Open(ctx, store, identities, "example", "big", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := map[string][]byte{}
	if err := r.Blobs(ctx, port.ModuleSlack, func(name string, b []byte) error { got[name] = bytes.Clone(b); return nil }); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got["slack/reports/big"], body) || string(got["slack/reports/acme"]) != `{"status":"ok"}` {
		t.Error("the blob did not come back whole")
	}
}

func TestAFailedRunWritesNothing(t *testing.T) {
	in := newInstall(t)
	p := params(t, "x")
	w, err := backup.NewWriter(p)
	if err != nil {
		t.Fatal(err)
	}
	f := &fanout{mods: in.mods, failPrefix: "tok.", failAfter: 3}
	if _, err := export.Run(ctx, w, in.src(f), export.Options{Now: clock}); err == nil {
		t.Fatal("an interrupted read did not fail the run")
	}
	if left, _ := os.ReadDir(p.TempDir); len(left) != 0 {
		t.Errorf("a failed run left %v", left)
	}
}

func TestModulesOption(t *testing.T) {
	in := newInstall(t)
	store := memory.New().Blobs()
	w, _ := backup.NewWriter(params(t, "one"))
	a, err := export.Run(ctx, w, in.src(nil), export.Options{Now: clock, Modules: []port.Module{port.ModuleSlack}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Remove()
	_ = store
	m := a.Manifest
	if len(m.Modules) != 1 || m.Modules[0].Module != port.ModuleSlack || m.Records(backup.State) != 3+1 {
		t.Errorf("manifest %+v", m.Modules)
	}
}

func TestDecodeRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		s   backup.Section
		rec string
	}{
		"not json":         {backup.State, `x`},
		"a later version":  {backup.State, `{"v":2,"t":"state","key":"a"}`},
		"no version":       {backup.State, `{"t":"state","key":"a"}`},
		"wrong section":    {backup.Secrets, `{"v":1,"t":"state","key":"a","value":""}`},
		"unknown type":     {backup.State, `{"v":1,"t":"other"}`},
		"unknown field":    {backup.State, `{"v":1,"t":"state","key":"a","extra":1}`},
		"no key":           {backup.State, `{"v":1,"t":"state"}`},
		"bad lifetime":     {backup.State, `{"v":1,"t":"state","key":"a","expires":"soon"}`},
		"secret no doc":    {backup.Secrets, `{"v":1,"t":"secret","ns":"internal","name":"a"}`},
		"secret namespace": {backup.Secrets, `{"v":1,"t":"secret","ns":"x","name":"a","doc":{}}`},
		"a blob record":    {backup.Blobs, `{"v":1,"t":"blob","name":"a"}`},
	} {
		if _, err := export.Decode(tc.s, []byte(tc.rec)); !errors.Is(err, export.ErrRecord) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSourceNeedsEveryPort(t *testing.T) {
	in := newInstall(t)
	store := memory.New().Blobs()
	w, _ := backup.NewWriter(params(t, "none"))
	_ = store
	src := in.src(nil)
	src.Blob = nil
	if _, err := export.Run(ctx, w, src, export.Options{}); err == nil {
		t.Error("a source without a Blob was accepted")
	}
}
