package backup

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/truvity/sluis/internal/port"
)

// ChunkBytes is the plaintext size a chunk is cut at: a chunk is flushed
// when the next record would take it past this, so a chunk is smaller unless
// one record is larger.
const ChunkBytes = 8 << 20

// Params describe the backup a [Writer] makes.
type Params struct {
	Installation string
	ID           string
	Layout       Layout
	// Creator names what writes it, for example "sluis-backup 1.75.0".
	Creator string
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// ChunkBytes overrides [ChunkBytes], for tests.
	ChunkBytes int
}

// Progress is what a run must keep to continue after a stop: the wrapped data
// key and the chunks written so far. It holds no secret (the data key is
// wrapped) and no record.
type Progress struct {
	Wrapped []byte        `json:"wrapped"`
	Created string        `json:"created"`
	Modules []ModuleEntry `json:"modules"`
}

// Writer writes one backup. It holds at most one chunk of records in memory.
// It is not safe for concurrent use.
type Writer struct {
	store   port.Blob
	p       Params
	dk      []byte
	wrapped []byte
	created string
	mods    map[port.Module]*ModuleEntry

	cur    *ModuleEntry
	curSec Section
	buf    []byte
	nrec   int
	closed bool
}

func (p *Params) check() error {
	if !ValidName(p.Installation) || !ValidName(p.ID) {
		return errors.New("backup: installation and id must be one path segment of letters, digits, '.', '-' and '_'")
	}
	if !p.Layout.Valid() {
		return fmt.Errorf("backup: layout %q is unknown", p.Layout)
	}
	if p.ChunkBytes < 0 {
		return errors.New("backup: ChunkBytes is negative")
	}
	return nil
}

func (p *Params) chunkBytes() int {
	if p.ChunkBytes > 0 {
		return p.ChunkBytes
	}
	return ChunkBytes
}

func (p *Params) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

// NewWriter starts a backup: it asks the key for a data key. Nothing is
// written to the store until the first chunk is flushed.
func NewWriter(ctx context.Context, store port.Blob, key Key, p Params) (*Writer, error) {
	if err := p.check(); err != nil {
		return nil, err
	}
	dk, err := key.GenerateDataKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("backup: generate data key: %w", err)
	}
	if len(dk.Plaintext) != dataKeyLen {
		zero(dk.Plaintext)
		return nil, fmt.Errorf("backup: data key is %d bytes, need %d", len(dk.Plaintext), dataKeyLen)
	}
	return &Writer{store: store, p: p, dk: dk.Plaintext, wrapped: dk.Wrapped,
		created: p.now().Format(time.RFC3339), mods: map[port.Module]*ModuleEntry{}}, nil
}

// Resume continues a backup from the [Progress] a stopped [Writer] returned
// from Flush: it unwraps the same data key and takes the chunks as written.
// The caller must not add records twice; it resumes from the first record
// not in a chunk listed by the progress.
func Resume(ctx context.Context, store port.Blob, key Key, p Params, prog Progress) (*Writer, error) {
	if err := p.check(); err != nil {
		return nil, err
	}
	dk, err := key.UnwrapDataKey(ctx, prog.Wrapped)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKey, err)
	}
	w := &Writer{store: store, p: p, dk: dk, wrapped: prog.Wrapped, created: prog.Created, mods: map[port.Module]*ModuleEntry{}}
	for _, e := range prog.Modules {
		if !e.Module.Valid() {
			return nil, fmt.Errorf("%w: progress names module %q", ErrFormat, e.Module)
		}
		e := e
		w.mods[e.Module] = &e
	}
	return w, nil
}

// Add appends one record to a section of a module. A change of section or
// module flushes the chunk in progress first.
func (w *Writer) Add(ctx context.Context, s Section, m port.Module, record []byte) error {
	if w.closed {
		return errors.New("backup: writer is closed")
	}
	if !s.Valid() || !m.Valid() {
		return fmt.Errorf("backup: section %q module %q", s, m)
	}
	if len(record) > maxRecord {
		return fmt.Errorf("backup: a record of %d bytes is over the limit of %d", len(record), maxRecord)
	}
	if w.cur == nil || w.cur.Module != m || w.curSec != s {
		if err := w.flushChunk(ctx); err != nil {
			return err
		}
		e := w.mods[m]
		if e == nil {
			e = &ModuleEntry{Module: m}
			w.mods[m] = e
		}
		w.cur, w.curSec = e, s
	}
	var l [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(l[:], uint64(len(record)))
	if w.nrec > 0 && len(w.buf)+n+len(record) > w.p.chunkBytes() {
		if err := w.flushChunk(ctx); err != nil {
			return err
		}
	}
	w.buf = append(w.buf, l[:n]...)
	w.buf = append(w.buf, record...)
	w.nrec++
	return nil
}

// flushChunk seals and writes the chunk in progress, if any.
func (w *Writer) flushChunk(ctx context.Context) error {
	if w.nrec == 0 {
		return nil
	}
	part := w.cur.part(w.curSec)
	n := len(part.Chunks)
	name := ChunkName(w.p.Installation, w.p.ID, w.curSec, w.cur.Module, n)
	sealed, err := sealChunk(w.dk, Version, name, w.buf)
	if err != nil {
		return err
	}
	if _, err := w.store.Write(ctx, name, sealed); err != nil {
		return fmt.Errorf("backup: write %s: %w", name, err)
	}
	sum := sha256.Sum256(sealed)
	part.Chunks = append(part.Chunks, Chunk{N: n, Records: w.nrec, Size: int64(len(sealed)), SHA256: hex.EncodeToString(sum[:])})
	part.Records += int64(w.nrec)
	zero(w.buf)
	w.buf, w.nrec = w.buf[:0], 0
	return nil
}

func (w *Writer) entries() []ModuleEntry {
	out := make([]ModuleEntry, 0, len(w.mods))
	for _, m := range port.Modules() {
		if e, ok := w.mods[m]; ok {
			out = append(out, *e)
		}
	}
	return out
}

// Flush writes the chunk in progress and returns the progress to resume from.
func (w *Writer) Flush(ctx context.Context) (Progress, error) {
	if err := w.flushChunk(ctx); err != nil {
		return Progress{}, err
	}
	return Progress{Wrapped: w.wrapped, Created: w.created, Modules: w.entries()}, nil
}

// Close writes the last chunk and then the manifest, and returns it. The data
// key is wiped. A backup is complete only once Close has returned.
func (w *Writer) Close(ctx context.Context) (*Manifest, error) {
	if w.closed {
		return nil, errors.New("backup: writer is closed")
	}
	if err := w.flushChunk(ctx); err != nil {
		return nil, err
	}
	m := &Manifest{Format: Version, Installation: w.p.Installation, ID: w.p.ID, Layout: w.p.Layout,
		Created: w.created, Creator: w.p.Creator, ChunkBytes: w.p.chunkBytes(), Modules: w.entries()}
	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	mac, err := manifestMAC(w.dk, Version, w.wrapped, body)
	if err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(envelope{Format: Version, Key: w.wrapped, Manifest: body, MAC: mac}, "", "  ")
	if err != nil {
		return nil, err
	}
	if _, err := w.store.Write(ctx, ManifestName(w.p.Installation, w.p.ID), out); err != nil {
		return nil, fmt.Errorf("backup: write manifest: %w", err)
	}
	w.closed = true
	zero(w.dk)
	return m, nil
}

// Abort wipes the data key without writing a manifest. The chunks already
// written stay; without a manifest they are not a backup.
func (w *Writer) Abort() {
	w.closed = true
	zero(w.dk)
	zero(w.buf)
}
