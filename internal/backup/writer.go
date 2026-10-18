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
	"os"
	"slices"
	"time"

	"filippo.io/age"

	"github.com/truvity/sluis/internal/port"
)

// maxRecord bounds one JSONL record, so a corrupt archive cannot ask for a
// huge allocation.
const maxRecord = 64 << 20

// Params describe the backup a [Writer] makes.
type Params struct {
	Instance string
	ID       string
	Layout   Layout
	// Creator names what writes it, for example "sluis-backup 1.76.1".
	Creator string
	// Recipients are the age X25519 public keys (age1...) the backup is
	// encrypted to; at least one.
	Recipients []string
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// TempDir holds the spool and the encrypted file while they are written;
	// os.TempDir() (/tmp on Lambda) when empty.
	TempDir string
}

func (p *Params) check() ([]age.Recipient, error) {
	if !ValidName(p.Instance) || !ValidName(p.ID) {
		return nil, errors.New("backup: instance and id must be one path segment of letters, digits, '.', '-' and '_'")
	}
	if !p.Layout.Valid() {
		return nil, fmt.Errorf("backup: layout %q is unknown", p.Layout)
	}
	return ParseRecipients(p.Recipients)
}

// Writer writes one backup into a local encrypted file ([Archive]); [Publish]
// uploads it. Records are spooled to disk as they come, so memory is one
// record or one blob. It is not safe for concurrent use.
type Writer struct {
	p       Params
	recips  []age.Recipient
	created time.Time
	sp      *spool
	mods    map[port.Module]*modFiles
	cur     string
	curN    *int64
	written map[string]span
	closed  bool
}

type modFiles struct {
	state, secrets       int64
	blobs                []string
	blobCount, blobBytes int64
}

