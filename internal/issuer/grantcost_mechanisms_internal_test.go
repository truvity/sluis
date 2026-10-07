package issuer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/port/memory"
)

// The mechanisms that make a grant cheap, tested one by one: the
// per-request resolution, the held-groups write, the index membership and
// the one-write refresh rotation. internal/port/porttest/grantcost holds the
// totals; these hold the behaviour each total depends on.

// ---------------------------------------------------------------- doubles

type gcClock struct {
	mu sync.Mutex
	t  time.Time
}

func newGCClock() *gcClock { return &gcClock{t: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)} }

func (c *gcClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *gcClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// gcDirectory answers per address and counts how often it was asked.
type gcDirectory struct {
	mu       sync.Mutex
	calls    map[string]int
	standing map[string]Standing
}

func newGCDirectory() *gcDirectory {
	return &gcDirectory{calls: map[string]int{}, standing: map[string]Standing{}}
}

func (d *gcDirectory) set(email string, s Standing) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.standing[email] = s
}

func (d *gcDirectory) count(email string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[email]
}

func (d *gcDirectory) ResolveUser(_ context.Context, email string) (Standing, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls[email]++
	return d.standing[email], nil
}

func gcAuth(groups ...string) Standing {
	return Standing{Found: true, Groups: groups, Authoritative: true}
}

type gcAdd struct {
	key string
	ttl time.Duration
}

// gcState counts what passes through it and keeps the revision-aware calls
// of the State beneath, which must have them. A hook runs before a call it
// names, once.
type gcState struct {
	State
	mu       sync.Mutex
	heldSets int
	adds     []gcAdd
	before   func(op, key string)
	peekErr  error
	peeks    int
}

func (s *gcState) hook(op, key string) {
	s.mu.Lock()
	fn := s.before
	s.mu.Unlock()
	if fn != nil {
		fn(op, key)
	}
}

func (s *gcState) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if strings.HasPrefix(key, "issuer:held:") {
		s.mu.Lock()
		s.heldSets++
		s.mu.Unlock()
	}
	return s.State.Set(ctx, key, value, ttl)
}

func (s *gcState) Add(ctx context.Context, key, member string, ttl time.Duration) error {
	s.mu.Lock()
	s.adds = append(s.adds, gcAdd{key, ttl})
	s.mu.Unlock()
	return s.State.Add(ctx, key, member, ttl)
}

func (s *gcState) SetVersion(ctx context.Context, key string, value []byte, ttl time.Duration) (string, error) {
	if strings.HasPrefix(key, "issuer:held:") {
		s.mu.Lock()
		s.heldSets++
		s.mu.Unlock()
	}
	return s.State.(peekingState).SetVersion(ctx, key, value, ttl)
}

func (s *gcState) PeekVersion(ctx context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	s.peeks++
	err := s.peekErr
	s.mu.Unlock()
	if err != nil {
		return "", false, err
	}
	return s.State.(peekingState).PeekVersion(ctx, key)
}

func (s *gcState) GetVersion(ctx context.Context, key string) ([]byte, string, bool, error) {
	return s.State.(versionedState).GetVersion(ctx, key)
}

func (s *gcState) Replace(ctx context.Context, key string, value []byte, ttl time.Duration, version string) error {
	s.hook("replace", key)
	return s.State.(versionedState).Replace(ctx, key, value, ttl, version)
}

func (s *gcState) DeleteVersion(ctx context.Context, key, version string) error {
	s.hook("delete-version", key)
	return s.State.(versionedDeleter).DeleteVersion(ctx, key, version)
}

func (s *gcState) held() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heldSets
}

func (s *gcState) takeAdds() []gcAdd {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.adds
	s.adds = nil
	return out
}

// gcPlain hides the revision-aware calls of the State it wraps, as a State
// that keeps no revisions does (the Valkey-backed one).
type gcPlain struct{ State }

// gcEnv is Sessions over one kind of State, with a clock that moves the
// State's and the Sessions' together.
type gcEnv struct {
	name      string
	state     State // what Sessions sees, wrapped to count
	counting  *gcState
	sessions  *Sessions
	clock     *gcClock
	versioned bool
}

func (e *gcEnv) advance(d time.Duration) { e.clock.advance(d) }

const gcLifetime = 12 * time.Hour

// gcKinds are the States the rotation must work over.
var gcKinds = []struct {
	name      string
	versioned bool
	build     func(clock *gcClock) State
}{
	{"memory-state", true, func(c *gcClock) State {
		m := NewMemoryState()
		m.SetClock(c.now)
		return m
	}},
	{"ports", true, func(c *gcClock) State {
		s := memory.New(memory.WithClock(c.now))
		return NewPortState(s, s)
	}},
	{"no-revisions", false, func(c *gcClock) State {
		m := NewMemoryState()
		m.SetClock(c.now)
		return gcPlain{m}
	}},
}

func gcEachKind(t *testing.T, absolute time.Duration, versionedOnly bool, fn func(t *testing.T, e *gcEnv)) {
	t.Helper()
	for _, kind := range gcKinds {
		if versionedOnly && !kind.versioned {
			continue
		}
		t.Run(kind.name, func(t *testing.T) {
			t.Parallel()
			clock := newGCClock()
			inner := kind.build(clock)
			state := inner
			counting := &gcState{State: inner}
			if kind.versioned {
				state = counting
			}
			sessions := NewSessions(state, gcLifetime, absolute)
			sessions.SetClock(clock.now)
			n := 0
			var mu sync.Mutex
			sessions.SetIDs(func() string {
				mu.Lock()
				defer mu.Unlock()
				n++
				return fmt.Sprintf("s%d", n)
			})
			fn(t, &gcEnv{
				name: kind.name, state: state, counting: counting, sessions: sessions,
				clock: clock, versioned: kind.versioned,
			})
		})
	}
}

