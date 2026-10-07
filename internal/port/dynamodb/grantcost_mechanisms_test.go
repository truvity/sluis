package dynamodb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/porttest/grantcost"
)

// The mechanisms that make a grant cheap, over the DynamoDB fake: the same
// behaviour internal/issuer tests over the in-memory adapter, where the
// revision checks are DynamoDB conditional writes.

const (
	gcLifetime = 12 * time.Hour
	gcPerson   = "ada@north.example"
)

// hookedState runs a hook before an Update of a key with the prefix named,
// once.
type hookedState struct {
	port.State
	mu     sync.Mutex
	prefix string
	before func()
}

func (h *hookedState) Update(ctx context.Context, key string, value []byte, ttl time.Duration, rev port.Revision) (port.Revision, error) {
	h.mu.Lock()
	fn := h.before
	if fn != nil && len(key) >= len(h.prefix) && key[:len(h.prefix)] == h.prefix {
		h.before = nil
	} else {
		fn = nil
	}
	h.mu.Unlock()
	if fn != nil {
		fn()
	}
	return h.State.Update(ctx, key, value, ttl, rev)
}

type gcSetup struct {
	store    *Store
	state    *hookedState
	sessions *issuer.Sessions
}

func newGCSetup(t *testing.T, absolute time.Duration) *gcSetup {
	t.Helper()
	store := fakeStore(t, newFake())
	hooked := &hookedState{State: store}
	sessions := issuer.NewSessions(issuer.NewPortState(hooked, store), gcLifetime, absolute)
	sessions.SetClock(store.clock)
	var mu sync.Mutex
	n := 0
	sessions.SetIDs(func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Sprintf("s%d", n)
	})
	return &gcSetup{store: store, state: hooked, sessions: sessions}
}

