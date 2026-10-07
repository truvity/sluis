package issuer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/truvity/sluis/policy"
)

// Directory is what the issuer needs from the hub, and all it needs: is
// this account live, which directory groups is it in, and may that answer
// be acted on. The issuer never reads a directory itself — one place
// holds the credentials, and it is not this service.
type Directory interface {
	ResolveUser(ctx context.Context, email string) (Standing, error)
}

// Standing is the hub's answer about one address.
type Standing struct {
	Found         bool
	Suspended     bool
	Groups        []string
	Authoritative bool
	// GivenName and FamilyName are the account's own, when the directory
	// supplies them. Identity, never authorization: they shape what a
	// relying party's UI shows and nothing that it decides. Empty is
	// normal and a consumer falls back to the address.
	GivenName  string
	FamilyName string
}

// lastKnown is what the issuer remembers about an identity so that a hub
// it cannot reach does not immediately lock everyone out.
type lastKnown struct {
	groups []string
	at     time.Time
}

// Resolver turns an address into the directory groups the policy should
// see, and implements the hold window: when the hub cannot vouch for its
// answer, an identity keeps the groups it last had, for a while.
//
// The window is the whole reason this type exists. A directory that goes
// unreachable must not read as "everyone lost their groups", because that
// is indistinguishable from a mass revocation and would take down every
// login at once. Equally it must not last forever, or a real removal
// would never take effect. So: last-known groups, for a bounded time, and
// only for identities already seen.
type Resolver struct {
	dir    Directory
	window time.Duration

	// state is where last-known answers live when more than one instance
	// serves (a Lambda fleet, a rollout that replaces replicas): a fresh
	// instance then finds what the one before it learned. Nil means the
	// answers live in seen, in this process only.
	state State

	mu   sync.Mutex
	seen map[string]lastKnown
	// stored is what this process last wrote to state for each identity,
	// so that an answer it would only write again unchanged is not
	// written: see [Resolver.remember].
	stored map[string]storedHeld
	now    func() time.Time
}

// heldKey is where one identity's last-known answer is kept in the State.
func heldKey(identity string) string { return "issuer:held:" + identity }

// heldRecord is what is written down: the groups and when they were
// answered, and nothing else. They are what the issuer already puts in the
// tokens it mints; no token and no name is kept.
type heldRecord struct {
	Groups []string  `json:"groups"`
	At     time.Time `json:"at"`
}

// UseState keeps last-known answers in a State shared across instances,
// each with a lifetime of the hold window, in place of this process's
// memory. A nil state keeps the memory.
func (r *Resolver) UseState(state State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state = state
	// What was written to another store says nothing about this one.
	r.stored = map[string]storedHeld{}
}

// NewResolver returns a resolver over dir, holding last-known groups for
// window.
func NewResolver(dir Directory, window time.Duration) *Resolver {
	return &Resolver{
		dir: dir, window: window,
		seen: map[string]lastKnown{}, stored: map[string]storedHeld{}, now: time.Now,
	}
}

// SetClock replaces the clock, for tests.
func (r *Resolver) SetClock(now func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = now
}

// Resolution is what the issuer decided about an address, and why.
type Resolution struct {
	Groups []string
	// Held is true when the groups come from the hold window rather than
	// from a fresh authoritative answer. A token is still issued; the
	// distinction is for the audit trail and for the console.
	Held bool
	// GivenName and FamilyName travel with the answer when the directory
	// supplied them, so a token can name a person rather than an address.
	// They are NOT remembered through the hold window: a held answer is
	// the last known GRANTS and nothing else, and a name recovered from
	// memory would be a claim this issuer could not currently vouch for.
	GivenName  string
	FamilyName string
}

// Refused reports that no token may be issued, with the reason.
//
// Authoritative says the directory vouched for the refusal: it answered
// authoritatively that the account is suspended or not found. That is a
// removal, and a refresh that meets it ends its session
// (docs/decisions/0040-agent-class-sessions.md, decision 5). Every other
// refusal -- a directory that cannot vouch with nothing held, a subject that
// names no ServiceAccount -- is not, and ends nothing.
type Refused struct {
	Reason        string
	Authoritative bool
}

func (e *Refused) Error() string { return "refused: " + e.Reason }

