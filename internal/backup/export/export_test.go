package export_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/backup/export"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/keys"
	"github.com/truvity/sluis/storage/keys/local"
	"github.com/truvity/sluis/storage/state"
	statemem "github.com/truvity/sluis/storage/state/memory"
)

var ctx = context.Background()

var epoch = time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)

func clock() time.Time { return epoch }

func archiveKey(t *testing.T) *keys.Key {
	t.Helper()
	a := sha256.Sum256([]byte("export test root"))
	b := sha256.Sum256(a[:])
	be, err := local.New(append(a[:], b[:]...))
	if err != nil {
		t.Fatal(err)
	}
	set, err := keys.Open(keys.Config{Adapter: "local", Keys: map[keys.Purpose]keys.Entry{
		keys.Archive: {Key: "backup"}}}, keys.Options{Backend: be, Instance: "example"})
	if err != nil {
		t.Fatal(err)
	}
	k, err := set.For(keys.Archive)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func params(id string, chunk int) backup.Params {
	return backup.Params{Installation: "example", ID: id, Layout: backup.LayoutV5, Creator: "export test",
		Now: clock, ChunkBytes: chunk}
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
	secret(port.ModuleBackup, export.Internal, "s3/credentials", "SECRET-VALUE-backup-0012")

	// Blobs: reports of two modules, and a snapshot that is a cache.
	blob := func(m port.Module, name string, body []byte) {
		t.Helper()
		if _, err := in.blob.Write(ctx, name, body); err != nil {
			t.Fatal(err)
		}
		if m != "" {
			in.count(m, backup.Blobs, max(1, (len(body)+export.BlobPart-1)/export.BlobPart))
		}
	}
	blob(port.ModuleGitHub, "reports/github/acme", []byte(`{"status":"ok"}`))
	blob(port.ModuleGitHub, "reports/github/empty", nil)
	blob(port.ModuleSlack, "reports/slack/acme", []byte(`{"status":"ok"}`))
	blob("", "google/C01ipl6j0", []byte("snapshot"))
	return in
}

func runExport(t *testing.T, in *install, store port.Blob, key backup.Key, id string, chunk int, f *fanout) *backup.Manifest {
	t.Helper()
	w, err := backup.NewWriter(ctx, store, key, params(id, chunk))
	if err != nil {
		t.Fatal(err)
	}
	m, err := export.Run(ctx, w, in.src(f), export.Options{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// dump is every record of a backup, by "<module>/<section>".
func dump(t *testing.T, store port.Blob, key backup.Key, id string) (map[string][]string, *backup.Manifest) {
	t.Helper()
	r, err := backup.Open(ctx, store, key, "example", id)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, m := range port.Modules() {
		for _, s := range backup.Sections() {
			if err := r.Records(ctx, s, m, func(b []byte) error {
				out[fmt.Sprintf("%s/%s", m, s)] = append(out[fmt.Sprintf("%s/%s", m, s)], string(b))
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return out, r.Manifest()
}

func TestExportFullInstallation(t *testing.T) {
	in := newInstall(t)
	store := memory.New().Blobs()
	key := archiveKey(t)
	var logs bytes.Buffer
	w, err := backup.NewWriter(ctx, store, key, params("full", 4096))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if _, err := export.Run(ctx, w, in.src(nil), export.Options{Now: clock, Log: log}); err != nil {
		t.Fatal(err)
	}
	recs, m := dump(t, store, key, "full")

	for _, mod := range port.Modules() {
		for _, s := range backup.Sections() {
			name := fmt.Sprintf("%s/%s", mod, s)
			if got := len(recs[name]); got != in.want[name] {
				t.Errorf("%s: %d records, want %d", name, got, in.want[name])
			}
			if got := m.Part(mod, s).Records; got != int64(in.want[name]) {
				t.Errorf("%s: manifest says %d records, want %d", name, got, in.want[name])
			}
		}
	}
	if len(recs["oidc/state"]) <= 3 || m.Part(port.ModuleOIDC, backup.State).Chunks == nil || len(m.Part(port.ModuleOIDC, backup.State).Chunks) < 2 {
		t.Errorf("the oidc state should span several chunks, has %d", len(m.Part(port.ModuleOIDC, backup.State).Chunks))
	}

	// Every record decodes, carries a lifetime that is absolute, and none is a
	// record a backup leaves out.
	types := map[string]int{}
	for name, list := range recs {
		_, s, _ := strings.Cut(name, "/")
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
			case export.Blob:
				types["blob"]++
			}
		}
	}
	if types["state"] != 42 || types["index"] != 3 || types["secret"] != 12 || types["blob"] != 3 {
		t.Errorf("record types %v", types)
	}

	// The backup holds nothing in the clear: not a secret, not a State value.
	names, err := store.List(ctx, "backup/")
	if err != nil || len(names) < 5 {
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
	// The planted values are in the sealed chunks, so the scan can find them.
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
	if found < 12 {
		t.Errorf("the decrypted records hold %d planted values; the scan proves nothing", found)
	}
}

func TestLargeBlobIsSplitAndReassembled(t *testing.T) {
	in := newInstall(t)
	body := make([]byte, 2*export.BlobPart+12345)
	for i := range body {
		body[i] = byte(i*31 + i>>7)
	}
	if _, err := in.blob.Write(ctx, "reports/slack/big", body); err != nil {
		t.Fatal(err)
	}
	in.count(port.ModuleSlack, backup.Blobs, 3)
	store := memory.New().Blobs()
	key := archiveKey(t)
	runExport(t, in, store, key, "big", 1<<20, nil)

	r, err := backup.Open(ctx, store, key, "example", "big")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var a export.Assembler
	got := map[string][]byte{}
	parts := 0
	if err := r.Records(ctx, backup.Blobs, port.ModuleSlack, func(rec []byte) error {
		d, err := export.Decode(backup.Blobs, rec)
		if err != nil {
			return err
		}
		parts++
		name, b, done, err := a.Add(d.(export.Blob))
		if done {
			got[name] = bytes.Clone(b)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if a.Pending() || parts != in.want["slack/blobs"] || parts != 4 {
		t.Errorf("parts %d (want 4), pending %v", parts, a.Pending())
	}
	if !bytes.Equal(got["reports/slack/big"], body) || string(got["reports/slack/acme"]) != `{"status":"ok"}` {
		t.Error("the blob did not come back whole")
	}
}

func TestAssemblerRefusesDamage(t *testing.T) {
	mk := func(part int, data string) export.Blob {
		sum := sha256.Sum256([]byte("abcd"))
		return export.Blob{V: 1, T: "blob", Name: "n", Size: 4, SHA256: fmt.Sprintf("%x", sum), Part: part, Parts: 2, Data: []byte(data)}
	}
	var a export.Assembler
	if _, _, _, err := a.Add(mk(1, "cd")); !errors.Is(err, export.ErrRecord) {
		t.Errorf("a part with no first: %v", err)
	}
	a = export.Assembler{}
	if _, _, _, err := a.Add(mk(0, "ab")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := a.Add(mk(0, "ab")); !errors.Is(err, export.ErrRecord) {
		t.Errorf("a repeated first part: %v", err)
	}
	a = export.Assembler{}
	if _, _, _, err := a.Add(mk(0, "ab")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := a.Add(mk(1, "xx")); !errors.Is(err, export.ErrRecord) {
		t.Errorf("a changed part: %v", err)
	}
	a = export.Assembler{}
	if _, _, _, err := a.Add(mk(0, "ab")); err != nil {
		t.Fatal(err)
	}
	if _, b, done, err := a.Add(mk(1, "cd")); err != nil || !done || string(b) != "abcd" {
		t.Errorf("good parts: %q %v %v", b, done, err)
	}
}

func TestResumeAfterInterruptionYieldsTheSameRecords(t *testing.T) {
	in := newInstall(t)
	store := memory.New().Blobs()
	key := archiveKey(t)

	runExport(t, in, store, key, "whole", 256, nil)
	whole, wm := dump(t, store, key, "whole")

	// First interruption inside a unit that already wrote chunks, then one at a
	// checkpoint.
	for _, tc := range []struct {
		name string
		f    func() *fanout
		stop int
	}{
		{"inside a unit", func() *fanout { return &fanout{mods: in.mods, failPrefix: "tok.", failAfter: 12} }, 0},
		{"at a checkpoint", func() *fanout { return nil }, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "resumed-" + strings.ReplaceAll(tc.name, " ", "-")
			var last *export.Checkpoint
			errStop := errors.New("stop")
			opt := export.Options{Now: clock, Checkpoint: func(_ context.Context, c export.Checkpoint) error {
				last = &c
				if tc.stop > 0 && c.Next == tc.stop {
					return errStop
				}
				return nil
			}}
			w, err := backup.NewWriter(ctx, store, key, params(id, 256))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := export.Run(ctx, w, in.src(tc.f()), opt); err == nil {
				t.Fatal("the run was not interrupted")
			}
			if last == nil {
				t.Fatal("no checkpoint before the interruption")
			}
			if _, _, err := manifestOf(store, id); err == nil {
				t.Fatal("an interrupted run left a manifest")
			}
			// The checkpoint survives a restart as JSON.
			raw, err := json.Marshal(last)
			if err != nil {
				t.Fatal(err)
			}
			var cp export.Checkpoint
			if err := json.Unmarshal(raw, &cp); err != nil {
				t.Fatal(err)
			}
			w, err = backup.Resume(ctx, store, key, params(id, 256), cp.Progress)
			if err != nil {
				t.Fatal(err)
			}
			m, err := export.Run(ctx, w, in.src(nil), export.Options{Now: clock, Resume: &cp})
			if err != nil {
				t.Fatal(err)
			}
			got, gm := dump(t, store, key, id)
			for name, list := range whole {
				if strings.Join(got[name], "\n") != strings.Join(list, "\n") {
					t.Errorf("%s differs after the resume: %d records, want %d", name, len(got[name]), len(list))
				}
			}
			for _, mod := range port.Modules() {
				for _, s := range backup.Sections() {
					if gm.Part(mod, s).Records != wm.Part(mod, s).Records {
						t.Errorf("%s/%s: %d records, want %d", mod, s, gm.Part(mod, s).Records, wm.Part(mod, s).Records)
					}
				}
			}
			_ = m
		})
	}
}

func manifestOf(store port.Blob, id string) (*backup.Manifest, bool, error) {
	m, err := backup.Peek(ctx, store, "example", id)
	return m, err == nil, err
}

func TestResumeRefusesAnotherRun(t *testing.T) {
	in := newInstall(t)
	store := memory.New().Blobs()
	key := archiveKey(t)
	w, err := backup.NewWriter(ctx, store, key, params("x", 256))
	if err != nil {
		t.Fatal(err)
	}
	_, err = export.Run(ctx, w, in.src(nil), export.Options{Now: clock, Resume: &export.Checkpoint{Units: 2, Next: 1}})
	if err == nil || !strings.Contains(err.Error(), "another run") {
		t.Errorf("a checkpoint of another run: %v", err)
	}
	if _, err := backup.Peek(ctx, store, "example", "x"); err == nil {
		t.Error("a refused run wrote a manifest")
	}
}

func TestModulesOption(t *testing.T) {
	in := newInstall(t)
	store := memory.New().Blobs()
	key := archiveKey(t)
	w, _ := backup.NewWriter(ctx, store, key, params("one", 4096))
	m, err := export.Run(ctx, w, in.src(nil), export.Options{Now: clock, Modules: []port.Module{port.ModuleSlack}})
	if err != nil {
		t.Fatal(err)
	}
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
		"blob part range":  {backup.Blobs, `{"v":1,"t":"blob","name":"a","size":0,"sha256":"` + strings.Repeat("0", 64) + `","part":1,"parts":1}`},
	} {
		if _, err := export.Decode(tc.s, []byte(tc.rec)); !errors.Is(err, export.ErrRecord) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSourceNeedsEveryPort(t *testing.T) {
	in := newInstall(t)
	store := memory.New().Blobs()
	w, _ := backup.NewWriter(ctx, store, archiveKey(t), params("none", 4096))
	src := in.src(nil)
	src.Blob = nil
	if _, err := export.Run(ctx, w, src, export.Options{}); err == nil {
		t.Error("a source without a Blob was accepted")
	}
}
