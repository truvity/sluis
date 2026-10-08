package secretstore

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/storage/state"
)

// DefaultGrace is how long a rotated client secret's previous value is still
// accepted: the overlap of ADR 0039.
const DefaultGrace = 24 * time.Hour

// maxHistory bounds the walk back to a document's first revision.
const maxHistory = 100

// Secrets is the [port.Secrets] of an installation on layout transition or v4:
// the callers keep their paths (`credentials/<kind>/<id>/<ref>`, `config/…`)
// and this maps each to its v4 address, so that nothing above the Secrets port
// knows the layout.
//
//   - `credentials/oidc-client/<id>/secret`, the record of a generated client,
//     is the document external/oidc/<id>. The record callers see is built from
//     the document and its revisions: Current from the document, Previous from
//     [state.Value.Rotating] (the revision before the current one, for the
//     grace period), Created and Rotated from the first and current revisions.
//     Writing a record writes its Current secret as a new revision and nothing
//     else is kept (an orphan mark is derived from the policy).
//   - every other path under `credentials/` or `config/` is the internal value
//     of the same path, a {"value": <base64>} document.
//   - `export/…` is v3's and is passed through; the exports controller still
//     runs in these layouts.
//
// In transition every write goes to v4 and then to v3, and a read tries v4
// first; in v4 v3 is never touched except for `export/`.
type Secrets struct {
	layout Layout
	stores *Stores
	v3     port.Secrets
	grace  time.Duration
	now    func() time.Time
}

var (
	_ port.Secrets           = (*Secrets)(nil)
	_ clientcreds.Overlapper = (*Secrets)(nil)
)

// NewSecrets returns the Secrets port over the v4 stores. v3 is the layout-v3
// port: required in transition and for `export/`, otherwise may be nil.
func NewSecrets(stores *Stores, v3 port.Secrets, grace time.Duration) *Secrets {
	if grace <= 0 {
		grace = DefaultGrace
	}
	return &Secrets{layout: stores.Layout, stores: stores, v3: v3, grace: grace, now: time.Now}
}

// Overlap is the grace period: the overlap of every rotation.
func (s *Secrets) Overlap() time.Duration { return s.grace }

// WithClock replaces the clock the grace period is compared to, for a test.
func (s *Secrets) WithClock(now func() time.Time) *Secrets { s.now = now; return s }

func isExport(path string) bool { return strings.HasPrefix(path, port.ExportPrefix) }

// oidcClient is the client id of a generated client's record path.
func oidcClient(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, port.CredentialsPrefix+clientcreds.Kind+"/")
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, "/secret")
	if !ok || id == "" || strings.Contains(id, "/") {
		return "", false
	}
	if h, ok := strings.CutPrefix(id, "u-"); ok {
		if b, err := hex.DecodeString(h); err == nil {
			id = string(b)
		}
	}
	return id, true
}

func toPort(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, state.ErrNotFound):
		return port.ErrNotFound
	case errors.Is(err, state.ErrConflict):
		return port.ErrConflict
	}
	return err
}

// internalKey is the internal store key of a path.
func internalKey(path string) string { return path }

// Get implements [port.Secrets].
func (s *Secrets) Get(ctx context.Context, path string) (port.Secret, error) {
	if isExport(path) {
		return s.v3Get(ctx, path)
	}
	if err := port.CheckSecretPath(path); err != nil {
		return port.Secret{}, err
	}
	if s.layout.ReadsV4() {
		got, err := s.getV4(ctx, path)
		if !errors.Is(err, port.ErrNotFound) || s.layout != LayoutTransition {
			return got, err
		}
	}
	return s.v3Get(ctx, path)
}

func (s *Secrets) v3Get(ctx context.Context, path string) (port.Secret, error) {
	if s.v3 == nil {
		return port.Secret{}, port.ErrNotFound
	}
	return s.v3.Get(ctx, path)
}

func (s *Secrets) getV4(ctx context.Context, path string) (port.Secret, error) {
	if id, ok := oidcClient(path); ok {
		return s.getRecord(ctx, id)
	}
	v := state.NewValue(s.stores.Internal.Store(), internalKey(path), state.Raw())
	val, rev, err := v.Get(ctx)
	if err != nil {
		return port.Secret{}, toPort(err)
	}
	return port.Secret{Value: val, Version: string(rev)}, nil
}

