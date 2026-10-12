package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
)

var update = flag.Bool("update", false, "rewrite the golden tar under testdata")

const marker = "PLAINTEXT-MARKER-4f1c"

func identity(t *testing.T) *age.X25519Identity {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func idsOf(ids ...*age.X25519Identity) Identities {
	return func(context.Context) ([]age.Identity, error) {
		out := make([]age.Identity, len(ids))
		for i, id := range ids {
			out[i] = id
		}
		return out, nil
	}
}

func params(t *testing.T, id string, recips ...*age.X25519Identity) Params {
	at := time.Date(2026, 10, 13, 2, 0, 0, 0, time.UTC)
	var rs []string
	for _, r := range recips {
		rs = append(rs, r.Recipient().String())
	}
	return Params{Instance: "example", ID: id, Layout: LayoutV5, Creator: "sluis-backup test",
		Now: func() time.Time { return at }, Recipients: rs, TempDir: t.TempDir()}
}

type rec struct {
	s Section
	m port.Module
	v string
}

type blob struct {
	m    port.Module
	key  string
	body string
}

func sample() ([]rec, []blob) {
	var rs []rec
	for i := range 7 {
		rs = append(rs, rec{State, port.ModuleOIDC, fmt.Sprintf(`{"n":%d,"v":%q}`, i, marker)})
	}
	rs = append(rs, rec{Secrets, port.ModuleOIDC, `{"secret":"` + marker + `"}`})
	rs = append(rs, rec{Secrets, port.ModuleGitHub, `{"token":"` + marker + `"}`})
	bs := []blob{
		{port.ModuleGitHub, "github/reports/org-b/latest.json", `{"b":"` + marker + `"}`},
		{port.ModuleGitHub, "github/reports/org-a/latest.json", `{"a":1}`},
		{port.ModuleSlack, "slack/reports/../odd key/%x.json", strings.Repeat("y", 300)},
		{port.ModuleSlack, "slack/reports/empty", ""},
	}
	return rs, bs
}

// write makes a backup of the sample and publishes it.
func write(t *testing.T, store port.Blob, p Params) *Archive {
	t.Helper()
	ctx := context.Background()
	w, err := NewWriter(p)
	if err != nil {
		t.Fatal(err)
	}
	rs, bs := sample()
	if err := w.Include(port.ModuleCloudflare); err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if err := w.Add(ctx, r.s, r.m, []byte(r.v)); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range bs {
		if err := w.AddBlob(ctx, b.m, b.key, []byte(b.body)); err != nil {
			t.Fatal(err)
		}
	}
	a, err := w.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Remove)
	if store != nil {
		if err := Publish(ctx, store, a); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func open(t *testing.T, store port.Blob, ids Identities) (*Reader, error) {
	t.Helper()
	r, err := Open(context.Background(), store, ids, "example", "b1", t.TempDir())
	if r != nil {
		t.Cleanup(r.Close)
	}
	return r, err
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
	store := memory.New().Blobs()
	id := identity(t)
	a := write(t, store, params(t, "b1", id))
	r, err := open(t, store, idsOf(id))
	if err != nil {
		t.Fatal(err)
	}
	rs, bs := sample()
	for _, m := range port.Modules() {
		for _, s := range []Section{State, Secrets} {
			var want []string
			for _, x := range rs {
				if x.s == s && x.m == m {
					want = append(want, x.v)
				}
			}
			if got := collect(t, r, s, m); !slices.Equal(got, want) {
				t.Errorf("%s/%s: %q, want %q", m, s, got, want)
			}
		}
		got := map[string]string{}
		if err := r.Blobs(context.Background(), m, func(k string, b []byte) error { got[k] = string(b); return nil }); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{}
		for _, b := range bs {
			if b.m == m {
				want[b.key] = b.body
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s blobs: %v, want %v", m, got, want)
		}
	}
	mf := r.Manifest()
	if mf.Format != 2 || mf.Instance != "example" || mf.ID != "b1" || mf.Created != "2026-10-13T02:00:00Z" {
		t.Errorf("manifest %+v", mf)
	}
	var mods []port.Module
	for _, e := range mf.Modules {
		mods = append(mods, e.Module)
	}
	if want := []port.Module{port.ModuleOIDC, port.ModuleGitHub, port.ModuleSlack, port.ModuleCloudflare}; !slices.Equal(mods, want) {
		t.Errorf("modules %v, want %v", mods, want)
	}
	if mf.Records(State) != 7 || mf.Records(Secrets) != 2 || mf.Records(Blobs) != 4 {
		t.Errorf("counts %d %d %d", mf.Records(State), mf.Records(Secrets), mf.Records(Blobs))
	}
	sc := r.Sidecar()
	if sc.Object.Key != "backup/example/b1.tar.gz.age" || sc.Object.Size != a.Sidecar.Object.Size || len(sc.Recipients) != 1 ||
		sc.Recipients[0] != id.Recipient().String() || sc.Records() != 13 {
		t.Errorf("sidecar %+v", sc)
	}
	names, _ := store.List(context.Background(), "")
	if want := []string{"backup/example/b1.json", "backup/example/b1.tar.gz.age"}; !slices.Equal(names, want) {
		t.Errorf("objects %v, want %v", names, want)
	}
}

// Nothing of a value is readable in the bucket, and no file is left behind.
func TestNothingIsPlaintextAndNothingIsLeft(t *testing.T) {
	store := memory.New().Blobs()
	id := identity(t)
	p := params(t, "b1", id)
	a := write(t, store, p)
	a.Remove()
	names, _ := store.List(context.Background(), "")
	for _, n := range names {
		o, _ := store.Read(context.Background(), n)
		if bytes.Contains(o.Body, []byte(marker)) || bytes.Contains(o.Body, []byte("org-b")) {
			t.Errorf("%s holds plaintext", n)
		}
	}
	if left, _ := os.ReadDir(p.TempDir); len(left) != 0 {
		t.Errorf("temp dir holds %v", left)
	}
}

func TestTwoRecipientsEachDecrypt(t *testing.T) {
	store := memory.New().Blobs()
	a, b := identity(t), identity(t)
	write(t, store, params(t, "b1", a, b))
	for _, id := range []*age.X25519Identity{a, b} {
		if _, err := open(t, store, idsOf(id)); err != nil {
			t.Errorf("%s: %v", id.Recipient(), err)
		}
	}
}

func TestWrongIdentityIsRefused(t *testing.T) {
	store := memory.New().Blobs()
	write(t, store, params(t, "b1", identity(t)))
	if _, err := open(t, store, idsOf(identity(t))); !errors.Is(err, ErrKey) {
		t.Fatalf("%v, want ErrKey", err)
	}
	failing := func(context.Context) ([]age.Identity, error) { return nil, errors.New("no secret") }
	if _, err := open(t, store, failing); !errors.Is(err, ErrKey) {
		t.Fatalf("%v, want ErrKey", err)
	}
}

func TestRecipientsAreRequired(t *testing.T) {
	p := params(t, "b1")
	if _, err := NewWriter(p); err == nil || !strings.Contains(err.Error(), "recipients") {
		t.Errorf("no recipients: %v", err)
	}
	p.Recipients = []string{"AGE-SECRET-KEY-1QQQ"}
	if _, err := NewWriter(p); err == nil {
		t.Error("a non-recipient was taken")
	}
	p.Recipients = []string{"age1notakey"}
	if _, err := NewWriter(p); err == nil {
		t.Error("a malformed recipient was taken")
	}
}

func TestRecordsOfOneFileComeTogether(t *testing.T) {
	ctx := context.Background()
	w, err := NewWriter(params(t, "b1", identity(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Abort()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(w.Add(ctx, State, port.ModuleOIDC, []byte(`{}`)))
	must(w.Add(ctx, Secrets, port.ModuleOIDC, []byte(`{}`)))
	if err := w.Add(ctx, State, port.ModuleOIDC, []byte(`{}`)); err == nil {
		t.Error("a file was reopened")
	}
	if err := w.Add(ctx, State, port.ModuleGitHub, []byte("two\nlines")); err == nil {
		t.Error("a record of two lines was taken")
	}
	if err := w.Add(ctx, Blobs, port.ModuleGitHub, []byte(`{}`)); err == nil {
		t.Error("a record in the blobs section was taken")
	}
	must(w.AddBlob(ctx, port.ModuleGitHub, "a", nil))
	if err := w.AddBlob(ctx, port.ModuleGitHub, "a", nil); err == nil {
		t.Error("a blob was taken twice")
	}
}

// tarOf decrypts and decompresses an archive: the golden is the tar, since the
// age stream is random by design.
func tarOf(t *testing.T, path string, id age.Identity) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	ar, err := age.Decrypt(f, id)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(ar)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func members(t *testing.T, tarball []byte) (names []string, hdrs []*tar.Header, bodies map[string][]byte) {
	t.Helper()
	bodies = map[string][]byte{}
	tr := tar.NewReader(bytes.NewReader(tarball))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names, hdrs, bodies
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		names, hdrs, bodies[h.Name] = append(names, h.Name), append(hdrs, h), b
	}
}

// The tar is deterministic: the same backup gives the same bytes, in the
// documented order, and testdata/v2/example.tar pins them.
func TestTheTarIsDeterministicAndGolden(t *testing.T) {
	id := identity(t)
	one := tarOf(t, write(t, nil, params(t, "b1", id)).Path, id)
	two := tarOf(t, write(t, nil, params(t, "b1", id)).Path, id)
	if !bytes.Equal(one, two) {
		t.Fatal("two runs of the same backup differ")
	}
	names, hdrs, bodies := members(t, one)
	want := []string{"manifest.json",
		"oidc/state.jsonl", "oidc/secrets.jsonl",
		"github/state.jsonl", "github/secrets.jsonl",
		"github/blobs/github/reports/org-a/latest.json", "github/blobs/github/reports/org-b/latest.json",
		"slack/state.jsonl", "slack/secrets.jsonl",
		"slack/blobs/slack/reports/%2E./odd%20key/%25x.json", "slack/blobs/slack/reports/empty",
		"cloudflare/state.jsonl", "cloudflare/secrets.jsonl"}
	if !slices.Equal(names, want) {
		t.Errorf("members\n%q\nwant\n%q", names, want)
	}
	for _, h := range hdrs {
		if h.Mode != 0o600 || h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" ||
			!h.ModTime.Equal(time.Date(2026, 10, 13, 2, 0, 0, 0, time.UTC)) || h.Typeflag != tar.TypeReg {
			t.Errorf("%s: header %+v", h.Name, h)
		}
	}
	var mf Manifest
	if err := json.Unmarshal(bodies["manifest.json"], &mf); err != nil {
		t.Fatal(err)
	}
	for name, sum := range mf.Files {
		got := sha256.Sum256(bodies[name])
		if hex.EncodeToString(got[:]) != sum {
			t.Errorf("%s: hash differs from the manifest", name)
		}
	}
	if len(mf.Files) != len(names)-1 {
		t.Errorf("manifest lists %d files, the tar holds %d", len(mf.Files), len(names)-1)
	}
	golden := filepath.Join("testdata", "v2", "example.tar")
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, one, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (go test ./internal/backup -run Golden -update)", err)
	}
	if !bytes.Equal(g, one) {
		t.Errorf("the tar differs from %s: a format change must be deliberate (rerun with -update and review)", golden)
	}
}

func TestEscapeKeyRoundTrips(t *testing.T) {
	for key, want := range map[string]string{
		"github/reports/org/latest.json": "github/reports/org/latest.json",
		"a/../b":                         "a/%2E./b",
		".hidden/x":                      "%2Ehidden/x",
		"/abs//x/":                       "%/abs/%/x/%",
		"sp ace/%/ü":                     "sp%20ace/%25/%C3%BC",
		"..":                             "%2E.",
		".":                              "%2E",
		"a\\b":                           "a%5Cb",
	} {
		got := EscapeKey(key)
		if got != want {
			t.Errorf("EscapeKey(%q) = %q, want %q", key, got, want)
		}
		back, err := UnescapeKey(got)
		if err != nil || back != key {
			t.Errorf("UnescapeKey(%q) = %q, %v", got, back, err)
		}
		for _, seg := range strings.Split(got, "/") {
			if seg == "." || seg == ".." || seg == "" {
				t.Errorf("%q has the segment %q", got, seg)
			}
		}
	}
	for _, bad := range []string{"%2e", "%", "a%2", "a%G0", "a b", "%41"} {
		if bad == "%" {
			continue // the empty key's single segment
		}
		if _, err := UnescapeKey(bad); err == nil {
			t.Errorf("UnescapeKey(%q) took a non-canonical path", bad)
		}
	}
	for i := range 256 {
		k := string([]byte{byte(i), 'x', byte(255 - i)})
		if back, err := UnescapeKey(EscapeKey(k)); err != nil || back != k {
			t.Errorf("byte %d does not round-trip: %q %v", i, back, err)
		}
	}
}

// ---------------------------------------------------------------- tamper

func mutate(t *testing.T, store port.Blob, name string, f func([]byte) []byte) {
	t.Helper()
	o, err := store.Read(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), name, f(bytes.Clone(o.Body))); err != nil {
		t.Fatal(err)
	}
}

// resign makes the sidecar describe the object as it now is, as an attacker
// who can write both would.
func resign(t *testing.T, store port.Blob) {
	t.Helper()
	o, _ := store.Read(context.Background(), ObjectName("example", "b1"))
	sum := sha256.Sum256(o.Body)
	mutate(t, store, SidecarName("example", "b1"), func(b []byte) []byte {
		var sc Sidecar
		if err := json.Unmarshal(b, &sc); err != nil {
			t.Fatal(err)
		}
		sc.Object.Size, sc.Object.SHA256 = int64(len(o.Body)), hex.EncodeToString(sum[:])
		out, _ := json.Marshal(sc)
		return out
	})
}

func TestAFlippedByteIsRefused(t *testing.T) {
	for _, at := range []string{"header", "payload", "end"} {
		t.Run(at, func(t *testing.T) {
			store := memory.New().Blobs()
			id := identity(t)
			write(t, store, params(t, "b1", id))
			mutate(t, store, ObjectName("example", "b1"), func(b []byte) []byte {
				i := map[string]int{"header": 40, "payload": len(b) / 2, "end": len(b) - 1}[at]
				b[i] ^= 1
				return b
			})
			if _, err := open(t, store, idsOf(id)); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("sidecar check: %v, want ErrIntegrity", err)
			}
			resign(t, store)
			_, err := open(t, store, idsOf(id))
			if !errors.Is(err, ErrIntegrity) && (at != "header" || !errors.Is(err, ErrKey)) {
				t.Fatalf("decryption: %v, want ErrIntegrity", err)
			}
		})
	}
}

func TestATruncatedObjectIsRefused(t *testing.T) {
	store := memory.New().Blobs()
	id := identity(t)
	write(t, store, params(t, "b1", id))
	mutate(t, store, ObjectName("example", "b1"), func(b []byte) []byte { return b[:len(b)-100] })
	resign(t, store)
	if _, err := open(t, store, idsOf(id)); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("%v, want ErrIntegrity", err)
	}
}

func TestAMissingSidecarOrObjectIsNotABackup(t *testing.T) {
	store := memory.New().Blobs()
	id := identity(t)
	write(t, store, params(t, "b1", id))
	_ = store.Delete(context.Background(), ObjectName("example", "b1"))
	if _, err := open(t, store, idsOf(id)); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("no object: %v", err)
	}
	_ = store.Delete(context.Background(), SidecarName("example", "b1"))
	if _, err := open(t, store, idsOf(id)); !errors.Is(err, ErrFormat) {
		t.Fatalf("no sidecar: %v", err)
	}
}