const gcPerson = "ada@north.example"

func (e *gcEnv) open(t *testing.T, token string) Session {
	t.Helper()
	session, err := e.sessions.Record(context.Background(), Opened{
		Identity: gcPerson, ClientID: "cli", How: HowCode, Token: token, AuthTime: e.clock.now(),
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	return session
}

func (e *gcEnv) rawPointer(t *testing.T, token string) (string, bool) {
	t.Helper()
	raw, found, err := e.state.Get(context.Background(), sessionTokenKey(token))
	if err != nil {
		t.Fatalf("read the pointer: %v", err)
	}
	return string(raw), found
}

func (e *gcEnv) refresh(t *testing.T, from, to string) (Session, string, bool) {
	t.Helper()
	session, token, ok, err := e.sessions.Refreshed(context.Background(), from, to)
	if err != nil {
		t.Fatalf("Refreshed(%s->%s): %v", from, to, err)
	}
	return session, token, ok
}

func (e *gcEnv) live(t *testing.T, token string) bool {
	t.Helper()
	_, ok, err := e.sessions.ByToken(context.Background(), token)
	if err != nil {
		t.Fatalf("ByToken: %v", err)
	}
	return ok
}

// ------------------------------------------------- 1. the per-request memo

func TestResolveAsksTheDirectoryOncePerRequest(t *testing.T) {
	t.Parallel()
	dir := newGCDirectory()
	dir.set(gcPerson, gcAuth("eng"))
	r := NewResolver(dir, time.Hour)
	ctx := withResolutions(context.Background())

	for _, spelling := range []string{gcPerson, "ADA@north.example", "  Ada@North.example "} {
		got, err := r.Resolve(ctx, spelling)
		if err != nil || !slices.Equal(got.Groups, []string{"eng"}) {
			t.Fatalf("Resolve(%q) = %+v, %v", spelling, got, err)
		}
	}
	if n := dir.count(gcPerson); n != 1 {
		t.Errorf("the directory was asked %d times within one request, want 1", n)
	}
}

func TestResolveMemoIsPerRequestAndPerPerson(t *testing.T) {
	t.Parallel()
	dir := newGCDirectory()
	dir.set(gcPerson, gcAuth("eng"))
	dir.set("bob@north.example", gcAuth("ops"))
	r := NewResolver(dir, time.Hour)
	ctx := context.Background()

	first, second := withResolutions(ctx), withResolutions(ctx)
	if _, err := r.Resolve(first, gcPerson); err != nil {
		t.Fatal(err)
	}
	// A second request is asked afresh, and so sees a change at once.
	dir.set(gcPerson, gcAuth("eng", "sre"))
	got, err := r.Resolve(second, gcPerson)
	if err != nil || !slices.Equal(got.Groups, []string{"eng", "sre"}) {
		t.Fatalf("second request = %+v, %v; want the new groups", got, err)
	}
	if n := dir.count(gcPerson); n != 2 {
		t.Errorf("two requests asked the directory %d times, want 2", n)
	}
	// The first still answers what it first learned: one answer per request.
	if got, _ = r.Resolve(first, gcPerson); !slices.Equal(got.Groups, []string{"eng"}) {
		t.Errorf("the first request's answer moved to %v mid-request", got.Groups)
	}

	// Another person in the same request is not answered with this one's entry.
	bob, err := r.Resolve(first, "bob@north.example")
	if err != nil || !slices.Equal(bob.Groups, []string{"ops"}) {
		t.Errorf("a second person in one request = %+v, %v; want their own groups", bob, err)
	}
	if dir.count("bob@north.example") != 1 {
		t.Error("the second person was not asked about")
	}
}

func TestResolveWithoutAMemoAsksEveryTime(t *testing.T) {
	t.Parallel()
	dir := newGCDirectory()
	dir.set(gcPerson, gcAuth("eng"))
	r := NewResolver(dir, time.Hour)
	for range 3 {
		if _, err := r.Resolve(context.Background(), gcPerson); err != nil {
			t.Fatal(err)
		}
	}
	if n := dir.count(gcPerson); n != 3 {
		t.Errorf("outside a request the directory was asked %d times, want every time (3)", n)
	}
}

func TestForgetDropsTheRequestsMemo(t *testing.T) {
	t.Parallel()
	dir := newGCDirectory()
	dir.set(gcPerson, gcAuth("eng"))
	r := NewResolver(dir, time.Hour)
	ctx := withResolutions(context.Background())

	if _, err := r.Resolve(ctx, gcPerson); err != nil {
		t.Fatal(err)
	}
	if err := r.Forget(ctx, gcPerson); err != nil {
		t.Fatal(err)
	}
	dir.set(gcPerson, Standing{Found: true, Suspended: true, Authoritative: true})
	_, err := r.Resolve(ctx, gcPerson)
	var refused *Refused
	if !errors.As(err, &refused) {
		t.Errorf("after Forget the request was answered from before it: %v", err)
	}
	if n := dir.count(gcPerson); n != 2 {
		t.Errorf("the directory was asked %d times, want 2 (before and after Forget)", n)
	}
}

func TestResolveMemoKeepsARefusalAndHandsOutCopies(t *testing.T) {
	t.Parallel()
	dir := newGCDirectory()
	dir.set(gcPerson, gcAuth("eng", "ops"))
	r := NewResolver(dir, time.Hour)
	ctx := withResolutions(context.Background())

	got, err := r.Resolve(ctx, gcPerson)
	if err != nil {
		t.Fatal(err)
	}
	got.Groups[0] = "tampered"
	again, _ := r.Resolve(ctx, gcPerson)
	if !slices.Equal(again.Groups, []string{"eng", "ops"}) {
		t.Errorf("a caller changing its groups changed the next caller's: %v", again.Groups)
	}

	other := withResolutions(context.Background())
	dir.set("eve@north.example", Standing{Found: true, Suspended: true, Authoritative: true})
	for range 2 {
		if _, err = r.Resolve(other, "eve@north.example"); err == nil {
			t.Fatal("a suspended account was resolved")
		}
	}
	if n := dir.count("eve@north.example"); n != 1 {
		t.Errorf("a refusal asked the directory %d times within one request, want 1", n)
	}
}

func TestResolveMemoIsRaceFree(t *testing.T) {
	t.Parallel()
	dir := newGCDirectory()
	dir.set(gcPerson, gcAuth("eng"))
	r := NewResolver(dir, time.Hour)
	ctx := withResolutions(context.Background())
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if _, err := r.Resolve(ctx, gcPerson); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

// Every HTTP request gets a memo of its own and none outlives it.
func TestEachHTTPRequestGetsItsOwnResolutionMemo(t *testing.T) {
	t.Parallel()
	dir := newGCDirectory()
	dir.set(gcPerson, gcAuth("eng"))
	r := NewResolver(dir, time.Hour)

	handler := withOneResolution(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		for range 3 {
			if _, err := r.Resolve(req.Context(), gcPerson); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}))
	for range 2 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	}
	if n := dir.count(gcPerson); n != 2 {
		t.Errorf("two requests of three resolutions each asked the directory %d times, want 2", n)
	}
}

// ----------------------------------------- 2. the held-groups write

func gcResolver(window time.Duration, dir Directory, state State, clock *gcClock) *Resolver {
	r := NewResolver(dir, window)
	r.SetClock(clock.now)
	r.UseState(state)
	return r
}

func gcHeldState(clock *gcClock) *gcState {
	m := NewMemoryState()
	m.SetClock(clock.now)
	return &gcState{State: m}
}

func TestHeldAnswerIsRewrittenOnlyWhenGroupsChangeOrAnEighthWindowPasses(t *testing.T) {
	t.Parallel()
	const window = 8 * time.Hour
	clock := newGCClock()
	state := gcHeldState(clock)
	dir := newGCDirectory()
	dir.set(gcPerson, gcAuth("eng"))
	r := gcResolver(window, dir, state, clock)
	ctx := context.Background()
	resolve := func() {
		t.Helper()
		if _, err := r.Resolve(ctx, gcPerson); err != nil {
			t.Fatal(err)
		}
	}
	step := window / heldRewrite

	resolve()
	if state.held() != 1 {
		t.Fatalf("the first answer made %d writes, want 1", state.held())
	}
	resolve()
	clock.advance(step - time.Nanosecond)
	resolve()
	if state.held() != 1 {
		t.Errorf("same groups under an eighth of the window made %d writes, want still 1", state.held())
	}
	clock.advance(time.Nanosecond) // exactly window/8 since the write
	resolve()
	if state.held() != 2 {
		t.Errorf("same groups an eighth of the window on made %d writes, want 2", state.held())
	}

	clock.advance(time.Second)
	dir.set(gcPerson, gcAuth("eng", "sre"))
	resolve()
	if state.held() != 3 {
		t.Errorf("changed groups made %d writes, want 3 at once", state.held())
	}
	resolve()
	if state.held() != 3 {
		t.Errorf("the changed groups were written again straight away (%d)", state.held())
	}

	raw, _, _ := state.Get(ctx, heldKey(gcPerson))
	var rec heldRecord
	if err := json.Unmarshal(raw, &rec); err != nil || !slices.Equal(rec.Groups, []string{"eng", "sre"}) {
		t.Errorf("the stored record = %s (%v), want the changed groups", raw, err)
	}
}

func TestHeldWriteIsPerProcessAndResetByForgetAndUseState(t *testing.T) {
	t.Parallel()
	const window = 8 * time.Hour
	clock := newGCClock()
	state := gcHeldState(clock)
	dir := newGCDirectory()
	dir.set(gcPerson, gcAuth("eng"))
	ctx := context.Background()

	a := gcResolver(window, dir, state, clock)
	if _, err := a.Resolve(ctx, gcPerson); err != nil {
		t.Fatal(err)
	}
	// What another process wrote says nothing about this one's own writes.
	b := gcResolver(window, dir, state, clock)
	if _, err := b.Resolve(ctx, gcPerson); err != nil {
		t.Fatal(err)
	}
	if state.held() != 2 {
		t.Errorf("a second process made %d writes in all, want 2 (one each)", state.held())
	}

	if err := a.Forget(ctx, gcPerson); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Resolve(ctx, gcPerson); err != nil {
		t.Fatal(err)
	}
	if state.held() != 3 {
		t.Errorf("after Forget the answer was not written again (%d writes)", state.held())
	}

	other := gcHeldState(clock)
	a.UseState(other)
	if _, err := a.Resolve(ctx, gcPerson); err != nil {
		t.Fatal(err)
	}
	if other.held() != 1 {
		t.Errorf("a new State was not written to (%d writes)", other.held())
	}
}

// The skip holds only while the record is still the one this process wrote:
// another process's write, a deletion, a failed read and a State that keeps
// no revisions all write.
func TestHeldWriteIsSkippedOnlyWhileTheRecordIsStillOurs(t *testing.T) {
	t.Parallel()
	const window = 8 * time.Hour
	ctx := context.Background()
	setup := func() (*Resolver, *gcState, *gcDirectory, *gcClock) {
		clock := newGCClock()
		state := gcHeldState(clock)
		dir := newGCDirectory()
		dir.set(gcPerson, gcAuth("eng"))
		r := gcResolver(window, dir, state, clock)
		if _, err := r.Resolve(ctx, gcPerson); err != nil {
			t.Fatal(err)
		}
		return r, state, dir, clock
	}
	again := func(r *Resolver) {
		t.Helper()
		if _, err := r.Resolve(ctx, gcPerson); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("untouched record is skipped, at the cost of one peek", func(t *testing.T) {
		t.Parallel()
		r, state, _, _ := setup()
		again(r)
		if state.held() != 1 || state.peeks != 1 {
			t.Errorf("writes=%d peeks=%d, want 1 and 1", state.held(), state.peeks)
		}
	})
	t.Run("another process wrote other groups", func(t *testing.T) {
		t.Parallel()
		r, state, _, _ := setup()
		raw, _ := json.Marshal(heldRecord{Groups: []string{"wide"}, At: time.Now()})
		if err := state.State.Set(ctx, heldKey(gcPerson), raw, window); err != nil {
			t.Fatal(err)
		}
		again(r)
		if state.held() != 2 {
			t.Errorf("a record another process replaced was left in place (%d writes)", state.held())
		}
		got, _, _ := state.Get(ctx, heldKey(gcPerson))
		var rec heldRecord
		_ = json.Unmarshal(got, &rec)
		if !slices.Equal(rec.Groups, []string{"eng"}) {
			t.Errorf("the record holds %v, want what the directory says", rec.Groups)
		}
	})
	t.Run("another process deleted it", func(t *testing.T) {
		t.Parallel()
		r, state, _, _ := setup()
		if err := state.Delete(ctx, heldKey(gcPerson)); err != nil {
			t.Fatal(err)
		}
		again(r)
		if state.held() != 2 {
			t.Errorf("a deleted record was not written again (%d writes)", state.held())
		}
	})
	t.Run("the peek fails", func(t *testing.T) {
		t.Parallel()
		r, state, _, _ := setup()
		state.peekErr = errors.New("unavailable")
		again(r)
		if state.held() != 2 {
			t.Errorf("a failed peek skipped the write (%d writes)", state.held())
		}
	})
	t.Run("a State without revisions always writes", func(t *testing.T) {
		t.Parallel()
		clock := newGCClock()
		inner := NewMemoryState()
		inner.SetClock(clock.now)
		counting := &gcState{State: inner}
		dir := newGCDirectory()
		dir.set(gcPerson, gcAuth("eng"))
		r := gcResolver(window, dir, gcPlain{counting}, clock)
		for range 3 {
			again(r)
		}
		if counting.held() != 3 {
			t.Errorf("a State with no revisions made %d writes for 3 answers, want 3", counting.held())
		}
	})
}

func TestHeldWriteIsNotSkippedWhenTheClockGoesBack(t *testing.T) {
	t.Parallel()
	clock := newGCClock()
	state := gcHeldState(clock)
	dir := newGCDirectory()
	dir.set(gcPerson, gcAuth("eng"))
	r := gcResolver(8*time.Hour, dir, state, clock)
	for _, d := range []time.Duration{0, -time.Minute} {
		clock.advance(d)
		if _, err := r.Resolve(context.Background(), gcPerson); err != nil {
			t.Fatal(err)
		}
	}
	if state.held() != 2 {
		t.Errorf("an answer older than the one stored made %d writes in all, want 2", state.held())
	}
}

// A hold can end up to an eighth of the window early and never late. Pinned
// with a clock: for a last authoritative answer x after the first, the hold
// that ends x+window after the first by the book ends no later than that and
// no more than window/8 earlier.
func TestAHeldAnswerEndsAtMostAnEighthEarlyAndNeverLate(t *testing.T) {
	t.Parallel()
	const window = 8 * time.Hour
	step := window / heldRewrite
	for _, x := range []time.Duration{0, time.Nanosecond, step / 2, step - time.Nanosecond, step, step + time.Minute} {
		t.Run(x.String(), func(t *testing.T) {
			t.Parallel()
			// heldAt: the directory answers authoritatively at the start
			// and again x later, then can vouch for nothing; is the person
			// still held at `at` after the start?
			heldAt := func(at time.Duration) bool {
				clock := newGCClock()
				state := gcHeldState(clock)
				dir := newGCDirectory()
				dir.set(gcPerson, gcAuth("eng"))
				r := gcResolver(window, dir, state, clock)
				ctx := context.Background()
				for _, d := range []time.Duration{0, x} {
					clock.advance(d)
					if _, err := r.Resolve(ctx, gcPerson); err != nil {
						t.Fatal(err)
					}
				}
				dir.set(gcPerson, Standing{Found: true})
				clock.advance(at - x)
				got, err := r.Resolve(ctx, gcPerson)
				return err == nil && got.Held
			}

			end := x + window // window after the last authoritative answer
			if heldAt(end) {
				t.Error("a hold outlived the window measured from the last authoritative answer")
			}
			if !heldAt(end - step) {
				t.Errorf("a hold ended more than an eighth of the window (%v) early", step)
			}
		})
	}
}

// ------------------------------------------------------ 3. (hub) see blobsnapshots_cache_test.go

// -------------------------------------------- 4. rotation in one write

func TestRotationSpendsTheOldPointerAsSpentSuccessorUntilTheSessionEnds(t *testing.T) {
	gcEachKind(t, 0, false, func(t *testing.T, e *gcEnv) {
		session := e.open(t, "t0")
		if raw, _ := e.rawPointer(t, "t0"); raw != session.ID {
			t.Fatalf("the pointer holds %q, want the session id %q", raw, session.ID)
		}

		_, successor, ok := e.refresh(t, "t0", "t1")
		if !ok || successor != "t1" {
			t.Fatalf("Refreshed = %q, %v; want t1", successor, ok)
		}
		raw, found := e.rawPointer(t, "t0")
		mark, isMark := readSpent([]byte(raw))
		if !found || !isMark || mark.session != session.ID {
			t.Errorf("the old pointer = %q (found %v), want the mark of a token spent in %s", raw, found, session.ID)
		}
		if got, opened := mark.successor("t0"); !opened || got != "t1" {
			t.Errorf("the mark's successor opened with the spent token = %q, %v; want t1", got, opened)
		}
		if _, opened := mark.successor("t1"); opened {
			t.Error("the mark's successor opened with a token other than the spent one")
		}
		if strings.Contains(raw, "t1") {
			t.Errorf("the mark %q holds the successor in plain", raw)
		}
		if raw, _ = e.rawPointer(t, "t1"); raw != session.ID {
			t.Errorf("the new pointer = %q, want the session id", raw)
		}
		if _, found, _ := e.state.Get(context.Background(), sessionRotatedKey("t0")); found {
			t.Error("a rotation still wrote the separate issuer:session-rotated: key")
		}

		// Kept past the grace window, so that a reuse is known for one.
		e.advance(31 * time.Second)
		if _, found = e.rawPointer(t, "t0"); !found {
			t.Error("the spent pointer lapsed with the grace window")
		}
	})
}

func TestReplayWithinGraceGetsTheSameSuccessor(t *testing.T) {
	gcEachKind(t, 0, false, func(t *testing.T, e *gcEnv) {
		opened := e.open(t, "t0")
		e.refresh(t, "t0", "t1")

		got, successor, ok := e.refresh(t, "t0", "t-other")
		if !ok || successor != "t1" || got.ID != opened.ID {
			t.Fatalf("replay = %q, %v, session %q; want t1 and the same session", successor, ok, got.ID)
		}
		if e.live(t, "t-other") {
			t.Error("a replay minted a second live credential")
		}
		if !e.live(t, "t1") {
			t.Error("the successor is not live")
		}
		if e.live(t, "t0") {
			t.Error("the spent token still names a session through ByToken")
		}
		if _, ok, err := e.sessions.ByRefreshToken(context.Background(), "t0"); err != nil || !ok {
			t.Errorf("ByRefreshToken of a replay = %v, %v; want the session", ok, err)
		}

		e.advance(31 * time.Second)
		if _, _, ok = e.refresh(t, "t0", "t-late"); ok {
			t.Error("a replay after the grace window was answered")
		}
		if _, ok, _ := e.sessions.ByRefreshToken(context.Background(), "t0"); ok {
			t.Error("ByRefreshToken answered a spent token after the grace window")
		}
	})
}

func TestAReplayOfAnEndedSessionIsRefused(t *testing.T) {
	gcEachKind(t, 0, false, func(t *testing.T, e *gcEnv) {
		session := e.open(t, "t0")
		e.refresh(t, "t0", "t1")
		if ended, err := e.sessions.RevokeID(context.Background(), session.ID); err != nil || !ended {
			t.Fatalf("RevokeID = %v, %v", ended, err)
		}
		if _, _, ok := e.refresh(t, "t0", "t-late"); ok {
			t.Error("a replay was answered with the successor of an ended session")
		}
	})
}

func TestRevokingASpentTokenDoesNotEndTheSession(t *testing.T) {
	gcEachKind(t, 0, false, func(t *testing.T, e *gcEnv) {
		e.open(t, "t0")
		e.refresh(t, "t0", "t1")
		if ended, err := e.sessions.RevokeToken(context.Background(), "t0"); err != nil || ended {
			t.Errorf("RevokeToken(spent) = %v, %v; want false, as the successor is what holds the session", ended, err)
		}
		if !e.live(t, "t1") {
			t.Error("revoking the spent token ended the live one")
		}
	})
}

// Two refreshes of one token at once: one wins, and the other is a replay
// that gets the winner's successor, so there are never two live successors.
func TestConcurrentRefreshesOfOneTokenHaveOneWinner(t *testing.T) {
	gcEachKind(t, 0, true, func(t *testing.T, e *gcEnv) {
		for round := range 25 {
			from := fmt.Sprintf("r%d", round)
			e.open(t, from)

			const racers = 12
			type result struct {
				mine, got string
				ok        bool
				err       error
			}
			results := make([]result, racers)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := range racers {
				wg.Go(func() {
					<-start
					mine := fmt.Sprintf("%s-n%d", from, i)
					_, got, ok, err := e.sessions.Refreshed(context.Background(), from, mine)
					results[i] = result{mine, got, ok, err}
				})
			}
			close(start)
			wg.Wait()

			successors := map[string]bool{}
			winners := 0
			for _, r := range results {
				if r.err != nil || !r.ok {
					t.Fatalf("round %d: a racer got %+v", round, r)
				}
				successors[r.got] = true
				if r.got == r.mine {
					winners++
				}
			}
			if len(successors) != 1 || winners != 1 {
				t.Fatalf("round %d: %d distinct successors and %d winners, want one of each", round, len(successors), winners)
			}
			live := 0
			for _, r := range results {
				if e.live(t, r.mine) {
					live++
					if !successors[r.mine] {
						t.Errorf("round %d: a live token is not the one every racer was handed", round)
					}
				}
			}
			if live != 1 {
				t.Errorf("round %d: %d live successors, want exactly 1", round, live)
			}
		}
	})
}

// A revocation that lands between the refresh's read and its write is not
// undone: the session stays revoked and nothing is handed out.
func TestARevocationBetweenReadAndWriteIsNotUndone(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		revoke func(e *gcEnv, session Session) error
	}{
		{"by session id", func(e *gcEnv, s Session) error {
			_, err := e.sessions.RevokeID(ctx, s.ID)
			return err
		}},
		{"by token", func(e *gcEnv, _ Session) error {
			_, err := e.sessions.RevokeToken(ctx, "t0")
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gcEachKind(t, 0, true, func(t *testing.T, e *gcEnv) {
				session := e.open(t, "t0")
				p, ok, err := e.sessions.present(ctx, "t0")
				if err != nil || !ok {
					t.Fatalf("present = %v, %v", ok, err)
				}
				if err = c.revoke(e, session); err != nil {
					t.Fatal(err)
				}

				_, _, ok, err = e.sessions.rotate(ctx, p, "t1")
				if err != nil || ok {
					t.Fatalf("rotate after a revocation = %v, %v; want refused", ok, err)
				}
				if _, live, _ := e.sessions.ByID(ctx, session.ID); live {
					t.Error("the revoked session is live again")
				}
				if _, found, _ := e.state.Get(ctx, sessionKey(session.ID)); found {
					t.Error("the revoked session's record was written back")
				}
				if e.live(t, "t1") {
					t.Error("the refresh left a working token behind")
				}
			})
		})
	}
}

// The same, landing after the old pointer was spent and before the record is
// written, which is the last window there is.
func TestARevocationBeforeTheRecordWriteIsNotUndone(t *testing.T) {
	ctx := context.Background()
	gcEachKind(t, 0, true, func(t *testing.T, e *gcEnv) {
		session := e.open(t, "t0")
		fired := false
		e.counting.before = func(op, key string) {
			if op == "replace" && key == sessionKey(session.ID) && !fired {
				fired = true
				if _, err := e.sessions.RevokeID(ctx, session.ID); err != nil {
					t.Error(err)
				}
			}
		}

		_, _, ok := e.refresh(t, "t0", "t1")
		if !fired {
			t.Fatal("the record was never written over its revision, so the guard is not exercised")
		}
		if ok {
			t.Error("a refresh of a session revoked before its record write was answered")
		}
		if _, found, _ := e.state.Get(ctx, sessionKey(session.ID)); found {
			t.Error("the revoked session's record was written back")
		}
		if e.live(t, "t1") {
			t.Error("the refresh left a working token behind")
		}
	})
}

// A record changed by something else but still there carries on with this
// refresh's end, as it always did.
func TestARecordWrittenMeanwhileIsStillRefreshed(t *testing.T) {
	ctx := context.Background()
	gcEachKind(t, 0, true, func(t *testing.T, e *gcEnv) {
		session := e.open(t, "t0")
		fired := false
		e.counting.before = func(op, key string) {
			if op == "replace" && key == sessionKey(session.ID) && !fired {
				fired = true
				raw, _ := json.Marshal(session)
				if err := e.state.Set(ctx, key, raw, gcLifetime); err != nil {
					t.Error(err)
				}
			}
		}
		e.advance(time.Hour)
		got, _, ok := e.refresh(t, "t0", "t1")
		if !fired || !ok {
			t.Fatalf("fired %v, ok %v; want a refresh that went through", fired, ok)
		}
		stored, live, _ := e.sessions.ByID(ctx, session.ID)
		if !live || !stored.ExpiresAt.Equal(got.ExpiresAt) {
			t.Errorf("the stored end = %v (live %v), want this refresh's %v", stored.ExpiresAt, live, got.ExpiresAt)
		}
	})
}

// A writer that shortens the session between the rotation's read and its
// write is not undone: the stored end is the shortened one, never the later
// one the refresh computed. A lengthening writer does not extend it either.
func TestARotationNeverExtendsAShortenedSession(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		moved func(computed time.Time) time.Time
		want  func(computed time.Time) time.Time
	}{
		{"shortened meanwhile", func(c time.Time) time.Time { return c.Add(-6 * time.Hour) }, func(c time.Time) time.Time { return c.Add(-6 * time.Hour) }},
		{"lengthened meanwhile", func(c time.Time) time.Time { return c.Add(6 * time.Hour) }, func(c time.Time) time.Time { return c }},
	} {
		t.Run(c.name, func(t *testing.T) {
			gcEachKind(t, 0, true, func(t *testing.T, e *gcEnv) {
				session := e.open(t, "t0")
				e.advance(time.Hour)
				computed := e.clock.now().Add(gcLifetime)
				fired := false
				e.counting.before = func(op, key string) {
					if op != "replace" || key != sessionKey(session.ID) || fired {
						return
					}
					fired = true
					moved := session
					moved.ExpiresAt = c.moved(computed)
					raw, _ := json.Marshal(moved)
					if err := e.state.Set(ctx, key, raw, gcLifetime); err != nil {
						t.Error(err)
					}
				}

				got, _, ok := e.refresh(t, "t0", "t1")
				if !fired || !ok {
					t.Fatalf("fired %v, ok %v; want a refresh that went through", fired, ok)
				}
				want := c.want(computed)
				stored, live, _ := e.sessions.ByID(ctx, session.ID)
				if !live || !stored.ExpiresAt.Equal(want) {
					t.Errorf("stored end = %v (live %v), want %v", stored.ExpiresAt, live, want)
				}
				if !got.ExpiresAt.Equal(want) {
					t.Errorf("the refresh answered end %v, want %v", got.ExpiresAt, want)
				}
			})
		})
	}
}