// NewWriter starts a backup. Nothing is written to a store.
func NewWriter(p Params) (*Writer, error) {
	recips, err := p.check()
	if err != nil {
		return nil, err
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	sp, err := newSpool(p.TempDir)
	if err != nil {
		return nil, fmt.Errorf("backup: spool: %w", err)
	}
	return &Writer{p: p, recips: recips, created: now().UTC().Truncate(time.Second), sp: sp,
		mods: map[port.Module]*modFiles{}, written: map[string]span{}}, nil
}

// Include puts a module in the backup even when it adds no record: its record
// files are written, empty.
func (w *Writer) Include(m port.Module) error {
	if !m.Valid() {
		return fmt.Errorf("backup: module %q", m)
	}
	if w.mods[m] == nil {
		w.mods[m] = &modFiles{}
	}
	return nil
}

func (w *Writer) finishCur() error {
	if w.cur == "" {
		return nil
	}
	sp, err := w.sp.finish()
	if err != nil {
		return err
	}
	w.written[w.cur] = sp
	w.cur, w.curN = "", nil
	return nil
}

// Add appends one record (one line of JSON) to the state or the secrets file
// of a module. The records of one file must come together: a file once left
// is closed.
func (w *Writer) Add(_ context.Context, s Section, m port.Module, record []byte) error {
	if w.closed {
		return errors.New("backup: writer is closed")
	}
	if (s != State && s != Secrets) || !m.Valid() {
		return fmt.Errorf("backup: section %q module %q takes no records", s, m)
	}
	if len(record) > maxRecord {
		return fmt.Errorf("backup: a record of %d bytes is over the limit of %d", len(record), maxRecord)
	}
	if len(record) == 0 || bytes.IndexByte(record, '\n') >= 0 {
		return errors.New("backup: a record is one non-empty line")
	}
	_ = w.Include(m)
	name := StateFile(m)
	n := &w.mods[m].state
	if s == Secrets {
		name, n = SecretsFile(m), &w.mods[m].secrets
	}
	if w.cur != name {
		if err := w.finishCur(); err != nil {
			return err
		}
		if _, done := w.written[name]; done {
			return fmt.Errorf("backup: the records of %s came apart", name)
		}
		w.sp.begin()
		w.cur, w.curN = name, n
	}
	if _, err := w.sp.Write(record); err != nil {
		return err
	}
	if _, err := w.sp.Write([]byte{'\n'}); err != nil {
		return err
	}
	*w.curN++
	return nil
}

// AddBlob adds one blob of a module as a file of its own.
func (w *Writer) AddBlob(_ context.Context, m port.Module, key string, body []byte) error {
	if w.closed {
		return errors.New("backup: writer is closed")
	}
	if !m.Valid() || key == "" {
		return fmt.Errorf("backup: blob %q of module %q", key, m)
	}
	if err := w.finishCur(); err != nil {
		return err
	}
	_ = w.Include(m)
	name := BlobFile(m, key)
	if _, dup := w.written[name]; dup {
		return fmt.Errorf("backup: blob %q is added twice", key)
	}
	w.sp.begin()
	if _, err := w.sp.Write(body); err != nil {
		return err
	}
	w.cur = name
	if err := w.finishCur(); err != nil {
		return err
	}
	mf := w.mods[m]
	mf.blobs = append(mf.blobs, name)
	mf.blobCount++
	mf.blobBytes += int64(len(body))
	return nil
}

// Archive is a finished backup on local disk, not yet uploaded.
type Archive struct {
	Manifest *Manifest
	// Sidecar describes the encrypted file; [Publish] writes it after it.
	Sidecar *Sidecar
	// Path is the encrypted file.
	Path string
	// SidecarSHA256 is the SHA-256 (hex) of the sidecar exactly as [Publish]
	// wrote it; empty until it has.
	SidecarSHA256 string
}

// Remove deletes the local file.
func (a *Archive) Remove() {
	if a != nil && a.Path != "" {
		_ = os.Remove(a.Path)
	}
}

// member is one file of the tar in write order.
type member struct {
	name string
	sp   span
}

// Close finishes the backup: it writes manifest.json and every member into a
// tar, compresses it with gzip and encrypts it with age into a file, and
// returns that file with its manifest and sidecar. The spool is removed.
func (w *Writer) Close(_ context.Context) (*Archive, error) {
	if w.closed {
		return nil, errors.New("backup: writer is closed")
	}
	defer w.Abort()
	if err := w.finishCur(); err != nil {
		return nil, err
	}
	created := w.created.Format(time.RFC3339)
	m := &Manifest{Format: Version, Instance: w.p.Instance, ID: w.p.ID, Layout: w.p.Layout, Created: created,
		Creator: w.p.Creator, Files: map[string]string{}}
	sc := &Sidecar{Format: Version, Instance: w.p.Instance, ID: w.p.ID, Created: created, Creator: w.p.Creator,
		Layout: w.p.Layout, Modules: []SidecarModule{}}
	for _, r := range w.recips {
		sc.Recipients = append(sc.Recipients, r.(*age.X25519Recipient).String())
	}
	var members []member
	for _, mod := range port.Modules() {
		mf := w.mods[mod]
		if mf == nil {
			continue
		}
		// A module's record files are written even when empty.
		for _, name := range []string{StateFile(mod), SecretsFile(mod)} {
			if _, ok := w.written[name]; !ok {
				w.sp.begin()
				w.cur = name
				if err := w.finishCur(); err != nil {
					return nil, err
				}
			}
		}
		st, se := w.written[StateFile(mod)], w.written[SecretsFile(mod)]
		e := ModuleEntry{Module: mod,
			State:   Lines{Records: mf.state, SHA256: hex.EncodeToString(st.sum[:])},
			Secrets: Lines{Records: mf.secrets, SHA256: hex.EncodeToString(se.sum[:])},
			Blobs:   BlobsEntry{Count: mf.blobCount, Bytes: mf.blobBytes}}
		m.Modules = append(m.Modules, e)
		sc.Modules = append(sc.Modules, SidecarModule{Module: mod, StateRecords: mf.state, SecretRecords: mf.secrets,
			Blobs: mf.blobCount, BlobBytes: mf.blobBytes})
		members = append(members, member{StateFile(mod), st}, member{SecretsFile(mod), se})
		blobs := slices.Clone(mf.blobs)
		slices.Sort(blobs)
		for _, b := range blobs {
			members = append(members, member{b, w.written[b]})
		}
	}
	for _, mb := range members {
		m.Files[mb.name] = hex.EncodeToString(mb.sp.sum[:])
	}
	if m.Modules == nil {
		m.Modules = []ModuleEntry{}
	}
	mjson, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	mjson = append(mjson, '\n')

	out, err := os.CreateTemp(w.p.TempDir, "sluis-backup-*"+ObjectSuffix)
	if err != nil {
		return nil, fmt.Errorf("backup: %w", err)
	}
	a := &Archive{Manifest: m, Sidecar: sc, Path: out.Name()}
	size, sum, err := w.encrypt(out, mjson, members)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		a.Remove()
		return nil, err
	}
	sc.Object = ObjectInfo{Key: ObjectName(w.p.Instance, w.p.ID), Size: size, SHA256: sum}
	return a, nil
}

