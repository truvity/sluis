package restore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/backup/export"
	"github.com/truvity/sluis/internal/backup/restore"
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

func archiveKey(t *testing.T) *keys.Key {
	t.Helper()
	a := sha256.Sum256([]byte("restore test root"))
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

// fanout routes an export over the memory tables.
type fanout struct{ mods *memory.Modules }

func (f fanout) ExportState(c context.Context, prefix string, fn func(port.Exported) error) error {
	stores := port.Modules()
	if m, ok := port.LocateModule5(prefix); ok {
		stores = []port.Module{m}
	}
	for _, m := range stores {
		if err := f.mods.Store(m).ExportState(c, prefix, fn); err != nil {
			return err
		}
	}
	return nil
}

func (f fanout) ExportIndex(c context.Context, prefix string, fn func(port.Exported) error) error {
	return f.mods.Store(port.ModuleOIDC).ExportIndex(c, prefix, fn)
}

// side is one installation on memory stores.
type side struct {
	mods    *memory.Modules
	root    state.Store
	secrets *secretstore.StoresV5
	blob    *memory.Blobs
}

func newSide(now func() time.Time) *side {
	s := &side{mods: memory.NewModules(memory.WithClock(now)), root: statemem.New(), blob: memory.New().Blobs()}
	s.secrets = secretstore.FromStoreV5(s.root, "")
	return s
}

func (s *side) source() export.Source {
	f := fanout{s.mods}
	return export.Source{State: f, Index: f, Secrets: s.secrets, Blob: s.blob}
}

const bigBlob = export.BlobPart + 10

// seed fills a source installation. The values carry markers that must never
// reach a report.
func seed(t *testing.T, s *side) {
	t.Helper()
	put := func(m port.Module, key string, ttl time.Duration) {
		t.Helper()
		if ttl == 0 && !port.Permanent(key) {
			ttl = 365 * 24 * time.Hour
		}
		if _, err := s.mods.Store(m).Put(ctx, key, []byte(fmt.Sprintf(`{"id":%q,"marker":"STATE-MARKER"}`, key)), ttl); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 12 {
		put(port.ModuleOIDC, fmt.Sprintf("tok.j%02d", i), time.Hour+time.Duration(i)*time.Minute)
	}
	put(port.ModuleOIDC, "keyring.ES384:kid1", 0)
	put(port.ModuleOIDC, "issuer:keyring:entry:ES384:kid1", 0)
	put(port.ModuleOIDC, "rec.console.session-key", 0)
	put(port.ModuleOIDC, "ses.ada.s1", 2*time.Hour)
	put(port.ModuleOIDC, "ses.ada.short", 10*time.Minute) // expired by the restore
	put(port.ModuleOIDC, "rt.h1", 24*time.Hour)
	put(port.ModuleGitHub, "gh.org.acme", 0)
	put(port.ModuleGitHub, "app.gh.link", 0)
	put(port.ModuleSlack, "ws.slack.T01", 0)
	put(port.ModuleGoogle, "ws.dir.google.C01ipl6j0", 0)
	for _, set := range [][2]string{
		{"issuer:sessions-of:ada@acme.example", "s1"}, {"issuer:sessions-of:ada@acme.example", "s2"},
		{"issuer:sso", "all-1"}, {"issuer:keyring:index:ES384", "kid1"},
	} {
		if err := s.mods.Store(port.ModuleOIDC).Add(ctx, set[0], set[1], 3*time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	// What a backup leaves out.
	for _, m := range port.Modules() {
		put(m, "rec.maintenance", 0)
	}
	put(port.ModuleGitHub, "lease.github-tick:acme", time.Minute)

	secret := func(m port.Module, ns, name, value string) {
		t.Helper()
		st := s.secrets.InternalStore(secretstore.Module(m))
		if ns == export.External {
			st = s.secrets.ExternalStore(secretstore.Module(m))
		}
		doc, err := state.Raw().Marshal([]byte(value))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Put(ctx, name, doc, ""); err != nil {
			t.Fatal(err)
		}
	}
	secret(port.ModuleOIDC, export.Internal, "state-secret", "SECRET-VALUE-0001")
	secret(port.ModuleOIDC, export.External, "web", "SECRET-VALUE-0002")
	secret(port.ModuleGitHub, export.Internal, "apps/app1/r1", "SECRET-VALUE-0003")
	secret(port.ModuleCloudflare, export.Internal, "main/minter", "SECRET-VALUE-0004")
	secret(port.ModuleGoogle, export.Internal, "workspaces/C01/key", "SECRET-VALUE-"+strings.Repeat("k", 8<<10))
	secret(port.ModuleBackup, export.Internal, "s3/credentials", "SECRET-VALUE-0006")

	big := bytes.Repeat([]byte("B"), bigBlob)
	for name, body := range map[string][]byte{
		"reports/github/acme":  []byte(`{"status":"ok"}`),
		"reports/github/big":   big,
		"reports/github/empty": nil,
		"reports/slack/acme":   []byte(`{"status":"ok"}`),
	} {
		if _, err := s.blob.Write(ctx, name, body); err != nil {
			t.Fatal(err)
		}
	}
}

// backupOf archives a source installation into a fresh store.
func backupOf(t *testing.T, src export.Source) (port.Blob, backup.Key, *backup.Reader) {
	t.Helper()
	store := memory.New().Blobs()
	key := archiveKey(t)
	w, err := backup.NewWriter(ctx, store, key, backup.Params{Installation: "example", ID: "b1", Layout: backup.LayoutV5,
		Creator: "restore test", Now: func() time.Time { return epoch }, ChunkBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := export.Run(ctx, w, src, export.Options{Now: func() time.Time { return epoch }}); err != nil {
		t.Fatal(err)
	}
	r, err := backup.Open(ctx, store, key, "example", "b1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return store, key, r
}

// dump is every record an export of src at now holds, by "<module>/<section>",
// sorted.
func dump(t *testing.T, src export.Source, now time.Time) map[string][]string {
	t.Helper()
	store := memory.New().Blobs()
	key := archiveKey(t)
	w, err := backup.NewWriter(ctx, store, key, backup.Params{Installation: "example", ID: "d", Layout: backup.LayoutV5,
		Creator: "dump", Now: func() time.Time { return now }, ChunkBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := export.Run(ctx, w, src, export.Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	r, err := backup.Open(ctx, store, key, "example", "d")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	out := map[string][]string{}
	for _, m := range port.Modules() {
		for _, s := range backup.Sections() {
			k := fmt.Sprintf("%s/%s", m, s)
			if err := r.Records(ctx, s, m, func(b []byte) error {
				// An empty body is null or "" depending on the adapter that read it.
				out[k] = append(out[k], strings.Replace(string(b), `"data":null`, `"data":""`, 1))
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			sort.Strings(out[k])
		}
	}
	return out
}

// recorder counts and orders the writes made through the fakes, and can fail
// the n-th one.
type recorder struct {
	mu     sync.Mutex
	ops    []string
	failAt int
}

func (r *recorder) write(op string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failAt > 0 && len(r.ops)+1 == r.failAt {
		r.failAt = 0
		return errors.New("simulated write failure")
	}
	r.ops = append(r.ops, op)
	return nil
}

func (r *recorder) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.ops) }

type recTable struct {
	restore.Table
	mod port.Module
	rec *recorder
}

func (t recTable) Put(c context.Context, key string, v []byte, ttl time.Duration) (port.Revision, error) {
	if err := t.rec.write(fmt.Sprintf("state %s %s", t.mod, key)); err != nil {
		return "", err
	}
	return t.Table.Put(c, key, v, ttl)
}

func (t recTable) Create(context.Context, string, []byte, time.Duration) (port.Revision, error) {
	panic("restore uses Put")
}

func (t recTable) Update(context.Context, string, []byte, time.Duration, port.Revision) (port.Revision, error) {
	panic("restore uses Put")
}

func (t recTable) Delete(context.Context, string) error { panic("restore never deletes") }

func (t recTable) DeleteIfRevision(context.Context, string, port.Revision) error {
	panic("restore never deletes")
}

func (t recTable) Add(c context.Context, key, member string, ttl time.Duration) error {
	if err := t.rec.write(fmt.Sprintf("index %s %s", t.mod, key)); err != nil {
		return err
	}
	return t.Table.Add(c, key, member, ttl)
}

func (t recTable) Remove(c context.Context, key, member string) error {
	if err := t.rec.write(fmt.Sprintf("remove %s %s", t.mod, key)); err != nil {
		return err
	}
	return t.Table.Remove(c, key, member)
}

type recStore struct {
	state.Store
	prefix string
	rec    *recorder
}

func (s recStore) Put(c context.Context, key string, v []byte, ifRev state.Rev) (state.Rev, error) {
	if err := s.rec.write("secret " + s.prefix + key); err != nil {
		return "", err
	}
	return s.Store.Put(c, key, v, ifRev)
}

func (s recStore) Delete(context.Context, string) error { panic("restore never deletes") }

func (s recStore) Child(p string, o ...state.Option) state.Store {
	return recStore{s.Store.Child(p, o...), s.prefix + p + "/", s.rec}
}

type recBlob struct {
	port.Blob
	rec *recorder
}

func (b recBlob) Write(c context.Context, name string, body []byte) (string, error) {
	if err := b.rec.write("blob " + name); err != nil {
		return "", err
	}
	return b.Blob.Write(c, name, body)
}

func (b recBlob) WriteIfVersion(context.Context, string, []byte, string) (string, error) {
	panic("restore uses Write")
}

func (b recBlob) Delete(context.Context, string) error { panic("restore never deletes") }

// dest is an empty installation whose writes go through the recorder.
type dest struct {
	*side
	rec    *recorder
	now    time.Time
	target restore.Target
}

func newDest(at time.Time) *dest {
	d := &dest{rec: &recorder{}, now: at}
	d.side = newSide(func() time.Time { return d.now })
	d.target = restore.Target{
		Table:   func(m port.Module) restore.Table { return recTable{d.mods.Store(m), m, d.rec} },
		Secrets: secretstore.FromStoreV5(recStore{d.root, "", d.rec}, ""),
		Blob:    recBlob{d.blob, d.rec},
	}
	return d
}

func (d *dest) opts() restore.Options {
	return restore.Options{Now: func() time.Time { return d.now }}
}

func equalDumps(t *testing.T, got, want map[string][]string) {
	t.Helper()
	for k := range want {
		if len(got[k]) != len(want[k]) {
			t.Errorf("%s: %d records, want %d", k, len(got[k]), len(want[k]))
			continue
		}
		for i := range want[k] {
			if got[k][i] != want[k][i] {
				t.Errorf("%s: record %d differs\n got %.300s\nwant %.300s", k, i, got[k][i], want[k][i])
			}
		}
	}
}

func newSource(t *testing.T) *side {
	src := newSide(func() time.Time { return epoch })
	seed(t, src)
	return src
}

func TestRestoreEqualsSourceAndTTLsShrink(t *testing.T) {
	src := newSource(t)
	_, _, r := backupOf(t, src.source())
	want := dump(t, src.source(), epoch)

	d := newDest(epoch.Add(30 * time.Minute))
	rep, err := restore.Apply(ctx, r, d.target, d.opts())
	if err != nil {
		t.Fatal(err)
	}
	// The record that lived ten minutes is gone; everything else is equal.
	exp := map[string][]string{}
	for k, list := range want {
		for _, rec := range list {
			if strings.Contains(rec, "ses.ada.short") {
				continue
			}
			exp[k] = append(exp[k], rec)
		}
	}
	equalDumps(t, dump(t, d.source(), d.now), exp)
	if tot := rep.Totals(); tot.Expired != 1 || tot.Create == 0 || tot.Overwrite != 0 {
		t.Errorf("totals %+v", tot)
	}

	var ttl time.Duration
	if err := d.mods.Store(port.ModuleOIDC).ExportState(ctx, "tok.", func(x port.Exported) error {
		if x.Key == "tok.j00" {
			ttl = x.TTL
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if ttl != 30*time.Minute {
		t.Errorf("tok.j00 has %v left, want 30m (1h less the 30m that passed)", ttl)
	}
	if _, err := d.mods.Store(port.ModuleOIDC).Get(ctx, "ses.ada.short"); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("the expired record was restored: %v", err)
	}
	for _, key := range []string{"rec.maintenance", "lease.github-tick:acme"} {
		for _, m := range port.Modules() {
			if _, err := d.mods.Store(m).Get(ctx, key); err == nil {
				t.Errorf("%s was restored into %s", key, m)
			}
		}
	}
	// A second apply finds everything in place and writes nothing.
	n := d.rec.count()
	if _, err := restore.Apply(ctx, r, d.target, d.opts()); err != nil {
		t.Fatal(err)
	}
	if d.rec.count() != n {
		t.Errorf("a second apply wrote %d more", d.rec.count()-n)
	}
}

func TestPreviewWritesNothingAndShowsNoValue(t *testing.T) {
	src := newSource(t)
	_, _, r := backupOf(t, src.source())
	d := newDest(epoch.Add(30 * time.Minute))
	rep, err := restore.Preview(ctx, r, d.target, d.opts())
	if err != nil {
		t.Fatal(err)
	}
	if d.rec.count() != 0 {
		t.Fatalf("preview wrote %d times: %v", d.rec.count(), d.rec.ops)
	}
	tot := rep.Totals()
	if tot.Create == 0 || tot.Same != 0 || tot.Expired != 1 {
		t.Errorf("totals %+v", tot)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"SECRET-VALUE", "STATE-MARKER", "kkkkkk", "BBBBBB"} {
		if bytes.Contains(raw, []byte(leak)) {
			t.Errorf("the report contains %q", leak)
		}
	}
	if !bytes.Contains(raw, []byte("internal/oidc/state-secret")) {
		t.Errorf("the report does not name the secrets: %s", raw)
	}

	// After a restore the preview sees only the same, and an overwrite names its version.
	if _, err := restore.Apply(ctx, r, d.target, d.opts()); err != nil {
		t.Fatal(err)
	}
	rep, err = restore.Preview(ctx, r, d.target, d.opts())
	if err != nil {
		t.Fatal(err)
	}
	if tot := rep.Totals(); tot.Create != 0 || tot.Overwrite != 0 || tot.Same == 0 {
		t.Errorf("after a restore: %+v", tot)
	}
	if _, err := d.mods.Store(port.ModuleGitHub).Put(ctx, "gh.org.acme", []byte(`{"changed":true}`), 0); err != nil {
		t.Fatal(err)
	}
	rep, err = restore.Preview(ctx, r, d.target, d.opts())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range rep.Sections {
		for _, it := range s.Items {
			if it.Name == "gh.org.acme" && it.Action == restore.Overwrite && it.Version != "" {
				found = true
			}
		}
	}
	if !found {
		t.Error("the changed record is not reported as an overwrite with its version")
	}
}

func TestTamperedArchiveRefusedBeforeAnyWrite(t *testing.T) {
	src := newSource(t)
	store, _, r := backupOf(t, src.source())
	mf := r.Manifest()
	// Flip a byte in the last chunk of the last module read: the first writes would have happened before it.
	last := mf.Part(port.ModuleOIDC, backup.State)
	name := backup.ChunkName("example", "b1", backup.State, port.ModuleOIDC, len(last.Chunks)-1)
	o, err := store.Read(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Clone(o.Body)
	body[len(body)/2] ^= 0xff
	if _, err := store.Write(ctx, name, body); err != nil {
		t.Fatal(err)
	}
	d := newDest(epoch)
	if _, err := restore.Apply(ctx, r, d.target, d.opts()); !errors.Is(err, backup.ErrIntegrity) {
		t.Fatalf("apply: %v, want integrity failure", err)
	}
	if _, err := restore.Preview(ctx, r, d.target, d.opts()); !errors.Is(err, backup.ErrIntegrity) {
		t.Fatalf("preview: %v, want integrity failure", err)
	}
	if d.rec.count() != 0 {
		t.Errorf("%d writes happened before the refusal", d.rec.count())
	}
}

func TestOIDCKeysAreWrittenLast(t *testing.T) {
	src := newSource(t)
	_, _, r := backupOf(t, src.source())
	d := newDest(epoch)
	if _, err := restore.Apply(ctx, r, d.target, d.opts()); err != nil {
		t.Fatal(err)
	}
	ops := d.rec.ops
	isKey := func(op string) bool {
		return strings.Contains(op, "keyring.") || strings.Contains(op, "issuer:keyring:")
	}
	firstKey, lastOther, lastOIDC, lastNonOIDC := -1, -1, -1, -1
	for i, op := range ops {
		if isKey(op) && firstKey < 0 {
			firstKey = i
		}
		if !isKey(op) {
			lastOther = i
		}
		if strings.Contains(op, " oidc ") || strings.HasPrefix(op, "secret internal/oidc/") || strings.HasPrefix(op, "secret external/oidc/") {
			lastOIDC = i
		} else {
			lastNonOIDC = i
		}
	}
	if firstKey < 0 {
		t.Fatalf("no key written: %v", ops)
	}
	if firstKey < lastOther {
		t.Errorf("a key was written at %d, before the other write at %d:\n%s", firstKey, lastOther, strings.Join(ops, "\n"))
	}
	if lastNonOIDC > lastOIDC {
		t.Errorf("a non-oidc write at %d came after oidc's last at %d", lastNonOIDC, lastOIDC)
	}
	for _, op := range ops[firstKey:] {
		if !isKey(op) {
			t.Errorf("%q written among the keys", op)
		}
	}
}

func TestNonEmptyDestinationRefusedUnlessOverwrite(t *testing.T) {
	src := newSource(t)
	_, _, r := backupOf(t, src.source())
	d := newDest(epoch)
	if _, err := d.mods.Store(port.ModuleGitHub).Put(ctx, "gh.org.acme", []byte(`{"other":true}`), 0); err != nil {
		t.Fatal(err)
	}
	doc, _ := state.Raw().Marshal([]byte("DIFFERENT"))
	if _, err := d.secrets.InternalStore(secretstore.ModuleOIDC).Put(ctx, "state-secret", doc, ""); err != nil {
		t.Fatal(err)
	}
	rep, err := restore.Apply(ctx, r, d.target, d.opts())
	if !errors.Is(err, restore.ErrNotEmpty) {
		t.Fatalf("apply: %v, want ErrNotEmpty", err)
	}
	if d.rec.count() != 0 {
		t.Errorf("%d writes before the refusal", d.rec.count())
	}
	if rep == nil || rep.Totals().Overwrite != 2 {
		t.Errorf("the refusal does not report the two differing records: %+v", rep)
	}
	o := d.opts()
	o.Overwrite = true
	if _, err := restore.Apply(ctx, r, d.target, o); err != nil {
		t.Fatal(err)
	}
	equalDumps(t, dump(t, d.source(), d.now), dump(t, src.source(), epoch))
}

func TestPartialApplyThenResume(t *testing.T) {
	src := newSource(t)
	_, _, r := backupOf(t, src.source())
	d := newDest(epoch)
	d.rec.failAt = 20
	if _, err := restore.Apply(ctx, r, d.target, d.opts()); err == nil {
		t.Fatal("apply should have failed")
	}
	done := d.rec.count()
	if done != 19 {
		t.Fatalf("%d writes before the failure, want 19", done)
	}
	if _, err := restore.Apply(ctx, r, d.target, d.opts()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	equalDumps(t, dump(t, d.source(), d.now), dump(t, src.source(), epoch))
	// Nothing written before the failure was written again.
	seen := map[string]bool{}
	for _, op := range d.rec.ops {
		if strings.HasPrefix(op, "index") || strings.HasPrefix(op, "remove") {
			continue // a set is written member by member
		}
		if seen[op] {
			t.Errorf("%q written twice", op)
		}
		seen[op] = true
	}
}

func TestRecordOfAnotherModuleRefused(t *testing.T) {
	src := newSide(func() time.Time { return epoch })
	seed(t, src)
	// A github key placed in the slack table of the source: the export of
	// slack skips it, so build the archive by hand.
	store := memory.New().Blobs()
	key := archiveKey(t)
	w, err := backup.NewWriter(ctx, store, key, backup.Params{Installation: "example", ID: "x", Layout: backup.LayoutV5,
		Creator: "t", Now: func() time.Time { return epoch }, ChunkBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := json.Marshal(export.State{V: export.RecordVersion, T: export.TypeState, Key: "gh.org.acme", Value: []byte(`{}`)})
	if err := w.Add(ctx, backup.State, port.ModuleSlack, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := backup.Open(ctx, store, key, "example", "x")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	d := newDest(epoch)
	if _, err := restore.Apply(ctx, r, d.target, d.opts()); !errors.Is(err, restore.ErrArchive) {
		t.Fatalf("got %v, want ErrArchive", err)
	}
	if d.rec.count() != 0 {
		t.Error("a write happened")
	}
}