// Resolve answers what groups an address should be evaluated with.
//
// The rules, in order: a suspended account gets nothing, because that is
// the whole point of asking the hub at all; an authoritative answer is
// taken and remembered; a non-authoritative answer falls back to what was
// last known, if that is recent enough; an identity never seen before
// gets nothing, because there is nothing to hold.
//
// Within one request (see [resolutions]) an address is resolved once and
// every later call is answered with that first answer.
func (r *Resolver) Resolve(ctx context.Context, email string) (Resolution, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	once := resolutionsFrom(ctx)
	if got, ok := once.get(email); ok {
		return got.res, got.err
	}
	res, err := r.resolve(ctx, email)
	once.put(email, res, err)
	return res, err
}

func (r *Resolver) resolve(ctx context.Context, email string) (Resolution, error) {
	standing, err := r.dir.ResolveUser(ctx, email)

	now := r.clock()

	if err == nil && standing.Authoritative {
		if !standing.Found || standing.Suspended {
			r.forget(ctx, email)
			return Resolution{}, &Refused{Reason: "the directory says this account is not live", Authoritative: true}
		}
		r.remember(ctx, email, lastKnown{groups: standing.Groups, at: now})

		return Resolution{
			Groups:     standing.Groups,
			GivenName:  standing.GivenName,
			FamilyName: standing.FamilyName,
		}, nil
	}

	// Either the hub could not be reached, or it answered without being
	// able to vouch for the answer. Both are the same situation from here:
	// act on what was last known, or on nothing.
	known, ok := r.recall(ctx, email)
	if !ok || now.Sub(known.at) > r.window {
		if err != nil {
			return Resolution{}, err
		}
		return Resolution{}, &Refused{Reason: "the directory cannot be vouched for and this identity has no recent answer"}
	}
	return Resolution{Groups: known.groups, Held: true}, nil
}

func (r *Resolver) clock() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now()
}

// heldRewrite is how stale, as a fraction of the hold window, this
// process lets the time on a held answer it wrote become before it writes
// the same groups again: an eighth, so 30 minutes of a 4-hour window.
const heldRewrite = 8