type counter struct {
	w io.Writer
	n int64
}

func (c *counter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// encrypt writes tar -> gzip -> age into out and returns the size and SHA-256
// of what it wrote.
func (w *Writer) encrypt(out *os.File, mjson []byte, members []member) (int64, string, error) {
	bw := bufio.NewWriterSize(out, 256<<10)
	h := sha256.New()
	cnt := &counter{w: io.MultiWriter(bw, h)}
	aw, err := age.Encrypt(cnt, w.recips...)
	if err != nil {
		return 0, "", fmt.Errorf("backup: age: %w", err)
	}
	gz := gzip.NewWriter(aw)
	tw := tar.NewWriter(gz)
	hdr := func(name string, size int64) *tar.Header {
		return &tar.Header{Typeflag: tar.TypeReg, Name: name, Size: size, Mode: 0o600, ModTime: w.created, Format: tar.FormatPAX}
	}
	if err := tw.WriteHeader(hdr(ManifestFile, int64(len(mjson)))); err != nil {
		return 0, "", err
	}
	if _, err := tw.Write(mjson); err != nil {
		return 0, "", err
	}
	for _, mb := range members {
		r, err := w.sp.reader(mb.sp)
		if err != nil {
			return 0, "", err
		}
		if err := tw.WriteHeader(hdr(mb.name, mb.sp.size)); err != nil {
			return 0, "", err
		}
		mh := sha256.New()
		if _, err := io.CopyN(tw, io.TeeReader(r, mh), mb.sp.size); err != nil {
			return 0, "", fmt.Errorf("backup: %s: %w", mb.name, err)
		}
		if !bytes.Equal(mh.Sum(nil), mb.sp.sum[:]) {
			return 0, "", fmt.Errorf("backup: the spool of %s changed under the writer", mb.name)
		}
	}
	for _, c := range []io.Closer{tw, gz, aw} {
		if err := c.Close(); err != nil {
			return 0, "", err
		}
	}
	if err := bw.Flush(); err != nil {
		return 0, "", err
	}
	if err := out.Sync(); err != nil {
		return 0, "", err
	}
	return cnt.n, hex.EncodeToString(h.Sum(nil)), nil
}

// Abort removes the spool without writing a backup.
func (w *Writer) Abort() {
	w.closed = true
	w.sp.close()
}

// Publish uploads an archive: the encrypted object with one write, then the
// sidecar. A backup is complete only once Publish returned; an object
// without its sidecar is not a backup and a prune removes it.
func Publish(ctx context.Context, store port.Blob, a *Archive) error {
	body, err := os.ReadFile(a.Path)
	if err != nil {
		return fmt.Errorf("backup: read the archive: %w", err)
	}
	sum := sha256.Sum256(body)
	if int64(len(body)) != a.Sidecar.Object.Size || hex.EncodeToString(sum[:]) != a.Sidecar.Object.SHA256 {
		return fmt.Errorf("%w: the local archive changed before its upload", ErrIntegrity)
	}
	if _, err := store.Write(ctx, a.Sidecar.Object.Key, body); err != nil {
		return fmt.Errorf("backup: write %s: %w", a.Sidecar.Object.Key, err)
	}
	sc, err := json.MarshalIndent(a.Sidecar, "", "  ")
	if err != nil {
		return err
	}
	name := SidecarName(a.Sidecar.Instance, a.Sidecar.ID)
	sc = append(sc, '\n')
	if _, err := store.Write(ctx, name, sc); err != nil {
		return fmt.Errorf("backup: write %s: %w", name, err)
	}
	ssum := sha256.Sum256(sc)
	a.SidecarSHA256 = hex.EncodeToString(ssum[:])
	return nil
}
