package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/storage/keys"
	"github.com/truvity/sluis/storage/keys/local"
)

var update = flag.Bool("update", false, "rewrite the golden archives under testdata")

const marker = "PLAINTEXT-MARKER-4f1c"

func testRoot(seed string) []byte {
	a := sha256.Sum256([]byte(seed))
	b := sha256.Sum256(a[:])
	return append(a[:], b[:]...)
}

// archiveKey is the Archive key over a backend rooted at seed, bound to the
// given instance.
func archiveKey(t *testing.T, seed, instance string) *keys.Key {
	t.Helper()
	b, err := local.New(testRoot(seed))
	if err != nil {
		t.Fatal(err)
	}
	set, err := keys.Open(keys.Config{Adapter: "local", Keys: map[keys.Purpose]keys.Entry{
		keys.Archive: {Key: "backup"}}}, keys.Options{Backend: b, Instance: instance})
	if err != nil {
		t.Fatal(err)
	}
	k, err := set.For(keys.Archive)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func params(id string) Params {
	t := time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)
	return Params{Installation: "example", ID: id, Layout: LayoutV5, Creator: "sluis-backup test",
		Now: func() time.Time { return t }, ChunkBytes: 64}
}

type rec struct {
	s Section
	m port.Module
	v string
}

func sample() []rec {
	var out []rec
	for i := range 7 {
		out = append(out, rec{State, port.ModuleOIDC, fmt.Sprintf("state-oidc-%d %s", i, marker)})
	}
	out = append(out, rec{Secrets, port.ModuleOIDC, "secret " + marker}, rec{Secrets, port.ModuleGitHub, "token " + marker})
	for i := range 3 {
		out = append(out, rec{Blobs, port.ModuleGoogle, fmt.Sprintf("blob-%d %s", i, strings.Repeat("x", 40))})
	}
	out = append(out, rec{State, port.ModuleBackup, ""})
	return out
}