// With nothing racing, the refresh sets the end it computed.
func TestARotationWithoutARaceSetsTheComputedEnd(t *testing.T) {
	gcEachKind(t, 0, false, func(t *testing.T, e *gcEnv) {
		session := e.open(t, "t0")
		e.advance(time.Hour)
		want := e.clock.now().Add(gcLifetime)
		got, _, ok := e.refresh(t, "t0", "t1")
		if !ok || !got.ExpiresAt.Equal(want) {
			t.Fatalf("answered end %v (ok %v), want %v", got.ExpiresAt, ok, want)
		}
		stored, live, _ := e.sessions.ByID(context.Background(), session.ID)
		if !live || !stored.ExpiresAt.Equal(want) {
			t.Errorf("stored end = %v (live %v), want %v", stored.ExpiresAt, live, want)
		}
	})
}

// The previous version recorded a spent token's successor under a key of its
// own; a token it rotated during a rollout is still answered.
func TestTheLegacyRotatedKeyIsStillHonoured(t *testing.T) {
	gcEachKind(t, 0, false, func(t *testing.T, e *gcEnv) {
		ctx := context.Background()
		session := e.open(t, "t0")
		// What the old rotation left: t0's pointer gone, t1's in place and
		// the successor recorded beside.
		if err := e.state.Delete(ctx, sessionTokenKey("t0")); err != nil {
			t.Fatal(err)
		}
		if err := e.state.Set(ctx, sessionTokenKey("t1"), []byte(session.ID), gcLifetime); err != nil {
			t.Fatal(err)
		}
		if err := e.state.Set(ctx, sessionRotatedKey("t0"), []byte("t1"), refreshGrace); err != nil {
			t.Fatal(err)
		}

		if got, ok, err := e.sessions.ByRefreshToken(ctx, "t0"); err != nil || !ok || got.ID != session.ID {
			t.Errorf("ByRefreshToken = %q, %v, %v; want the session", got.ID, ok, err)
		}
		got, successor, ok := e.refresh(t, "t0", "t-other")
		if !ok || successor != "t1" || got.ID != session.ID {
			t.Errorf("replay of a legacy-rotated token = %q, %v; want t1", successor, ok)
		}
		if e.live(t, "t-other") {
			t.Error("a legacy replay minted a second live credential")
		}

		e.advance(31 * time.Second)
		if _, _, ok = e.refresh(t, "t0", "t-late"); ok {
			t.Error("a legacy-rotated token was answered after its grace")
		}
	})
}