// getRecord builds the v3-shaped record of a generated client from its
// document and revisions.
func (s *Secrets) getRecord(ctx context.Context, id string) (port.Secret, error) {
	doc := s.stores.External.OIDC(id).WithClock(s.now)
	cur, prev, err := doc.Rotating(ctx, s.grace)
	if err != nil {
		return port.Secret{}, toPort(err)
	}
	it, err := s.stores.External.Store().Get(ctx, "oidc/"+segment(id))
	if err != nil {
		return port.Secret{}, toPort(err)
	}
	rec := clientcreds.Record{V: clientcreds.RecordVersion, Current: cur.ClientSecret, Created: it.Modified}
	if it.Previous != "" {
		rec.Rotated = it.Modified
		rec.Created = s.firstRevision(ctx, "oidc/"+segment(id), it)
	}
	if prev != nil {
		rec.Previous = prev.ClientSecret
		rec.PreviousValidUntil = it.Modified.Add(s.grace)
	}
	body, err := rec.Encode()
	if err != nil {
		return port.Secret{}, err
	}
	return port.Secret{Value: body, Version: string(it.Rev)}, nil
}

// firstRevision is when the document was first written: the Modified of the
// oldest revision still held, found by walking Previous.
func (s *Secrets) firstRevision(ctx context.Context, key string, it state.Item) time.Time {
	first := it.Modified
	for i := 0; i < maxHistory && it.Previous != ""; i++ {
		older, err := s.stores.External.Store().GetRev(ctx, key, it.Previous)
		if err != nil {
			break
		}
		it, first = older, older.Modified
	}
	return first
}

// Put implements [port.Secrets].
func (s *Secrets) Put(ctx context.Context, path string, value []byte) (string, error) {
	return s.put(ctx, path, value, nil)
}

// PutIfVersion implements [port.Secrets].
func (s *Secrets) PutIfVersion(ctx context.Context, path string, value []byte, version string) (string, error) {
	return s.put(ctx, path, value, &version)
}

// put writes to v4 and then, in transition, to v3. version is nil for an
// unconditional write.
func (s *Secrets) put(ctx context.Context, path string, value []byte, version *string) (string, error) {
	if isExport(path) {
		return s.v3Put(ctx, path, value, version)
	}
	if err := port.CheckSecretWrite(path, value); err != nil {
		return "", err
	}
	if !s.layout.WritesV4() {
		return s.v3Put(ctx, path, value, version)
	}
	if s.layout != LayoutTransition {
		return s.putV4(ctx, path, value, version)
	}
	// Transition. The caller saw a version from v4 when v4 holds the secret,
	// else from v3: condition the write on the store that holds it, and write
	// the other unconditionally once the first has won.
	if _, err := s.getV4(ctx, path); err == nil {
		rev, err := s.putV4(ctx, path, value, version)
		if err != nil {
			return "", err
		}
		return rev, s.mirrorV3(ctx, path, value)
	} else if !errors.Is(err, port.ErrNotFound) {
		return "", err
	}
	// Absent from v4: v3 decides (a create-only write is conflict-checked
	// there too), then v4 takes the value as its first revision.
	if _, err := s.v3Put(ctx, path, value, version); err != nil {
		return "", err
	}
	rev, err := s.putV4(ctx, path, value, nil)
	if errors.Is(err, port.ErrConflict) {
		return "", err
	}
	return rev, err
}

func (s *Secrets) mirrorV3(ctx context.Context, path string, value []byte) error {
	if s.v3 == nil {
		return nil
	}
	if _, err := s.v3.Put(ctx, path, value); err != nil {
		return fmt.Errorf("secretstore: v4 holds the new value and the v3 copy could not be written: %w", err)
	}
	return nil
}

func (s *Secrets) v3Put(ctx context.Context, path string, value []byte, version *string) (string, error) {
	if s.v3 == nil {
		return "", fmt.Errorf("%w: no layout-v3 store for %s", port.ErrUnsupported, path)
	}
	if version == nil {
		return s.v3.Put(ctx, path, value)
	}
	return s.v3.PutIfVersion(ctx, path, value, *version)
}

func (s *Secrets) putV4(ctx context.Context, path string, value []byte, version *string) (string, error) {
	if id, ok := oidcClient(path); ok {
		return s.putRecord(ctx, id, value, version)
	}
	key := internalKey(path)
	v := state.NewValue(s.stores.Internal.Store(), key, state.Raw())
	var rev state.Rev
	var err error
	if version == nil {
		rev, err = overwrite(func() (state.Rev, error) { return currentRev(ctx, s.stores.Internal.Store(), key) },
			func(ifRev state.Rev) (state.Rev, error) { return v.Put(ctx, value, ifRev) })
	} else {
		rev, err = v.Put(ctx, value, state.Rev(*version))
	}
	return string(rev), toPort(err)
}

// currentRev is the revision of key, "" when it is absent.
func currentRev(ctx context.Context, st state.Store, key string) (state.Rev, error) {
	it, err := st.Get(ctx, key)
	if errors.Is(err, state.ErrNotFound) {
		return "", nil
	}
	return it.Rev, err
}

