package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"filippo.io/age"

	"github.com/truvity/sluis/internal/port"
)

// maxManifest bounds manifest.json.
const maxManifest = 64 << 20

// ReadSidecar reads a backup's sidecar. Nothing authenticates it: it is for
// listing, and for checking the object against before it is decrypted.
func ReadSidecar(ctx context.Context, store port.Blob, instance, id string) (*Sidecar, error) {
	sc, _, err := readSidecar(ctx, store, instance, id)
	return sc, err
}

// readSidecar reads the sidecar and the SHA-256 (hex) of its bytes.
func readSidecar(ctx context.Context, store port.Blob, instance, id string) (*Sidecar, string, error) {
	if !ValidName(instance) || !ValidName(id) {
		return nil, "", fmt.Errorf("%w: instance or id is not a valid name", ErrFormat)
	}
	o, err := store.Read(ctx, SidecarName(instance, id))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			return nil, "", fmt.Errorf("%w: backup %s/%s has no sidecar (%s): it is not a complete backup", ErrFormat, instance, id, SidecarName(instance, id))
		}
		return nil, "", fmt.Errorf("backup: read the sidecar: %w", err)
	}
	var sc Sidecar
	if err := json.Unmarshal(o.Body, &sc); err != nil {
		return nil, "", fmt.Errorf("%w: %s is not a sidecar", ErrFormat, SidecarName(instance, id))
	}
	if err := sc.validate(instance, id); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(o.Body)
	return &sc, hex.EncodeToString(sum[:]), nil
}

// Fetched is a backup read from the archive: the sidecar, the SHA-256 of the
// sidecar's bytes, and the encrypted object, whose size and SHA-256 matched
// the sidecar's. None of it is authenticated: whoever may write the archive
// bucket wrote it. A [Verifier] is what says it is the backup that was made.
type Fetched struct {
	Sidecar       *Sidecar
	SidecarSHA256 string
	Body          []byte
}

// Fetch reads a backup's sidecar and its encrypted object and checks the
// object's size and SHA-256 against the sidecar. It needs no identity.
func Fetch(ctx context.Context, store port.Blob, instance, id string) (*Sidecar, []byte, error) {
	f, err := FetchAll(ctx, store, instance, id)
	if err != nil {
		return nil, nil, err
	}
	return f.Sidecar, f.Body, nil
}

// FetchAll is [Fetch] that also reports the hash of the sidecar's bytes.
func FetchAll(ctx context.Context, store port.Blob, instance, id string) (*Fetched, error) {
	sc, ssum, err := readSidecar(ctx, store, instance, id)
	if err != nil {
		return nil, err
	}
	o, err := store.Read(ctx, sc.Object.Key)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			return nil, fmt.Errorf("%w: the object %s is missing", ErrIntegrity, sc.Object.Key)
		}
		return nil, fmt.Errorf("backup: read %s: %w", sc.Object.Key, err)
	}
	sum := sha256.Sum256(o.Body)
	if int64(len(o.Body)) != sc.Object.Size || hex.EncodeToString(sum[:]) != sc.Object.SHA256 {
		return nil, fmt.Errorf("%w: the object %s does not match its sidecar's size and SHA-256", ErrIntegrity, sc.Object.Key)
	}
	return &Fetched{Sidecar: sc, SidecarSHA256: ssum, Body: o.Body}, nil
}

// Identities returns the age identities a reader decrypts with; it is called
// once the object has been checked, so a backup that is not there never reads
// the secret.
type Identities func(ctx context.Context) ([]age.Identity, error)

// Reader is one verified backup, decrypted into a local spool.
type Reader struct {
	sc    *Sidecar
	m     *Manifest
	sp    *spool
	files map[string]span
}

// Open fetches a backup, checks the object against its sidecar, decrypts it
// with the identities, and verifies every member of the tar against the
// manifest (its hash, its place in the order, the counts) before it returns.
// A key that does not open it fails with [ErrKey]; anything changed, with
// [ErrIntegrity]. tempDir holds the spool ("" is os.TempDir()).
//
// Open trusts the archive bucket: it does not ask whether the backup is the one
// the backup function wrote. A restore uses [OpenVerified].
func Open(ctx context.Context, store port.Blob, ids Identities, instance, id, tempDir string) (*Reader, error) {
	sc, body, err := Fetch(ctx, store, instance, id)
	if err != nil {
		return nil, err
	}
	if ids == nil {
		return nil, fmt.Errorf("%w: no identity is configured", ErrKey)
	}
	idents, err := ids(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKey, err)
	}
	return Decrypt(ctx, sc, bytes.NewReader(body), idents, tempDir)
}