// The no-revision path writes plainly and still rotates, replays and refuses.
func TestRotationOverAStateWithoutRevisions(t *testing.T) {
	t.Parallel()
	clock := newGCClock()
	inner := NewMemoryState()
	inner.SetClock(clock.now)
	state := gcPlain{inner}
	if _, ok := State(state).(versionedState); ok {
		t.Fatal("the plain State offers revisions, so this tests nothing")
	}
	sessions := NewSessions(state, gcLifetime, 0)
	sessions.SetClock(clock.now)
	ctx := context.Background()

	if _, err := sessions.Record(ctx, Opened{Identity: gcPerson, ClientID: "cli", How: HowCode, Token: "t0", AuthTime: clock.now()}); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"t1", "t2", "t3"} {
		_, got, ok, err := sessions.Refreshed(ctx, map[string]string{"t1": "t0", "t2": "t1", "t3": "t2"}[token], token)
		if err != nil || !ok || got != token {
			t.Fatalf("Refreshed -> %s = %q, %v, %v", token, got, ok, err)
		}
	}
	if _, got, ok, _ := sessions.Refreshed(ctx, "t2", "x"); !ok || got != "t3" {
		t.Errorf("replay = %q, %v; want t3", got, ok)
	}
	if _, ok, _ := sessions.ByToken(ctx, "t3"); !ok {
		t.Error("the latest token is not live")
	}
	clock.advance(time.Minute)
	if _, _, ok, _ := sessions.Refreshed(ctx, "t2", "y"); ok {
		t.Error("a spent token was answered after the grace window")
	}
}