func write(t *testing.T, store port.Blob, key Key, p Params, rs []rec) *Manifest {
	t.Helper()
	ctx := context.Background()
	w, err := NewWriter(ctx, store, key, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if err := w.Add(ctx, r.s, r.m, []byte(r.v)); err != nil {
			t.Fatal(err)
		}
	}
	m, err := w.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func collect(t *testing.T, r *Reader, s Section, m port.Module) []string {
	t.Helper()
	var out []string
	if err := r.Records(context.Background(), s, m, func(b []byte) error { out = append(out, string(b)); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := memory.New().Blobs()
	key := archiveKey(t, "a", "example")
	m := write(t, store, key, params("b1"), sample())

	if m.Format != Version || m.Installation != "example" || m.Layout != LayoutV5 || m.Created != "2026-10-10T03:00:00Z" {
		t.Fatalf("manifest %+v", m)
	}
	var mods []port.Module
	for _, e := range m.Modules {
		mods = append(mods, e.Module)
	}
	want := []port.Module{port.ModuleOIDC, port.ModuleGitHub, port.ModuleGoogle, port.ModuleBackup}
	if fmt.Sprint(mods) != fmt.Sprint(want) {
		t.Fatalf("modules %v, want %v (port order)", mods, want)
	}
	if got := m.Part(port.ModuleOIDC, State); got.Records != 7 || len(got.Chunks) < 2 {
		t.Fatalf("oidc state %+v: want 7 records in more than one chunk", got)
	}

	r, err := Open(ctx, store, key, "example", "b1")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	got := collect(t, r, State, port.ModuleOIDC)
	if len(got) != 7 || got[0] != "state-oidc-0 "+marker || got[6] != "state-oidc-6 "+marker {
		t.Fatalf("state records %q", got)
	}
	if g := collect(t, r, Secrets, port.ModuleGitHub); len(g) != 1 || g[0] != "token "+marker {
		t.Fatalf("secrets %q", g)
	}
	if g := collect(t, r, State, port.ModuleBackup); len(g) != 1 || g[0] != "" {
		t.Fatalf("an empty record must survive: %q", g)
	}
	if g := collect(t, r, Blobs, port.ModuleSlack); len(g) != 0 {
		t.Fatalf("a module that is not in the backup has records: %q", g)
	}
	if p, err := Peek(ctx, store, "example", "b1"); err != nil || p.Records(State) != m.Records(State) {
		t.Fatalf("peek %v %v", p, err)
	}
}

func TestNothingIsPlaintext(t *testing.T) {
	ctx := context.Background()
	store := memory.New().Blobs()
	write(t, store, archiveKey(t, "a", "example"), params("b1"), sample())
	names, err := store.List(ctx, Prefix("example", "b1"))
	if err != nil || len(names) < 5 {
		t.Fatalf("%v %v", names, err)
	}
	for _, n := range names {
		o, err := store.Read(ctx, n)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{marker, "xxxxxxxx", "token ", "secret ", "state-oidc-"} {
			if bytes.Contains(o.Body, []byte(bad)) {
				t.Fatalf("%s holds %q in the clear", n, bad)
			}
		}
	}
}

func mutate(t *testing.T, store port.Blob, name string, f func([]byte) []byte) {
	t.Helper()
	ctx := context.Background()
	o, err := store.Read(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, name, f(append([]byte(nil), o.Body...))); err != nil {
		t.Fatal(err)
	}
}

// verifyErr opens and verifies, and returns the first error.
func verifyErr(store port.Blob, key Key) error {
	ctx := context.Background()
	r, err := Open(ctx, store, key, "example", "b1")
	if err != nil {
		return err
	}
	defer r.Close()
	return r.Verify(ctx)
}

func setup(t *testing.T) (port.Blob, *keys.Key) {
	store := memory.New().Blobs()
	key := archiveKey(t, "a", "example")
	write(t, store, key, params("b1"), sample())
	if err := verifyErr(store, key); err != nil {
		t.Fatal(err)
	}
	return store, key
}

func TestTamperChunk(t *testing.T) {
	store, key := setup(t)
	name := ChunkName("example", "b1", State, port.ModuleOIDC, 0)
	mutate(t, store, name, func(b []byte) []byte { b[len(b)-3] ^= 1; return b })
	if err := verifyErr(store, key); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("flipped chunk byte: %v", err)
	}
}

func TestTamperChunkAndItsManifestHash(t *testing.T) {
	store, key := setup(t)
	ctx := context.Background()
	name := ChunkName("example", "b1", State, port.ModuleOIDC, 0)
	mutate(t, store, name, func(b []byte) []byte { b[len(b)-3] ^= 1; return b })
	o, _ := store.Read(ctx, name)
	sum := sha256.Sum256(o.Body)
	mutate(t, store, ManifestName("example", "b1"), func(b []byte) []byte {
		var env map[string]json.RawMessage
		_ = json.Unmarshal(b, &env)
		var m Manifest
		_ = json.Unmarshal(env["manifest"], &m)
		m.Modules[0].State.Chunks[0].SHA256 = fmt.Sprintf("%x", sum)
		env["manifest"], _ = json.Marshal(m)
		out, _ := json.Marshal(env)
		return out
	})
	if err := verifyErr(store, key); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("a rewritten manifest hash must fail the MAC: %v", err)
	}
}

func TestTamperManifest(t *testing.T) {
	cases := map[string]func(*Manifest){
		"count":   func(m *Manifest) { m.Modules[0].State.Records++; m.Modules[0].State.Chunks[0].Records++ },
		"layout":  func(m *Manifest) { m.Layout = LayoutV4 },
		"creator": func(m *Manifest) { m.Creator = "someone else" },
		"reorder": func(m *Manifest) {
			c := m.Modules[0].State.Chunks
			c[0], c[1] = c[1], c[0]
		},
		"drop a chunk": func(m *Manifest) {
			p := &m.Modules[0].State
			p.Records -= int64(p.Chunks[len(p.Chunks)-1].Records)
			p.Chunks = p.Chunks[:len(p.Chunks)-1]
		},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			store, key := setup(t)
			mutate(t, store, ManifestName("example", "b1"), func(b []byte) []byte {
				var env map[string]json.RawMessage
				_ = json.Unmarshal(b, &env)
				var m Manifest
				_ = json.Unmarshal(env["manifest"], &m)
				f(&m)
				env["manifest"], _ = json.Marshal(m)
				out, _ := json.Marshal(env)
				return out
			})
			err := verifyErr(store, key)
			if !errors.Is(err, ErrIntegrity) && !errors.Is(err, ErrFormat) {
				t.Fatalf("want a refusal, got %v", err)
			}
		})
	}
}