// overwrite writes on top of whatever is current, once more if another writer
// got in between.
func overwrite(current func() (state.Rev, error), put func(state.Rev) (state.Rev, error)) (state.Rev, error) {
	var err error
	for range 3 {
		var rev state.Rev
		if rev, err = current(); err != nil {
			return "", err
		}
		var written state.Rev
		if written, err = put(rev); !errors.Is(err, state.ErrConflict) {
			return written, err
		}
	}
	return "", err
}

// putRecord writes a generated client's record as its document: the Current
// secret as a new revision. A record whose Current is the document's already
// (an orphan mark, which is derived from the policy now) writes nothing, so it
// cannot push the previous revision out of reach. A rotation without an
// overlap (the record has no Previous) is written twice, so that the previous
// revision is the new secret and the old one is refused at once.
func (s *Secrets) putRecord(ctx context.Context, id string, body []byte, version *string) (string, error) {
	rec, err := clientcreds.DecodeRecord(body)
	if err != nil {
		return "", err
	}
	st := s.stores.External.Store()
	key := "oidc/" + segment(id)
	existing, err := st.Get(ctx, key)
	if err != nil && !errors.Is(err, state.ErrNotFound) {
		return "", err
	}
	exists := err == nil
	doc := OIDCv1{ClientID: id, ClientSecret: rec.Current}
	if exists {
		old, derr := oidcCodec.Unmarshal(existing.Value)
		if derr != nil {
			return "", derr
		}
		if old.ClientSecret == rec.Current {
			if version != nil && *version != string(existing.Rev) {
				return "", port.ErrConflict
			}
			return string(existing.Rev), nil
		}
		if old.ClientID != "" {
			doc.ClientID = old.ClientID // an operator-seeded document names its own
		}
	}
	v := s.stores.External.OIDC(id)
	var rev state.Rev
	switch {
	case version == nil && !exists:
		rev, err = v.Put(ctx, doc, "")
	case version == nil:
		rev, err = v.Put(ctx, doc, existing.Rev)
	default:
		rev, err = v.Put(ctx, doc, state.Rev(*version))
	}
	if err != nil {
		return "", toPort(err)
	}
	if exists && rec.Previous == "" {
		// A hard cut: the revision before the current one is the current one.
		if rev, err = v.Put(ctx, doc, rev); err != nil {
			return "", toPort(err)
		}
	}
	return string(rev), nil
}

func sortedUnique(in []string) []string {
	sort.Strings(in)
	return slices.Compact(in)
}

// Delete implements [port.Secrets]. An absent secret is not an error.
func (s *Secrets) Delete(ctx context.Context, path string) error {
	if isExport(path) || !s.layout.WritesV4() {
		return s.v3Delete(ctx, path)
	}
	var err error
	if id, ok := oidcClient(path); ok {
		err = s.stores.External.Store().Delete(ctx, "oidc/"+segment(id))
	} else {
		err = s.stores.Internal.Store().Delete(ctx, internalKey(path))
	}
	if err != nil && !errors.Is(err, state.ErrNotFound) {
		return err
	}
	if s.layout == LayoutTransition {
		return s.v3Delete(ctx, path)
	}
	return nil
}

func (s *Secrets) v3Delete(ctx context.Context, path string) error {
	if s.v3 == nil {
		return nil
	}
	return s.v3.Delete(ctx, path)
}

// List implements [port.Secrets]. A v4 listing is of the keys directly under
// the prefix (the state store lists one level): the callers list one kind's
// ids, `credentials/oidc-client`, and ask whether `credentials` answers.
func (s *Secrets) List(ctx context.Context, prefix string) ([]string, error) {
	if isExport(prefix) || strings.TrimSuffix(prefix, "/") == strings.TrimSuffix(port.ExportPrefix, "/") || !s.layout.ReadsV4() {
		if s.v3 == nil {
			return nil, nil
		}
		return s.v3.List(ctx, prefix)
	}
	norm, err := port.SecretPrefix(prefix)
	if err != nil {
		return nil, err
	}
	var out []string
	if strings.TrimSuffix(norm, "/") == port.CredentialsPrefix+clientcreds.Kind {
		names, err := s.stores.External.Store().Child("oidc").List(ctx)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			out = append(out, clientcreds.Path(unsegment(n)))
		}
	} else {
		child := s.stores.Internal.Store()
		if norm != "" {
			child = child.Child(strings.TrimSuffix(norm, "/"))
		}
		names, err := child.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			out = append(out, norm+n)
		}
	}
	if s.layout == LayoutTransition && s.v3 != nil {
		old, err := s.v3.List(ctx, prefix)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, p := range out {
			seen[p] = true
		}
		for _, p := range old {
			if !seen[p] {
				out = append(out, p)
			}
		}
	}
	return sortedUnique(out), nil
}

// unsegment undoes [segment] for the `u-` spelling.
func unsegment(s string) string {
	if h, ok := strings.CutPrefix(s, "u-"); ok {
		if b, err := hex.DecodeString(h); err == nil {
			return string(b)
		}
	}
	return s
}
