package hub

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/internal/port"
)

// SnapshotBlobPrefix and SnapshotLeasePrefix are where the hub's snapshots
// and refresh leases sit in the ports: `snapshots/<workspace>` in the blob
// port and `lease.<kind>:<workspace>` in the state port
// (docs/concepts/sluis/ports.md).
const (
	SnapshotBlobPrefix  = "snapshots/"
	SnapshotLeasePrefix = "lease."
)

// BlobSnapshots is a [SnapshotStore] and a [Locker] over the blob and state
// ports. It is the whole of what the Valkey-backed store did, minus Valkey:
// the encoding is the one the legacy cache has always written, and the lease
// is a Create with a lifetime, released by deleting it only if it is still
// the one this caller took.
//
// A snapshot read is decoded once per version of the blob: the blob is
// still read every time, so what is answered is always what is stored,
// but the decompression and decoding of a whole tenant -- most of what a
// read costs this process -- happens again only when the content changed.
type BlobSnapshots struct {
	blob   port.Blob
	leases port.State

	mu      sync.Mutex
	decoded map[string]decodedSnapshot
}

// decodedSnapshot is the last snapshot of one workspace this store
// decoded, and the version of the blob it was decoded from.
type decodedSnapshot struct {
	version string
	snap    *Snapshot
}

var (
	_ SnapshotStore = (*BlobSnapshots)(nil)
	_ Locker        = (*BlobSnapshots)(nil)
)

// NewBlobSnapshots returns the store.
func NewBlobSnapshots(blob port.Blob, leases port.State) *BlobSnapshots {
	return &BlobSnapshots{blob: blob, leases: leases, decoded: map[string]decodedSnapshot{}}
}

// Get implements [SnapshotStore].
//
// The snapshot returned for an unchanged blob is the one returned last
// time, shared. Nothing in this package changes a snapshot it was handed
// -- a patch is made on a clone ([Snapshot.clone]) -- and a caller
// outside it must not either.
func (s *BlobSnapshots) Get(ctx context.Context, workspace string) (*Snapshot, error) {
	object, err := s.blob.Read(ctx, SnapshotBlobPrefix+workspace)
	if errors.Is(err, port.ErrNotFound) {
		s.forget(workspace)
		return nil, nil //nolint:nilnil // absence is not an error: there is simply no snapshot yet
	}
	if err != nil {
		return nil, fmt.Errorf("read the snapshot of %s: %w", workspace, err)
	}
	if object.Version != "" {
		s.mu.Lock()
		held, ok := s.decoded[workspace]
		s.mu.Unlock()
		if ok && held.version == object.Version {
			return held.snap, nil
		}
	}
	snap, err := DecodeSnapshot(object.Body)
	if err != nil {
		return nil, err
	}
	if object.Version != "" {
		s.mu.Lock()
		s.decoded[workspace] = decodedSnapshot{version: object.Version, snap: snap}
		s.mu.Unlock()
	}
	return snap, nil
}

// forget drops what was decoded for a workspace.
func (s *BlobSnapshots) forget(workspace string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.decoded, workspace)
}

// Put implements [SnapshotStore].
func (s *BlobSnapshots) Put(ctx context.Context, snap *Snapshot) error {
	body, err := EncodeSnapshot(snap)
	if err != nil {
		return err
	}
	// Forgotten rather than replaced with snap: the caller still holds
	// snap, and the next read decodes what was actually stored.
	s.forget(snap.Workspace)
	if _, err = s.blob.Write(ctx, SnapshotBlobPrefix+snap.Workspace, body); err != nil {
		return fmt.Errorf("store the snapshot of %s: %w", snap.Workspace, err)
	}
	return nil
}

// Delete implements [SnapshotStore].
func (s *BlobSnapshots) Delete(ctx context.Context, workspace string) error {
	s.forget(workspace)
	if err := s.blob.Delete(ctx, SnapshotBlobPrefix+workspace); err != nil {
		return fmt.Errorf("delete the snapshot of %s: %w", workspace, err)
	}
	return nil
}