func TestChunkOrderAndPlacement(t *testing.T) {
	ctx := context.Background()
	store, key := setup(t)
	a := ChunkName("example", "b1", State, port.ModuleOIDC, 0)
	b := ChunkName("example", "b1", State, port.ModuleOIDC, 1)
	oa, _ := store.Read(ctx, a)
	ob, _ := store.Read(ctx, b)
	_, _ = store.Write(ctx, a, ob.Body)
	_, _ = store.Write(ctx, b, oa.Body)
	if err := verifyErr(store, key); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("swapped chunks: %v", err)
	}

	// A chunk that is moved to another place and the manifest is not touched.
	store, key = setup(t)
	src := ChunkName("example", "b1", Secrets, port.ModuleOIDC, 0)
	dst := ChunkName("example", "b1", Secrets, port.ModuleGitHub, 0)
	o, _ := store.Read(ctx, src)
	_, _ = store.Write(ctx, dst, o.Body)
	if err := verifyErr(store, key); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("moved chunk: %v", err)
	}

	// A missing chunk, and a truncated one.
	store, key = setup(t)
	_ = store.Delete(ctx, a)
	if err := verifyErr(store, key); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("missing chunk: %v", err)
	}
	store, key = setup(t)
	mutate(t, store, a, func(b []byte) []byte { return b[:len(b)-1] })
	if err := verifyErr(store, key); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("truncated chunk: %v", err)
	}
}

func TestManifestFromAnotherBackupIsRefused(t *testing.T) {
	ctx := context.Background()
	store := memory.New().Blobs()
	key := archiveKey(t, "a", "example")
	write(t, store, key, params("b1"), sample())
	write(t, store, key, params("b2"), sample())
	o, _ := store.Read(ctx, ManifestName("example", "b2"))
	_, _ = store.Write(ctx, ManifestName("example", "b1"), o.Body)
	if err := verifyErr(store, key); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("manifest of b2 read as b1: %v", err)
	}
}

func TestKeyWithoutDecryptRightFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, _ := setup(t)

	// Another key: nothing is read.
	counting := &countingStore{Blob: store}
	if _, err := Open(ctx, counting, archiveKey(t, "other", "example"), "example", "b1"); !errors.Is(err, ErrKey) {
		t.Fatalf("other key: %v", err)
	}
	if counting.chunkReads != 0 {
		t.Fatal("a chunk was read without the data key")
	}

	// The same key under another encryption context (another instance).
	renamed := archiveKey(t, "a", "renamed")
	if _, err := Open(ctx, store, renamed, "example", "b1"); !errors.Is(err, ErrKey) {
		t.Fatalf("other context: %v", err)
	}
	// ... which opens when the context it was written under is named.
	r, err := Open(ctx, store, renamed, "example", "b1", keys.WithContext(map[string]string{"instance": "example", "purpose": "archive"}))
	if err != nil {
		t.Fatal(err)
	}
	r.Close()

	// A key that refuses.
	if _, err := Open(ctx, store, denyKey{}, "example", "b1"); !errors.Is(err, ErrKey) {
		t.Fatalf("denied: %v", err)
	}
	if _, err := NewWriter(ctx, store, denyKey{}, params("b9")); err == nil {
		t.Fatal("a writer started without a data key")
	}
}

type denyKey struct{}

func (denyKey) GenerateDataKey(context.Context) (keys.DataKey, error) {
	return keys.DataKey{}, errors.New("AccessDenied")
}
func (denyKey) UnwrapDataKey(context.Context, []byte, ...keys.DecryptOption) ([]byte, error) {
	return nil, errors.New("AccessDenied")
}

type countingStore struct {
	port.Blob
	chunkReads int
}

func (c *countingStore) Read(ctx context.Context, name string) (port.Object, error) {
	if !strings.HasSuffix(name, "manifest.json") {
		c.chunkReads++
	}
	return c.Blob.Read(ctx, name)
}