type entry struct {
	name string
	body []byte
}

// craft writes an archive whose tar is exactly the entries, encrypted to id,
// with a sidecar that matches the object and the given modules.
func craft(t *testing.T, store port.Blob, id *age.X25519Identity, mods []SidecarModule, es []entry) {
	t.Helper()
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	for _, e := range es {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: e.name, Size: int64(len(e.body)), Mode: 0o600}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(e.body)
	}
	_ = tw.Close()
	var out bytes.Buffer
	aw, err := age.Encrypt(&out, id.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(aw)
	_, _ = gz.Write(tb.Bytes())
	_ = gz.Close()
	_ = aw.Close()
	sum := sha256.Sum256(out.Bytes())
	sc := Sidecar{Format: 2, Instance: "example", ID: "b1", Created: "2026-10-13T02:00:00Z", Layout: LayoutV5,
		Object:     ObjectInfo{Key: ObjectName("example", "b1"), Size: int64(out.Len()), SHA256: hex.EncodeToString(sum[:])},
		Recipients: []string{id.Recipient().String()}, Modules: mods}
	raw, _ := json.Marshal(sc)
	_, _ = store.Write(context.Background(), ObjectName("example", "b1"), out.Bytes())
	_, _ = store.Write(context.Background(), SidecarName("example", "b1"), raw)
}