// ---------------------------------------------------------- 5. IndexedUntil

func gcIndexKeys(session Session) []string {
	return []string{sessionAllKey, sessionOfKey(session.Identity), sessionForKey(session.ClientID)}
}

func TestIndexEntriesAreAddedWithTwiceTheRefreshLifetime(t *testing.T) {
	gcEachKind(t, 0, true, func(t *testing.T, e *gcEnv) {
		session := e.open(t, "t0")
		adds := e.counting.takeAdds()
		if len(adds) != 3 {
			t.Fatalf("Record made %d index adds, want 3: %+v", len(adds), adds)
		}
		for _, a := range adds {
			if a.ttl != 2*gcLifetime || !slices.Contains(gcIndexKeys(session), a.key) {
				t.Errorf("add %+v, want one of %v for twice the refresh lifetime", a, gcIndexKeys(session))
			}
		}
		if want := e.clock.now().Add(2 * gcLifetime); !session.IndexedUntil.Equal(want) {
			t.Errorf("IndexedUntil = %v, want %v", session.IndexedUntil, want)
		}
	})
}

func TestARefreshReAddsToTheIndexOnlyWhenTheEntryWouldLapse(t *testing.T) {
	gcEachKind(t, 0, true, func(t *testing.T, e *gcEnv) {
		ctx := context.Background()
		session := e.open(t, "t0")
		e.counting.takeAdds()

		// New end = +10.8h+12h = +22.8h, inside the +24h the entry covers.
		e.advance(gcLifetime * 9 / 10)
		e.refresh(t, "t0", "t1")
		if adds := e.counting.takeAdds(); len(adds) != 0 {
			t.Errorf("a refresh whose end the entry covers made %d index adds: %+v", len(adds), adds)
		}
		kept, _, _ := e.sessions.ByID(ctx, session.ID)
		if !kept.IndexedUntil.Equal(session.IndexedUntil) {
			t.Errorf("IndexedUntil moved to %v without an add", kept.IndexedUntil)
		}

		// New end = +21.6h+12h = +33.6h, past +24h: the entry would lapse.
		e.advance(gcLifetime * 9 / 10)
		e.refresh(t, "t1", "t2")
		adds := e.counting.takeAdds()
		if len(adds) != 3 {
			t.Fatalf("a refresh past the entry's cover made %d index adds, want 3: %+v", len(adds), adds)
		}
		for _, a := range adds {
			if a.ttl != 2*gcLifetime {
				t.Errorf("re-add %+v, want twice the refresh lifetime", a)
			}
		}
		moved, _, _ := e.sessions.ByID(ctx, session.ID)
		if want := e.clock.now().Add(2 * gcLifetime); !moved.IndexedUntil.Equal(want) {
			t.Errorf("IndexedUntil = %v after the re-add, want %v", moved.IndexedUntil, want)
		}

		// And it is covered again.
		e.advance(time.Hour)
		e.refresh(t, "t2", "t3")
		if adds = e.counting.takeAdds(); len(adds) != 0 {
			t.Errorf("a refresh after the re-add made %d index adds", len(adds))
		}
	})
}