func TestResume(t *testing.T) {
	ctx := context.Background()
	store := memory.New().Blobs()
	key := archiveKey(t, "a", "example")
	rs := sample()
	w, err := NewWriter(ctx, store, key, params("b1"))
	if err != nil {
		t.Fatal(err)
	}
	half := len(rs) / 2
	for _, r := range rs[:half] {
		if err := w.Add(ctx, r.s, r.m, []byte(r.v)); err != nil {
			t.Fatal(err)
		}
	}
	prog, err := w.Flush(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w.Abort()
	if bytes.Contains(mustJSON(t, prog), []byte(marker)) {
		t.Fatal("progress holds a record")
	}
	w, err = Resume(ctx, store, key, params("b1"), prog)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs[half:] {
		if err := w.Add(ctx, r.s, r.m, []byte(r.v)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := verifyErr(store, key); err != nil {
		t.Fatal(err)
	}
	r, _ := Open(ctx, store, key, "example", "b1")
	if got := collect(t, r, State, port.ModuleOIDC); len(got) != 7 {
		t.Fatalf("%q", got)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParamsAndReadablePolicy(t *testing.T) {
	ctx := context.Background()
	var store port.Blob = memory.New().Blobs()
	key := archiveKey(t, "a", "example")
	for _, p := range []Params{
		{Installation: "a/b", ID: "x", Layout: LayoutV5},
		{Installation: "a", ID: "..", Layout: LayoutV5},
		{Installation: "a", ID: "x", Layout: "v3"},
	} {
		if _, err := NewWriter(ctx, store, key, p); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
	if !Readable(Version) || Readable(Version+1) || Readable(0) || Readable(-1) {
		t.Fatal("readable policy")
	}
	// A newer format is refused before any key is used.
	store, _ = setup(t)
	mutate(t, store, ManifestName("example", "b1"), func(b []byte) []byte {
		return bytes.Replace(b, []byte(`"format": 1`), []byte(`"format": 2`), 1)
	})
	if _, err := Open(ctx, store, denyKey{}, "example", "b1"); !errors.Is(err, ErrFormat) {
		t.Fatalf("future format: %v", err)
	}
	if _, err := Open(ctx, store, key, "example", "absent"); !errors.Is(err, ErrFormat) {
		t.Fatalf("absent: %v", err)
	}
}

// The golden archive is a released format: it is opened by every later
// release. Regenerate it only for a new format version, into a new directory.
const goldenDir = "testdata/v1"

func goldenKey(t *testing.T) *keys.Key { return archiveKey(t, "golden fixture root", "example") }

func TestGoldenV1(t *testing.T) {
	ctx := context.Background()
	if *update {
		store := memory.New().Blobs()
		write(t, store, goldenKey(t), params("golden"), sample())
		names, _ := store.List(ctx, "")
		_ = os.RemoveAll(goldenDir)
		for _, n := range names {
			o, _ := store.Read(ctx, n)
			p := filepath.Join(goldenDir, filepath.FromSlash(n))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, o.Body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	store := memory.New().Blobs()
	n := 0
	err := filepath.WalkDir(goldenDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(goldenDir, p)
		n++
		_, err = store.Write(ctx, filepath.ToSlash(rel), b)
		return err
	})
	if err != nil || n < 5 {
		t.Fatalf("golden archive: %d files, %v", n, err)
	}
	r, err := Open(ctx, store, goldenKey(t), "example", "golden")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if m := r.Manifest(); m.Format != 1 || m.Layout != LayoutV5 || m.Records(State) != 8 || m.Records(Secrets) != 2 || m.Records(Blobs) != 3 {
		t.Fatalf("manifest %+v", m)
	}
	if err := r.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if got := collect(t, r, Secrets, port.ModuleOIDC); len(got) != 1 || got[0] != "secret "+marker {
		t.Fatalf("%q", got)
	}
	// The keys of the golden archive do not open another installation's.
	if _, err := Open(ctx, store, archiveKey(t, "not the fixture", "example"), "example", "golden"); !errors.Is(err, ErrKey) {
		t.Fatalf("%v", err)
	}
}
