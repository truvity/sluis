package secretstore

import (
	"context"
	"encoding/hex"
	"errors"
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

// Secrets is the [port.Secrets] of an installation on layout v4:
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
type Secrets struct {
	stores *Stores
	grace  time.Duration
	now    func() time.Time
}

var (
	_ port.Secrets           = (*Secrets)(nil)
	_ clientcreds.Overlapper = (*Secrets)(nil)
)

// NewSecrets returns the Secrets port over the v4 stores.
func NewSecrets(stores *Stores, grace time.Duration) *Secrets {
	if grace <= 0 {
		grace = DefaultGrace
	}
	return &Secrets{stores: stores, grace: grace, now: time.Now}
}

// Overlap is the grace period: the overlap of every rotation.
func (s *Secrets) Overlap() time.Duration { return s.grace }

// WithClock replaces the clock the grace period is compared to, for a test.
func (s *Secrets) WithClock(now func() time.Time) *Secrets { s.now = now; return s }

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
	if err := port.CheckSecretPath(path); err != nil {
		return port.Secret{}, err
	}
	return s.getV4(ctx, path)
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

// getRecord builds the record of a generated client from its
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

// put writes the secret. version is nil for an unconditional write.
func (s *Secrets) put(ctx context.Context, path string, value []byte, version *string) (string, error) {
	if err := port.CheckSecretWrite(path, value); err != nil {
		return "", err
	}
	return s.putV4(ctx, path, value, version)
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
	var err error
	if id, ok := oidcClient(path); ok {
		err = s.stores.External.Store().Delete(ctx, "oidc/"+segment(id))
	} else {
		err = s.stores.Internal.Store().Delete(ctx, internalKey(path))
	}
	if err != nil && !errors.Is(err, state.ErrNotFound) {
		return err
	}
	return nil
}

// List implements [port.Secrets]. A v4 listing is of the keys directly under
// the prefix (the state store lists one level): the callers list one kind's
// ids, `credentials/oidc-client`, and ask whether `credentials` answers.
func (s *Secrets) List(ctx context.Context, prefix string) ([]string, error) {
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
