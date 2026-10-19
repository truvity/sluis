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

// Secrets is the [port.Secrets] of an installation's generated clients: the
// one thing that still goes through the port on layout v5 (ADR 0072). The
// record of a generated client, `credentials/oidc-client/<id>/secret`, is the
// document external/oidc/<id>: the record callers see is built from the
// document and its revisions: Current from the document, Previous from
// [state.Value.Rotating] (the revision before the current one, for the grace
// period), Created and Rotated from the first and current revisions. Writing a
// record writes its Current secret as a new revision and nothing else is kept
// (an orphan mark is derived from the policy). Every other path is
// [port.ErrUnsupported]: a module reads its own names from [StoresV5].
type Secrets struct {
	// docs holds the generated clients' documents, external/oidc/<id>.
	docs  oidcDocs
	grace time.Duration
	now   func() time.Time
}

// oidcDocs is where the generated clients' documents are: s is the store they
// are in (rooted at external/oidc).
type oidcDocs struct {
	s state.Store
}

func (d oidcDocs) key(id string) string { return segment(id) }

func (d oidcDocs) value(id string) state.Value[OIDCv1] {
	return state.NewValue(d.s, d.key(id), state.Codec[OIDCv1](oidcCodec))
}

func (d oidcDocs) list(ctx context.Context) ([]string, error) { return d.s.List(ctx) }

var (
	_ port.Secrets           = (*Secrets)(nil)
	_ clientcreds.Overlapper = (*Secrets)(nil)
)

// NewSecretsV5 returns the Secrets port over layout v5 for the one thing that
// still goes through the port there: the generated clients' records, at
// external/oidc/<id> (ADR 0072). Every other path is [port.ErrUnsupported]: a
// module reads its own names from [StoresV5].
func NewSecretsV5(stores *StoresV5, grace time.Duration) *Secrets {
	if grace <= 0 {
		grace = DefaultGrace
	}
	return &Secrets{docs: oidcDocs{stores.external.Child(string(ModuleOIDC))}, grace: grace, now: time.Now}
}

// errNotClient is a path the layout v5 port does not serve.
func errNotClient(path string) error {
	return fmt.Errorf("%w: secret path %q is not a client record; on layout v5 it is read from its module", port.ErrUnsupported, path)
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

// Get implements [port.Secrets].
func (s *Secrets) Get(ctx context.Context, path string) (port.Secret, error) {
	if err := port.CheckSecretPath(path); err != nil {
		return port.Secret{}, err
	}
	if id, ok := oidcClient(path); ok {
		return s.getRecord(ctx, id)
	}
	return port.Secret{}, errNotClient(path)
}

// getRecord builds the record of a generated client from its
// document and revisions.
func (s *Secrets) getRecord(ctx context.Context, id string) (port.Secret, error) {
	doc := s.docs.value(id).WithClock(s.now)
	cur, prev, err := doc.Rotating(ctx, s.grace)
	if err != nil {
		return port.Secret{}, toPort(err)
	}
	it, err := s.docs.s.Get(ctx, s.docs.key(id))
	if err != nil {
		return port.Secret{}, toPort(err)
	}
	rec := clientcreds.Record{V: clientcreds.RecordVersion, Current: cur.ClientSecret, Created: it.Modified}
	if it.Previous != "" {
		rec.Rotated = it.Modified
		rec.Created = s.firstRevision(ctx, s.docs.key(id), it)
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
		older, err := s.docs.s.GetRev(ctx, key, it.Previous)
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
	if id, ok := oidcClient(path); ok {
		return s.putRecord(ctx, id, value, version)
	}
	return "", errNotClient(path)
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
	st := s.docs.s
	key := s.docs.key(id)
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
	v := s.docs.value(id)
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
	id, ok := oidcClient(path)
	if !ok {
		return errNotClient(path)
	}
	if err := s.docs.s.Delete(ctx, s.docs.key(id)); err != nil && !errors.Is(err, state.ErrNotFound) {
		return err
	}
	return nil
}

// List implements [port.Secrets]: the generated clients' records under
// `credentials/oidc-client`, and nothing under any other prefix.
func (s *Secrets) List(ctx context.Context, prefix string) ([]string, error) {
	norm, err := port.SecretPrefix(prefix)
	if err != nil {
		return nil, err
	}
	var out []string
	if strings.TrimSuffix(norm, "/") == port.CredentialsPrefix+clientcreds.Kind {
		names, err := s.docs.list(ctx)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			out = append(out, clientcreds.Path(unsegment(n)))
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