// A session recorded before the field existed has none, and is added again
// at its next refresh, once.
func TestASessionWithoutIndexedUntilIsReAddedOnce(t *testing.T) {
	gcEachKind(t, 0, true, func(t *testing.T, e *gcEnv) {
		ctx := context.Background()
		session := e.open(t, "t0")
		session.IndexedUntil = time.Time{}
		raw, _ := json.Marshal(session)
		if err := e.state.Set(ctx, sessionKey(session.ID), raw, gcLifetime); err != nil {
			t.Fatal(err)
		}
		e.counting.takeAdds()

		e.advance(time.Minute)
		e.refresh(t, "t0", "t1")
		if adds := e.counting.takeAdds(); len(adds) != 3 {
			t.Fatalf("the first refresh of a session with no IndexedUntil made %d adds, want 3", len(adds))
		}
		e.advance(time.Minute)
		e.refresh(t, "t1", "t2")
		if adds := e.counting.takeAdds(); len(adds) != 0 {
			t.Errorf("the second refresh made %d adds, want none", len(adds))
		}
	})
}

// Listings show only live sessions, whatever a set still holds.
func TestListingDropsEndedSessionsAnIndexSetStillHolds(t *testing.T) {
	gcEachKind(t, 2*time.Hour, false, func(t *testing.T, e *gcEnv) {
		ctx := context.Background()
		session := e.open(t, "t0")
		queries := []Query{{}, {Identity: gcPerson}, {ClientID: "cli"}}

		for _, q := range queries {
			if got, err := e.sessions.List(ctx, q); err != nil || len(got) != 1 {
				t.Fatalf("List(%+v) = %d, %v; want the live session", q, len(got), err)
			}
		}

		// Past the 2h absolute limit, the record still stored (12h) and the
		// sets still holding the id (24h): ended, and not listed.
		e.advance(3 * time.Hour)
		if members, _ := e.state.Members(ctx, sessionAllKey); !slices.Contains(members, session.ID) {
			t.Fatal("the index set no longer holds the id, so this test proves nothing")
		}
		for _, q := range queries {
			if got, err := e.sessions.List(ctx, q); err != nil || len(got) != 0 {
				t.Errorf("List(%+v) = %d sessions, %v; want the ended one dropped", q, len(got), err)
			}
		}

		// Past the record's own life the set still holds the id, and a
		// listing drops it from the set as it goes. A second session, as
		// the first was repaired by the listings above.
		second := e.open(t, "t9")
		e.advance(13 * time.Hour)
		if _, found, _ := e.state.Get(ctx, sessionKey(second.ID)); found {
			t.Fatal("the record is still stored, so this test proves nothing")
		}
		if members, _ := e.state.Members(ctx, sessionAllKey); !slices.Contains(members, second.ID) {
			t.Fatal("the index set was shorter than the record's life")
		}
		if got, err := e.sessions.List(ctx, Query{}); err != nil || len(got) != 0 {
			t.Errorf("List = %d, %v; want none", len(got), err)
		}
		if members, _ := e.state.Members(ctx, sessionAllKey); slices.Contains(members, second.ID) {
			t.Error("a listing left an id with no record in the set")
		}
	})
}