func (s *gcSetup) open(t *testing.T, token string) issuer.Session {
	t.Helper()
	session, err := s.sessions.Record(context.Background(), issuer.Opened{
		Identity: gcPerson, ClientID: "cli", How: issuer.HowCode, Token: token, AuthTime: s.store.clock(),
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	return session
}

func (s *gcSetup) refresh(t *testing.T, from, to string) (string, bool) {
	t.Helper()
	_, got, ok, err := s.sessions.Refreshed(context.Background(), from, to)
	if err != nil {
		t.Fatalf("Refreshed(%s->%s): %v", from, to, err)
	}
	return got, ok
}

func (s *gcSetup) live(t *testing.T, token string) bool {
	t.Helper()
	_, ok, err := s.sessions.ByToken(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// pointerKey is where a refresh token's pointer is kept: hashed, as the
// issuer does.
func pointerKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "issuer:session-token:" + hex.EncodeToString(sum[:])
}

// near is equality within a second: this store's clock is the real one
// plus an offset, so two readings differ by what elapsed between them.
func near(a, b time.Time) bool { return a.Sub(b).Abs() < time.Second }

func TestRotationOverTheFakeSpendsTheOldPointer(t *testing.T) {
	t.Parallel()
	s := newGCSetup(t, 0)
	ctx := context.Background()
	opened := s.open(t, "t0")
	if got, ok := s.refresh(t, "t0", "t1"); !ok || got != "t1" {
		t.Fatalf("Refreshed = %q, %v", got, ok)
	}
	rec, err := s.store.Get(ctx, pointerKey("t0"))
	if err != nil || string(rec.Value) != "spent:t1" {
		t.Fatalf("the old pointer = %q, %v; want spent:t1", rec.Value, err)
	}

	// A replay inside the grace window is answered with the successor.
	if got, ok := s.refresh(t, "t0", "t-other"); !ok || got != "t1" {
		t.Errorf("replay = %q, %v; want t1", got, ok)
	}
	if s.live(t, "t-other") || !s.live(t, "t1") || s.live(t, "t0") {
		t.Error("after a replay exactly the successor should be live")
	}

	s.store.Advance(29 * time.Second)
	if _, err = s.store.Get(ctx, pointerKey("t0")); err != nil {
		t.Errorf("the spent pointer lapsed before 30 seconds: %v", err)
	}
	s.store.Advance(2 * time.Second)
	if _, err = s.store.Get(ctx, pointerKey("t0")); err == nil {
		t.Error("the spent pointer outlived its 30 seconds")
	}
	if _, ok := s.refresh(t, "t0", "t-late"); ok {
		t.Error("a replay after the grace window was answered")
	}
	if _, live, _ := s.sessions.ByID(ctx, opened.ID); !live {
		t.Error("the session ended with its spent token")
	}
}

func TestConcurrentRefreshesOverTheFakeHaveOneWinner(t *testing.T) {
	t.Parallel()
	for round := range 5 {
		s := newGCSetup(t, 0)
		from := fmt.Sprintf("r%d", round)
		s.open(t, from)

		const racers = 8
		got := make([]string, racers)
		var wg sync.WaitGroup
		for i := range racers {
			wg.Go(func() {
				_, token, ok, err := s.sessions.Refreshed(context.Background(), from, fmt.Sprintf("%s-n%d", from, i))
				if err != nil || !ok {
					t.Errorf("racer %d: %v, %v", i, ok, err)
				}
				got[i] = token
			})
		}
		wg.Wait()

		distinct, live := map[string]bool{}, 0
		for i := range racers {
			distinct[got[i]] = true
			if s.live(t, fmt.Sprintf("%s-n%d", from, i)) {
				live++
			}
		}
		if len(distinct) != 1 || live != 1 {
			t.Errorf("round %d: %d distinct successors and %d live tokens, want 1 and 1", round, len(distinct), live)
		}
	}
}

// A revocation landing between the rotation's read and either of its
// conditional writes is not undone.
func TestARevocationDuringARefreshOverTheFakeIsNotUndone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, c := range []struct{ name, prefix string }{
		{"before the old pointer is spent", "issuer:session-token:"},
		{"before the record is written", "issuer:session:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newGCSetup(t, 0)
			session := s.open(t, "t0")
			fired := false
			s.state.mu.Lock()
			s.state.prefix = c.prefix
			s.state.before = func() {
				fired = true
				if _, err := s.sessions.RevokeID(ctx, session.ID); err != nil {
					t.Error(err)
				}
			}
			s.state.mu.Unlock()

			if _, ok := s.refresh(t, "t0", "t1"); ok {
				t.Error("a refresh of a session revoked mid-way was answered")
			}
			if !fired {
				t.Fatal("the conditional write was never reached, so the guard is not exercised")
			}
			if _, live, _ := s.sessions.ByID(ctx, session.ID); live {
				t.Error("the revoked session is live again")
			}
			if _, err := s.store.Get(ctx, "issuer:session:"+session.ID); err == nil {
				t.Error("the revoked session's record was written back")
			}
			if s.live(t, "t1") {
				t.Error("the refresh left a working token behind")
			}
		})
	}
}

func TestTheLegacyRotatedKeyIsStillHonouredOverTheFake(t *testing.T) {
	t.Parallel()
	s := newGCSetup(t, 0)
	ctx := context.Background()
	session := s.open(t, "t0")
	// What the previous version's rotation left.
	if err := s.store.Delete(ctx, pointerKey("t0")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Put(ctx, pointerKey("t1"), []byte(session.ID), gcLifetime); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("t0"))
	if _, err := s.store.Put(ctx, "issuer:session-rotated:"+hex.EncodeToString(sum[:]), []byte("t1"), 30*time.Second); err != nil {
		t.Fatal(err)
	}

	got, ok := s.refresh(t, "t0", "t-other")
	if !ok || got != "t1" {
		t.Errorf("replay of a legacy-rotated token = %q, %v; want t1", got, ok)
	}
	if s.live(t, "t-other") {
		t.Error("a legacy replay minted a second live credential")
	}
}

func TestIndexMembershipOverTheFake(t *testing.T) {
	t.Parallel()
	s := newGCSetup(t, 0)
	ctx := context.Background()
	session := s.open(t, "t0")
	if want := s.store.clock().Add(2 * gcLifetime); !near(session.IndexedUntil, want) {
		t.Errorf("IndexedUntil = %v, want twice the refresh lifetime from now (%v)", session.IndexedUntil, want)
	}

	s.store.Advance(gcLifetime * 9 / 10)
	s.refresh(t, "t0", "t1")
	if kept, _, _ := s.sessions.ByID(ctx, session.ID); !kept.IndexedUntil.Equal(session.IndexedUntil) {
		t.Error("a refresh the entry covers re-added the session to the index")
	}

	s.store.Advance(gcLifetime * 9 / 10)
	s.refresh(t, "t1", "t2")
	moved, _, _ := s.sessions.ByID(ctx, session.ID)
	if want := s.store.clock().Add(2 * gcLifetime); !near(moved.IndexedUntil, want) {
		t.Errorf("IndexedUntil = %v after a refresh past the cover, want %v", moved.IndexedUntil, want)
	}
	for _, q := range []issuer.Query{{}, {Identity: gcPerson}, {ClientID: "cli"}} {
		if got, err := s.sessions.List(ctx, q); err != nil || len(got) != 1 {
			t.Errorf("List(%+v) = %d, %v; want the live session", q, len(got), err)
		}
	}
}

func TestListingDropsEndedSessionsAnIndexSetStillHoldsOverTheFake(t *testing.T) {
	t.Parallel()
	s := newGCSetup(t, 2*time.Hour)
	ctx := context.Background()
	session := s.open(t, "t0")

	s.store.Advance(3 * time.Hour) // past the absolute limit, inside every lifetime
	members, err := s.store.Members(ctx, "issuer:sessions")
	if err != nil || !slices.Contains(members, session.ID) {
		t.Fatalf("the index set = %v, %v; want it to hold the ended session, or this proves nothing", members, err)
	}
	for _, q := range []issuer.Query{{}, {Identity: gcPerson}, {ClientID: "cli"}} {
		if got, err := s.sessions.List(ctx, q); err != nil || len(got) != 0 {
			t.Errorf("List(%+v) = %d sessions, %v; want the ended one dropped", q, len(got), err)
		}
	}
}

// The held-groups write is skipped for an eighth of the window and made for
// a change, over the fake.
func TestHeldWriteOverTheFake(t *testing.T) {
	t.Parallel()
	store := fakeStore(t, newFake())
	counter := &grantcost.Counter{}
	state := issuer.NewPortState(counter.State(store), store)
	dir := &gcDirectory{standing: issuer.Standing{Found: true, Groups: []string{"eng"}, Authoritative: true}}
	const window = 8 * time.Hour
	r := issuer.NewResolver(dir, window)
	r.SetClock(store.clock)
	r.UseState(state)
	ctx := context.Background()

	writes := func() int {
		n := 0
		for _, op := range counter.Ops() {
			if op.Write && op.Kind == "issuer-held" {
				n++
			}
		}
		return n
	}
	resolve := func() {
		t.Helper()
		if _, err := r.Resolve(ctx, gcPerson); err != nil {
			t.Fatal(err)
		}
	}

	resolve()
	resolve()
	store.Advance(window/8 - time.Second)
	resolve()
	if writes() != 1 {
		t.Errorf("same groups inside an eighth of the window made %d writes, want 1", writes())
	}
	store.Advance(time.Second)
	resolve()
	if writes() != 2 {
		t.Errorf("same groups an eighth of the window on made %d writes, want 2", writes())
	}
	dir.standing.Groups = []string{"eng", "sre"}
	resolve()
	if writes() != 3 {
		t.Errorf("changed groups made %d writes, want 3", writes())
	}

	// A record another process wrote since is not left in place: the skip
	// holds only while the revision is still the one this process wrote.
	other, _ := json.Marshal(map[string]any{"groups": []string{"wide"}, "at": store.clock()})
	if _, err := store.Put(ctx, "issuer:held:"+gcPerson, other, window); err != nil {
		t.Fatal(err)
	}
	resolve()
	if writes() != 4 {
		t.Errorf("a record replaced by another process was left in place (%d writes, want 4)", writes())
	}
	resolve()
	if writes() != 4 {
		t.Errorf("an answer already written was written again (%d writes, want 4)", writes())
	}
}

type gcDirectory struct{ standing issuer.Standing }

func (d *gcDirectory) ResolveUser(context.Context, string) (issuer.Standing, error) {
	return d.standing, nil
}

// A grant that carries a session records its client among the sign-in's
// clients once, at sign-in; refreshes skip it; a session recorded before the
// flag existed is recorded once more at its next refresh.
//
// Not parallel: building the issuer's provider writes package-level state in
// the OIDC library, which two parallel builds race on under -race.
func TestInvolveHappensOnceAtSignInOverTheFake(t *testing.T) {
	ctx := context.Background()
	store := fakeStore(t, newFake())
	h := grantcost.New(t, grantcost.Env{Set: store.Set(), Advance: store.Advance})
	adds := func() int {
		n := 0
		for _, op := range h.Counter.Ops() {
			if op.Port == grantcost.PortIndex && op.Call == "add" && op.Kind == "sso-clients" {
				n++
			}
		}
		return n
	}

	h.Counter.Reset()
	first := h.Grant()
	if n := adds(); n != 1 {
		t.Errorf("a sign-in and its redemption recorded the client %d times, want 1", n)
	}
	sessions, err := h.Issuer.Sessions().List(ctx, issuer.Query{Identity: grantcost.Person})
	if err != nil || len(sessions) != 1 || !sessions[0].Involved {
		t.Fatalf("sessions = %+v, %v; want one carrying the flag", sessions, err)
	}

	h.Counter.Reset()
	second := h.Refresh(first.Refresh)
	third := h.Refresh(second.Refresh)
	if second.Status != http.StatusOK || third.Status != http.StatusOK {
		t.Fatalf("refreshes: %d, %d", second.Status, third.Status)
	}
	if n := adds(); n != 0 {
		t.Errorf("two refreshes recorded the client %d times, want 0", n)
	}

	key := "issuer:session:" + sessions[0].ID
	record, err := store.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(record.Value, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "involved")
	raw, _ := json.Marshal(fields)
	if _, err = store.Update(ctx, key, raw, gcLifetime, record.Revision); err != nil {
		t.Fatal(err)
	}

	h.Counter.Reset()
	fourth := h.Refresh(third.Refresh)
	if fourth.Status != http.StatusOK {
		t.Fatalf("refresh of a pre-change session: %d %q", fourth.Status, fourth.Error)
	}
	if n := adds(); n != 1 {
		t.Errorf("the first refresh of a pre-change session recorded the client %d times, want 1", n)
	}
	h.Counter.Reset()
	if fifth := h.Refresh(fourth.Refresh); fifth.Status != http.StatusOK {
		t.Fatalf("refresh: %d", fifth.Status)
	}
	if n := adds(); n != 0 {
		t.Errorf("the refresh after that recorded the client %d times, want 0", n)
	}
}