func hashOf(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestAManifestOrMemberThatDisagreesIsRefused(t *testing.T) {
	state := []byte("{\"a\":1}\n{\"b\":2}\n")
	secrets := []byte("{\"s\":1}\n")
	blobBody := []byte("blob")
	good := func() (Manifest, []entry) {
		m := Manifest{Format: 2, Instance: "example", ID: "b1", Layout: LayoutV5, Created: "2026-10-13T02:00:00Z",
			Modules: []ModuleEntry{{Module: port.ModuleGitHub, State: Lines{2, hashOf(state)}, Secrets: Lines{1, hashOf(secrets)},
				Blobs: BlobsEntry{Count: 1, Bytes: 4}}},
			Files: map[string]string{"github/state.jsonl": hashOf(state), "github/secrets.jsonl": hashOf(secrets),
				"github/blobs/x": hashOf(blobBody)}}
		return m, []entry{{"github/state.jsonl", state}, {"github/secrets.jsonl", secrets}, {"github/blobs/x", blobBody}}
	}
	mods := []SidecarModule{{Module: port.ModuleGitHub, StateRecords: 2, SecretRecords: 1, Blobs: 1, BlobBytes: 4}}
	build := func(m Manifest, es []entry) []entry {
		raw, _ := json.Marshal(m)
		return append([]entry{{ManifestFile, raw}}, es...)
	}
	for name, c := range map[string]struct {
		edit func(*Manifest, []entry) []entry
		want error
	}{
		"as written":      {func(_ *Manifest, es []entry) []entry { return es }, nil},
		"member changed":  {func(_ *Manifest, es []entry) []entry { es[0].body = []byte("{\"a\":9}\n{\"b\":2}\n"); return es }, ErrIntegrity},
		"manifest hash":   {func(m *Manifest, es []entry) []entry { m.Files["github/blobs/x"] = hashOf([]byte("other")); return es }, ErrIntegrity},
		"member missing":  {func(_ *Manifest, es []entry) []entry { return es[:2] }, ErrIntegrity},
		"member extra":    {func(_ *Manifest, es []entry) []entry { return append(es, entry{"github/blobs/y", nil}) }, ErrIntegrity},
		"reordered":       {func(_ *Manifest, es []entry) []entry { es[0], es[1] = es[1], es[0]; return es }, ErrIntegrity},
		"count differs":   {func(m *Manifest, es []entry) []entry { m.Modules[0].State.Records = 3; return es }, ErrIntegrity},
		"other backup":    {func(m *Manifest, es []entry) []entry { m.ID = "b2"; return es }, ErrIntegrity},
		"format":          {func(m *Manifest, es []entry) []entry { m.Format = 1; return es }, ErrFormat},
		"unknown module":  {func(m *Manifest, es []entry) []entry { m.Modules[0].Module = "nope"; return es }, ErrFormat},
		"bad blob escape": {func(m *Manifest, es []entry) []entry { return renameBlob(m, es, "github/blobs/a b") }, ErrFormat},
	} {
		t.Run(name, func(t *testing.T) {
			store := memory.New().Blobs()
			id := identity(t)
			m, es := good()
			es = c.edit(&m, es)
			craft(t, store, id, mods, build(m, es))
			_, err := open(t, store, idsOf(id))
			if c.want == nil && err != nil || c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("%v, want %v", err, c.want)
			}
		})
	}
	t.Run("manifest not first", func(t *testing.T) {
		store := memory.New().Blobs()
		id := identity(t)
		m, es := good()
		all := build(m, es)
		all[0], all[1] = all[1], all[0]
		craft(t, store, id, mods, all)
		if _, err := open(t, store, idsOf(id)); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("sidecar counts differ", func(t *testing.T) {
		store := memory.New().Blobs()
		id := identity(t)
		m, es := good()
		other := slices.Clone(mods)
		other[0].Blobs = 7
		craft(t, store, id, other, build(m, es))
		if _, err := open(t, store, idsOf(id)); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("%v", err)
		}
	})
}

func renameBlob(m *Manifest, es []entry, to string) []entry {
	m.Files[to] = m.Files["github/blobs/x"]
	delete(m.Files, "github/blobs/x")
	es[2].name = to
	return es
}

// ------------------------------------------------------------ publishing

type orderStore struct {
	port.Blob
	writes []string
	fail   string
}

func (o *orderStore) Write(ctx context.Context, name string, body []byte) (string, error) {
	if o.fail != "" && strings.HasSuffix(name, o.fail) {
		return "", errors.New("refused")
	}
	o.writes = append(o.writes, name)
	return o.Blob.Write(ctx, name, body)
}

func TestTheSidecarIsWrittenLast(t *testing.T) {
	st := &orderStore{Blob: memory.New().Blobs()}
	write(t, st, params(t, "b1", identity(t)))
	if want := []string{"backup/example/b1.tar.gz.age", "backup/example/b1.json"}; !slices.Equal(st.writes, want) {
		t.Errorf("writes %v, want %v", st.writes, want)
	}
	failing := &orderStore{Blob: memory.New().Blobs(), fail: ObjectSuffix}
	a := write(t, nil, params(t, "b1", identity(t)))
	if err := Publish(context.Background(), failing, a); err == nil {
		t.Fatal("a failed upload published")
	}
	if names, _ := failing.List(context.Background(), ""); len(names) != 0 {
		t.Errorf("a failed upload left %v", names)
	}
}

// --------------------------------------------------------------- offline

// The archive opens with the age and tar commands alone, as the
// documentation promises: age -d -i key.txt <id>.tar.gz.age | tar xz.
func TestTheArchiveOpensWithoutSluis(t *testing.T) {
	if _, err := exec.LookPath("age"); err != nil {
		t.Skip("the age command is not installed")
	}
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("the tar command is not installed")
	}
	id := identity(t)
	a := write(t, nil, params(t, "b1", id))
	dir := t.TempDir()
	key := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(key, []byte("# test\n"+id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", `age -d -i "$1" "$2" | tar xz -C "$3"`, "sh", key, a.Path, out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	_, _, bodies := members(t, tarOf(t, a.Path, id))
	for name, body := range bodies {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(got, body) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
