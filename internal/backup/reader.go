package backup

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/storage/keys"
)

func readEnvelope(ctx context.Context, store port.Blob, installation, id string) (*envelope, error) {
	if !ValidName(installation) || !ValidName(id) {
		return nil, fmt.Errorf("%w: installation or id is not a valid name", ErrFormat)
	}
	o, err := store.Read(ctx, ManifestName(installation, id))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			return nil, fmt.Errorf("%w: backup %s/%s has no manifest", ErrFormat, installation, id)
		}
		return nil, fmt.Errorf("backup: read manifest: %w", err)
	}
	var env envelope
	if err := json.Unmarshal(o.Body, &env); err != nil || len(env.Manifest) == 0 {
		return nil, fmt.Errorf("%w: manifest.json is not a manifest", ErrFormat)
	}
	if !Readable(env.Format) {
		return nil, fmt.Errorf("%w: format %d is not readable by this release (reads %d)", ErrFormat, env.Format, Version)
	}
	return &env, nil
}

// Peek reads a manifest without verifying it: nothing authenticates the
// result, so it is for listing and for showing a person what is there, never
// for deciding what to restore. [Open] verifies.
func Peek(ctx context.Context, store port.Blob, installation, id string) (*Manifest, error) {
	env, err := readEnvelope(ctx, store, installation, id)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(env.Manifest, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFormat, err)
	}
	if err := m.validate(installation, id); err != nil {
		return nil, err
	}
	return &m, nil
}

// Reader reads one verified backup.
type Reader struct {
	store port.Blob
	m     *Manifest
	dk    []byte
}

// Open reads a backup's manifest, recovers the data key with the key (an
// option may name the context the key was written under, see
// keys.WithContext) and verifies the manifest's MAC. No chunk is read; a
// key that may not decrypt fails here with [ErrKey].
func Open(ctx context.Context, store port.Blob, key Key, installation, id string, opts ...keys.DecryptOption) (*Reader, error) {
	env, err := readEnvelope(ctx, store, installation, id)
	if err != nil {
		return nil, err
	}
	dk, err := key.UnwrapDataKey(ctx, env.Key, opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKey, err)
	}
	body, err := compact(env.Manifest)
	if err != nil {
		zero(dk)
		return nil, err
	}
	want, err := manifestMAC(dk, env.Format, env.Key, body)
	if err != nil || !hmac.Equal(want, env.MAC) {
		zero(dk)
		return nil, fmt.Errorf("%w: manifest MAC does not match", ErrIntegrity)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		zero(dk)
		return nil, fmt.Errorf("%w: %v", ErrFormat, err)
	}
	if m.Format != env.Format {
		zero(dk)
		return nil, fmt.Errorf("%w: manifest format differs from its envelope", ErrIntegrity)
	}
	if err := m.validate(installation, id); err != nil {
		zero(dk)
		return nil, err
	}
	return &Reader{store: store, m: &m, dk: dk}, nil
}

// Manifest is the verified manifest.
func (r *Reader) Manifest() *Manifest { return r.m }

// Close wipes the data key.
func (r *Reader) Close() { zero(r.dk) }

// Records calls fn with each record of a section of a module, in the order
// they were added. Each chunk is checked against the manifest (size, SHA-256,
// authentication, record count) before its records are given out. The slice
// passed to fn is valid only during the call. A module or section the backup
// does not hold has no records.
func (r *Reader) Records(ctx context.Context, s Section, m port.Module, fn func(record []byte) error) error {
	if !s.Valid() || !m.Valid() {
		return fmt.Errorf("backup: section %q module %q", s, m)
	}
	part := r.m.Part(m, s)
	for _, c := range part.Chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := ChunkName(r.m.Installation, r.m.ID, s, m, c.N)
		o, err := r.store.Read(ctx, name)
		if err != nil {
			if errors.Is(err, port.ErrNotFound) {
				return fmt.Errorf("%w: chunk %s is missing", ErrIntegrity, name)
			}
			return fmt.Errorf("backup: read %s: %w", name, err)
		}
		sum := sha256.Sum256(o.Body)
		if int64(len(o.Body)) != c.Size || hex.EncodeToString(sum[:]) != c.SHA256 {
			return fmt.Errorf("%w: chunk %s does not match the manifest", ErrIntegrity, name)
		}
		pt, err := openChunk(r.dk, r.m.Format, name, o.Body)
		if err != nil {
			return err
		}
		n := 0
		for rest := pt; len(rest) > 0; n++ {
			l, k := binary.Uvarint(rest)
			if k <= 0 || l > uint64(len(rest)-k) {
				zero(pt)
				return fmt.Errorf("%w: chunk %s has a damaged record", ErrIntegrity, name)
			}
			rec := rest[k : k+int(l)]
			rest = rest[k+int(l):]
			if err := fn(rec); err != nil {
				zero(pt)
				return err
			}
		}
		zero(pt)
		if n != c.Records {
			return fmt.Errorf("%w: chunk %s holds %d records, the manifest says %d", ErrIntegrity, name, n, c.Records)
		}
	}
	return nil
}

// Verify reads and checks every chunk of the backup.
func (r *Reader) Verify(ctx context.Context) error {
	for _, e := range r.m.Modules {
		for _, s := range Sections() {
			if err := r.Records(ctx, s, e.Module, func([]byte) error { return nil }); err != nil {
				return err
			}
		}
	}
	return nil
}