// OpenVerified is [Open] for a restore: the backup is fetched, and v decides
// whether it is genuine before the identity is read or a byte is decrypted
// (see [Verifier]). A backup v refuses is [ErrUnverified] (or [ErrRecipients]),
// and nothing is read of it but its sidecar and its hash. The verification it
// passed is returned with the reader.
func OpenVerified(ctx context.Context, store port.Blob, ids Identities, v Verifier, instance, id, tempDir string) (*Reader, Verification, error) {
	f, err := FetchAll(ctx, store, instance, id)
	if err != nil {
		return nil, Verification{}, err
	}
	ver, err := v.Check(ctx, f)
	if err != nil {
		return nil, ver, err
	}
	if ids == nil {
		return nil, ver, fmt.Errorf("%w: no identity is configured", ErrKey)
	}
	idents, err := ids(ctx)
	if err != nil {
		return nil, ver, fmt.Errorf("%w: %v", ErrKey, err)
	}
	r, err := Decrypt(ctx, f.Sidecar, bytes.NewReader(f.Body), idents, tempDir)
	return r, ver, err
}

// Decrypt reads the encrypted object of the sidecar from r: see [Open].
func Decrypt(ctx context.Context, sc *Sidecar, r io.Reader, idents []age.Identity, tempDir string) (*Reader, error) {
	if len(idents) == 0 {
		return nil, fmt.Errorf("%w: no identity", ErrKey)
	}
	ar, err := age.Decrypt(r, idents...)
	if err != nil {
		var miss *age.NoIdentityMatchError
		if errors.As(err, &miss) {
			return nil, fmt.Errorf("%w: it is not encrypted to this identity", ErrKey)
		}
		return nil, fmt.Errorf("%w: age: %v", ErrIntegrity, err)
	}
	sp, err := newSpool(tempDir)
	if err != nil {
		return nil, fmt.Errorf("backup: spool: %w", err)
	}
	rd := &Reader{sc: sc, sp: sp, files: map[string]span{}}
	if err := rd.load(ctx, ar); err != nil {
		rd.Close()
		return nil, err
	}
	return rd, nil
}

func integrity(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrIntegrity, fmt.Sprintf(format, a...))
}

// load reads the tar into the spool and verifies it.
func (rd *Reader) load(ctx context.Context, ar io.Reader) error {
	gz, err := gzip.NewReader(ar)
	if err != nil {
		return integrity("the content is not gzip: %v", err)
	}
	gz.Multistream(false)
	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if err != nil || hdr.Name != ManifestFile || hdr.Typeflag != tar.TypeReg || hdr.Size > maxManifest {
		return integrity("the tar does not begin with %s", ManifestFile)
	}
	raw, err := io.ReadAll(tr)
	if err != nil {
		return integrity("read %s: %v", ManifestFile, err)
	}
	var m Manifest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrFormat, ManifestFile, err)
	}
	if err := m.validate(rd.sc); err != nil {
		return err
	}
	rd.m = &m
	want := m.order()
	for i := 0; ; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			if i != len(want) {
				return integrity("the tar holds %d of the %d files its manifest lists", i, len(want))
			}
			break
		}
		if err != nil {
			return integrity("read the tar: %v", err)
		}
		if i >= len(want) || hdr.Name != want[i] {
			return integrity("member %q is not the one the manifest lists next", hdr.Name)
		}
		if hdr.Typeflag != tar.TypeReg || hdr.Size < 0 {
			return integrity("member %q is not a regular file", hdr.Name)
		}
		rd.sp.begin()
		lines, err := countLines(io.TeeReader(io.LimitReader(tr, hdr.Size), rd.sp))
		if err != nil {
			return integrity("read %s: %v", hdr.Name, err)
		}
		sp, err := rd.sp.finish()
		if err != nil {
			return err
		}
		if sp.size != hdr.Size || hex.EncodeToString(sp.sum[:]) != m.Files[hdr.Name] {
			return integrity("member %s does not match its hash in the manifest", hdr.Name)
		}
		if err := m.checkCount(hdr.Name, lines); err != nil {
			return err
		}
		rd.files[hdr.Name] = sp
	}
	if err := m.checkBlobs(rd.files); err != nil {
		return err
	}
	// Read to the end so gzip checks its CRC and age its last chunk, and
	// refuse anything after the gzip stream.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return integrity("the compressed stream: %v", err)
	}
	if n, err := io.Copy(io.Discard, ar); err != nil || n != 0 {
		return integrity("the encrypted stream does not end with the archive")
	}
	return nil
}