// Lock implements [Locker].
func (s *BlobSnapshots) Lock(
	ctx context.Context, name string, ttl time.Duration,
) (func(context.Context), bool, error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, false, fmt.Errorf("take the %s lease: %w", name, err)
	}
	// The lease always expires. A replica killed mid-refresh must not
	// stop every other replica from ever refreshing that workspace again.
	key := SnapshotLeasePrefix + name
	rev, err := s.leases.Create(ctx, key, []byte(hex.EncodeToString(token)), ttl)
	if errors.Is(err, port.ErrExists) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("take the %s lease: %w", name, err)
	}
	return func(ctx context.Context) {
		// Only if it is still the one taken: a replica whose lease had
		// expired must not delete the lease of whoever took it next.
		_ = s.leases.DeleteIfRevision(ctx, key, rev)
	}, true, nil
}

// wire is what a snapshot looks like on the way to a blob.
//
// The reverse index is deliberately not stored: it is derived from the
// groups, so writing it would double the payload and make a corrupted
// copy possible -- an index that disagrees with the memberships it came
// from would answer questions about people wrongly, quietly.
type wire struct {
	Workspace string            `json:"workspace"`
	TakenAt   time.Time         `json:"takenAt"`
	Accounts  []backend.Account `json:"accounts"`
	Groups    []backend.Group   `json:"groups"`
	// Discovered is every group the read held before narrowing. It is
	// omitempty so that a snapshot written by an older replica during a
	// rollout decodes into "the kept groups are all there were", which is
	// what it meant.
	Discovered []string `json:"discovered,omitempty"`
}

// EncodeSnapshot writes a snapshot compressed. A directory of any size is
// mostly repeated domain names and repeated addresses, which gzip takes down
// by roughly an order of magnitude -- worth it for something written once per
// refresh interval and read on every miss. The bytes are the ones the Valkey
// cache has always held.
func EncodeSnapshot(snap *Snapshot) ([]byte, error) {
	if snap == nil {
		return nil, errors.New("nothing to store")
	}
	out := wire{Workspace: snap.Workspace, TakenAt: snap.TakenAt, Discovered: snap.Discovered}
	for _, email := range sortedKeys(snap.Accounts) {
		out.Accounts = append(out.Accounts, snap.Accounts[email])
	}
	for _, email := range sortedKeys(snap.Groups) {
		out.Groups = append(out.Groups, snap.Groups[email])
	}

	var buf bytes.Buffer
	zip := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zip).Encode(out); err != nil {
		return nil, fmt.Errorf("encode the snapshot of %s: %w", snap.Workspace, err)
	}
	if err := zip.Close(); err != nil {
		return nil, fmt.Errorf("encode the snapshot of %s: %w", snap.Workspace, err)
	}
	return buf.Bytes(), nil
}

// DecodeSnapshot rebuilds a snapshot, index and all.
func DecodeSnapshot(blob []byte) (*Snapshot, error) {
	zip, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return nil, fmt.Errorf("the stored snapshot is not readable: %w", err)
	}
	defer func() { _ = zip.Close() }()

	var in wire
	if err = json.NewDecoder(io.LimitReader(zip, maxSnapshotBytes)).Decode(&in); err != nil {
		return nil, fmt.Errorf("the stored snapshot is not readable: %w", err)
	}
	// Rebuilt through the ordinary constructor, so a snapshot read back
	// is indexed exactly like one just taken.
	return NewSnapshot(in.Workspace, in.TakenAt, in.Accounts, in.Groups, in.Discovered), nil
}

// maxSnapshotBytes bounds what one decompression may produce. The value
// is far above any real directory; it is here so that a corrupt or
// hostile value in the cache cannot be turned into unbounded memory.
const maxSnapshotBytes = 512 << 20

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Sorted so that two encodings of the same snapshot are identical,
	// which makes a stored value diffable by a human debugging one.
	slices.Sort(out)
	return out
}