// remember records an authoritative answer: in the shared State when there
// is one, and in memory when there is none or the State refused the write
// (a store that is down must not turn a good answer into no answer).
//
// An answer this process itself wrote moments ago, with the same groups,
// and that is STILL the record in the State, is not written again. The
// groups are what the hold window keeps, and they have not changed; what
// has is the time beside them, which is where the window is measured
// from. Rewriting it on every request was a State write on every sign-in
// and every refresh -- in an installation where MCP clients refresh every
// few minutes, the commonest write there was -- to move that time forward
// by minutes. It is moved forward once it is an eighth of the window old
// instead, so a hold that starts while it lags ends at most an eighth of
// the window EARLY, never late: the issuer stops acting on a directory it
// cannot reach slightly sooner, which is the safe direction to be wrong in.
//
// "Still the record" is checked, not assumed: another process may have
// written since -- other groups, perhaps wider ones the directory has since
// taken away -- or deleted it, and leaving THAT in place for the hold
// window would be holding groups nobody holds any more. So the skip costs
// one read of the record's revision (eventually consistent where the store
// offers it, and never its value) and happens only when that revision is
// the one this process wrote. Any other answer, a failed read included,
// writes. A State that keeps no revisions always writes.
func (r *Resolver) remember(ctx context.Context, email string, known lastKnown) {
	r.mu.Lock()
	state := r.state
	prev, wrote := r.stored[email]
	r.mu.Unlock()
	if state != nil {
		versioned, canSkip := state.(peekingState)
		if canSkip && wrote && prev.version != "" && slices.Equal(prev.groups, known.groups) &&
			known.at.Sub(prev.at) < r.window/heldRewrite && !known.at.Before(prev.at) {
			if current, found, err := versioned.PeekVersion(ctx, heldKey(email)); err == nil && found && current == prev.version {
				return
			}
		}
		raw, err := json.Marshal(heldRecord{Groups: known.groups, At: known.at})
		var version string
		if err == nil {
			if canSkip {
				version, err = versioned.SetVersion(ctx, heldKey(email), raw, r.window)
			} else {
				err = state.Set(ctx, heldKey(email), raw, r.window)
			}
		}
		if err == nil {
			r.mu.Lock()
			r.stored[email] = storedHeld{groups: slices.Clone(known.groups), at: known.at, version: version}
			r.mu.Unlock()
			return
		}
		slog.WarnContext(ctx, "the issuer could not store the last-known groups; keeping them in memory",
			"error", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen[email] = known
}

// storedHeld is what this process last wrote to the State for one
// identity, with the revision the write was given.
type storedHeld struct {
	groups  []string
	at      time.Time
	version string
}

// recall is what is remembered about an identity. With a State it is the
// State's word alone, so that a removal made by another instance is not
// undone by a stale copy here; memory answers when there is no State or it
// cannot be read.
func (r *Resolver) recall(ctx context.Context, email string) (lastKnown, bool) {
	r.mu.Lock()
	state := r.state
	r.mu.Unlock()
	if state != nil {
		rec, err := getJSON[heldRecord](ctx, state, heldKey(email))
		if err == nil {
			if rec == nil {
				return lastKnown{}, false
			}
			return lastKnown{groups: rec.Groups, at: rec.At}, true
		}
		slog.WarnContext(ctx, "the issuer could not read the last-known groups; using memory", "error", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	known, ok := r.seen[email]
	return known, ok
}

// forget drops an answer from both places. A failure to delete from the
// State is logged and not returned from Resolve, which is refusing anyway;
// the record still ends with its lifetime.
func (r *Resolver) forget(ctx context.Context, email string) {
	if err := r.Forget(ctx, email); err != nil {
		slog.WarnContext(ctx, "the issuer could not delete the last-known groups", "error", err)
	}
}

// Forget drops what is remembered about an address, so that revoking
// someone also stops the hold window from keeping them alive through an
// unreachable hub.
func (r *Resolver) Forget(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	resolutionsFrom(ctx).drop(email)
	r.mu.Lock()
	state := r.state
	delete(r.seen, email)
	delete(r.stored, email)
	r.mu.Unlock()
	if state == nil {
		return nil
	}
	if err := state.Delete(ctx, heldKey(email)); err != nil {
		return fmt.Errorf("issuer: forget the last-known groups: %w", err)
	}
	return nil
}

// Input builds the policy input for a person, given what the hub said.
func (res Resolution) Input(email string) policy.Input {
	return policy.Input{
		Email:           email,
		DirectoryGroups: res.Groups,
		// A held answer still counts as authoritative to the policy: the
		// window has already decided that acting on it is acceptable, and
		// the policy's own Authoritative flag means "may membership grant
		// anything at all", which within the window it may.
		Authoritative: true,
	}
}

// resolutions is one request's memory of what [Resolver.Resolve] answered,
// so that a request asks the directory about any one address once.
//
// A token request asks about the same person several times over: whether
// they may still use the client, what the access token carries, what the
// ID token carries. Each of those was a full resolution -- a workspace
// listing, a snapshot read, a write of the last-known groups -- and in an
// installation where MCP clients refresh every few minutes, that was most
// of what a refresh cost. It is also more than one answer to a question
// that should have one: a snapshot replaced halfway through a request
// could have given the access token and the ID token different groups.
//
// It lives exactly as long as the request ([withOneResolution]) and never
// longer: what the directory says about a person is asked afresh by the
// next request, at whichever replica takes it. Without one installed,
// every call resolves, which is what a caller outside an HTTP request
// (a test, a background task) gets.
type resolutions struct {
	mu   sync.Mutex
	done map[string]resolved
}

type resolved struct {
	res Resolution
	err error
}

type resolutionsKey struct{}

// withResolutions returns ctx carrying an empty [resolutions].
func withResolutions(ctx context.Context) context.Context {
	return context.WithValue(ctx, resolutionsKey{}, &resolutions{done: map[string]resolved{}})
}

// resolutionsFrom is the request's memory, or nil where none was
// installed; every method is safe on nil and remembers nothing.
func resolutionsFrom(ctx context.Context) *resolutions {
	once, _ := ctx.Value(resolutionsKey{}).(*resolutions)
	return once
}

func (o *resolutions) get(email string) (resolved, bool) {
	if o == nil {
		return resolved{}, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	got, ok := o.done[email]
	// A copy of the groups: a caller that narrows the slice it was handed
	// must not narrow it for the next caller in the same request.
	got.res.Groups = slices.Clone(got.res.Groups)
	return got, ok
}

func (o *resolutions) put(email string, res Resolution, err error) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	res.Groups = slices.Clone(res.Groups)
	o.done[email] = resolved{res: res, err: err}
}

// drop forgets an address, so that a request which revokes somebody and
// then asks about them is not answered with what it knew before.
func (o *resolutions) drop(email string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.done, email)
}

// withOneResolution installs a fresh [resolutions] on every request.
func withOneResolution(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(withResolutions(r.Context())))
	})
}