// countLines copies r to nowhere (the tee keeps it) and counts its lines.
func countLines(r io.Reader) (int64, error) {
	buf := make([]byte, 64<<10)
	var n int64
	for {
		k, err := r.Read(buf)
		n += int64(bytes.Count(buf[:k], []byte{'\n'}))
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}

// order is every member after the manifest, in the order the tar must hold
// them: per module its record files, then its blobs sorted by name.
func (m *Manifest) order() []string {
	var out []string
	for _, e := range m.Modules {
		out = append(out, StateFile(e.Module), SecretsFile(e.Module))
		var blobs []string
		prefix := string(e.Module) + "/blobs/"
		for name := range m.Files {
			if strings.HasPrefix(name, prefix) {
				blobs = append(blobs, name)
			}
		}
		slices.Sort(blobs)
		out = append(out, blobs...)
	}
	return out
}

func (m *Manifest) checkCount(name string, lines int64) error {
	for _, e := range m.Modules {
		switch name {
		case StateFile(e.Module):
			if lines != e.State.Records {
				return integrity("%s holds %d records, the manifest says %d", name, lines, e.State.Records)
			}
			return nil
		case SecretsFile(e.Module):
			if lines != e.Secrets.Records {
				return integrity("%s holds %d records, the manifest says %d", name, lines, e.Secrets.Records)
			}
			return nil
		}
	}
	return nil
}

func (m *Manifest) checkBlobs(files map[string]span) error {
	listed := 2 * len(m.Modules)
	for _, e := range m.Modules {
		prefix := string(e.Module) + "/blobs/"
		var n, bytes int64
		for name, sp := range files {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			if _, err := UnescapeKey(strings.TrimPrefix(name, prefix)); err != nil {
				return err
			}
			n++
			bytes += sp.size
		}
		if n != e.Blobs.Count || bytes != e.Blobs.Bytes {
			return integrity("module %s holds %d blobs of %d bytes, the manifest says %d of %d", e.Module, n, bytes, e.Blobs.Count, e.Blobs.Bytes)
		}
		listed += int(n)
	}
	if listed != len(m.Files) {
		return integrity("the manifest lists files of no module")
	}
	return nil
}

// Manifest is the verified manifest.
func (rd *Reader) Manifest() *Manifest { return rd.m }

// Sidecar is the backup's sidecar, checked against the manifest.
func (rd *Reader) Sidecar() *Sidecar { return rd.sc }

// Close removes the spool.
func (rd *Reader) Close() { rd.sp.close() }

// Records calls fn with each record of the state or the secrets file of a
// module, in order. The slice passed to fn is valid only during the call. A
// module the backup does not hold has no records.
func (rd *Reader) Records(ctx context.Context, s Section, m port.Module, fn func(record []byte) error) error {
	if (s != State && s != Secrets) || !m.Valid() {
		return fmt.Errorf("backup: section %q module %q holds no records", s, m)
	}
	name := StateFile(m)
	if s == Secrets {
		name = SecretsFile(m)
	}
	sp, ok := rd.files[name]
	if !ok {
		return nil
	}
	r, err := rd.sp.reader(sp)
	if err != nil {
		return err
	}
	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk, isPrefix, err := br.ReadLine()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		line = append(line[:0], chunk...)
		for isPrefix {
			if len(line) > maxRecord {
				return integrity("%s holds a record over %d bytes", name, maxRecord)
			}
			chunk, isPrefix, err = br.ReadLine()
			if err != nil {
				return integrity("%s: %v", name, err)
			}
			line = append(line, chunk...)
		}
		if err := fn(line); err != nil {
			return err
		}
	}
}

// Blobs calls fn with each blob of a module, sorted by member name; the body is
// valid only during the call.
func (rd *Reader) Blobs(ctx context.Context, m port.Module, fn func(key string, body []byte) error) error {
	prefix := string(m) + "/blobs/"
	var names []string
	for name := range rd.files {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		key, err := UnescapeKey(strings.TrimPrefix(name, prefix))
		if err != nil {
			return err
		}
		sp := rd.files[name]
		r, err := rd.sp.reader(sp)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		if sum := sha256.Sum256(body); sum != sp.sum {
			return integrity("blob %s changed in the spool", name)
		}
		if err := fn(key, body); err != nil {
			return err
		}
	}
	return nil
}
