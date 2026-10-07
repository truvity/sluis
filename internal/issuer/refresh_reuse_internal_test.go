package issuer

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/port/memory"
)

// A spent refresh token is kept as a mark until its session ends, and a
// presentation of it after the grace window is a reuse. These tests hold the
// mark: when it is read as a replay and when as a reuse, how long it lives,
// what it refuses to open for, and what the session ending does to the
// records around it.

// listings reads the session index the three ways an operator can, and
// returns how many sessions each shows.
func listings(t *testing.T, s *Sessions) (byPerson, byClient, all int) {
	t.Helper()
	ctx := context.Background()
	for i, q := range []Query{{Identity: gcPerson}, {ClientID: "cli"}, {}} {
		got, err := s.List(ctx, q)
		if err != nil {
			t.Fatalf("List(%+v): %v", q, err)
		}
		switch i {
		case 0:
			byPerson = len(got)
		case 1:
			byClient = len(got)
		default:
			all = len(got)
		}
	}
	return byPerson, byClient, all
}

func (e *gcEnv) present(t *testing.T, token string) (presented, bool) {
	t.Helper()
	p, ok, err := e.sessions.present(context.Background(), token)
	if err != nil {
		t.Fatalf("present(%s): %v", token, err)
	}
	return p, ok
}

// end ends the session a reuse found, as the storage does once it has matched
// the client: the session record in hand, deleted with its index entries.
func (e *gcEnv) end(t *testing.T, p presented) {
	t.Helper()
	if err := e.sessions.deleteSession(context.Background(), p.session); err != nil {
		t.Fatalf("deleteSession: %v", err)
	}
}

func (e *gcEnv) sessionLive(t *testing.T, id string) bool {
	t.Helper()
	_, live, err := e.sessions.ByID(context.Background(), id)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	return live
}

// -------------------------------------------------- 1. the window's edge

func TestASpentTokenIsAReplayBeforeThirtySecondsAndAReuseFrom(t *testing.T) {
	for _, c := range []struct {
		after  time.Duration
		replay bool
	}{
		{29 * time.Second, true},
		{30*time.Second - time.Millisecond, true},
		{30 * time.Second, false},
		{31 * time.Second, false},
		{time.Hour, false},
	} {
		t.Run(c.after.String(), func(t *testing.T) {
			gcEachKind(t, 0, false, func(t *testing.T, e *gcEnv) {
				session := e.open(t, "t0")
				e.refresh(t, "t0", "t1")
				e.advance(c.after)

				p, ok := e.present(t, "t0")
				switch {
				case c.replay && (!ok || p.successor != "t1" || p.reused != ""):
					t.Fatalf("at %v: present = %+v, %v; want a replay answered with t1", c.after, p, ok)
				case !c.replay && (ok || p.reused != session.ID || p.successor != ""):
					t.Fatalf("at %v: present = %+v, %v; want a reuse naming %s and no successor", c.after, p, ok, session.ID)
				}
				if _, _, ok = e.refresh(t, "t0", "t-new"); ok == !c.replay {
					t.Errorf("at %v: Refreshed answered = %v, want %v", c.after, ok, c.replay)
				}
				if e.live(t, "t-new") {
					t.Errorf("at %v: a presentation of a spent token minted a live token", c.after)
				}
				// Presenting alone ends nothing: that is the storage's act.
				if !e.sessionLive(t, session.ID) || !e.live(t, "t1") {
					t.Errorf("at %v: presenting a spent token ended the session by itself", c.after)
				}
			})
		})
	}
}

// A reuse is a reuse whatever the successor became since: the mark knows its
// session, not what the chain has turned into.
func TestAReuseIsKnownAfterTheSuccessorWasRotatedAgain(t *testing.T) {
	gcEachKind(t, 0, false, func(t *testing.T, e *gcEnv) {
		ctx := context.Background()
		session := e.open(t, "t0")
		e.refresh(t, "t0", "t1")
		e.advance(31 * time.Second)
		e.refresh(t, "t1", "t2")
		e.advance(31 * time.Second)

		for _, spent := range []string{"t0", "t1"} {
			if p, ok := e.present(t, spent); ok || p.reused != session.ID {
				t.Errorf("present(%s) = %+v, %v; want a reuse naming %s", spent, p, ok, session.ID)
			}
		}

		p, _ := e.present(t, "t0")
		if p.session.ID != session.ID || p.session.Identity != gcPerson || p.session.ClientID != "cli" {
			t.Fatalf("a reuse carries %+v, want the session that was open", p.session)
		}
		e.end(t, p)
		if e.live(t, "t2") {
			t.Error("the successor of a reused token still names a session")
		}
		if _, _, ok := e.refresh(t, "t2", "t3"); ok {
			t.Error("the successor still refreshes after its session was ended")
		}
		if e.sessionLive(t, session.ID) {
			t.Error("the session is live after endReused")
		}
		if _, found, _ := e.state.Get(ctx, sessionKey(session.ID)); found {
			t.Error("the session record is still stored")
		}
		if p, a, c := listings(t, e.sessions); p+a+c != 0 {
			t.Errorf("listings after the end = %d/%d/%d, want empty", p, a, c)
		}
		members, err := e.state.Members(ctx, sessionAllKey)
		if err != nil || len(members) != 0 {
			t.Errorf("the index still holds %v (%v)", members, err)
		}
	})
}

// A reuse of a session already ended -- by an earlier reuse, a revocation or
// its expiry -- names no session: there is nothing to end, so nothing to
// audit a second time. The mark is still there (it outlives the session) and
// the token is refused as it always was.
func TestAReuseOfASessionAlreadyEndedNamesNothingToEnd(t *testing.T) {
	gcEachKind(t, 0, false, func(t *testing.T, e *gcEnv) {
		session := e.open(t, "t0")
		e.refresh(t, "t0", "t1")
		e.advance(31 * time.Second)

		first, ok := e.present(t, "t0")
		if ok || first.reused != session.ID {
			t.Fatalf("present = %+v, %v; want the reuse named", first, ok)
		}
		e.end(t, first)

		if _, found := e.rawPointer(t, "t0"); !found {
			t.Error("the mark lapsed with the session")
		}
		for range 3 {
			if p, ok := e.present(t, "t0"); ok || p.reused != "" || p.session.ID != "" {
				t.Errorf("present after the end = %+v, %v; want a refusal that names no session", p, ok)
			}
		}

		// Ended by a revocation rather than a reuse: the same.
		other := e.open(t, "u0")
		e.refresh(t, "u0", "u1")
		e.advance(31 * time.Second)
		if _, err := e.sessions.RevokeID(context.Background(), other.ID); err != nil {
			t.Fatal(err)
		}
		if p, ok := e.present(t, "u0"); ok || p.reused != "" {
			t.Errorf("present after a revocation = %+v, %v; want no reuse named", p, ok)
		}
	})
}

// Two rotations inside one grace window, then the first token replayed inside
// it: the first token's successor is spent, so nothing is answered, and
// nothing is revoked either: it is a burst, not a reuse.
func TestAReplayOfARotationAlreadyRotatedAgainIsRefusedButRevokesNothing(t *testing.T) {
	gcEachKind(t, 0, false, func(t *testing.T, e *gcEnv) {
		session := e.open(t, "t0")
		e.refresh(t, "t0", "t1")
		e.advance(5 * time.Second)
		e.refresh(t, "t1", "t2")
		e.advance(5 * time.Second)

		if _, _, ok := e.refresh(t, "t0", "t-x"); ok {
			t.Error("the first token was answered although its successor was spent")
		}
		p, ok := e.present(t, "t0")
		if ok || p.reused != "" {
			t.Errorf("present(t0) = %+v, %v; want refused with no reuse named", p, ok)
		}
		if !e.sessionLive(t, session.ID) || !e.live(t, "t2") {
			t.Error("the refusal ended the session or its live token")
		}
		if e.live(t, "t-x") {
			t.Error("a refused replay minted a live token")
		}
		// The second token's own replay is still a replay of the live chain.
		if _, got, ok := e.refresh(t, "t1", "t-y"); !ok || got != "t2" {
			t.Errorf("replay of t1 = %q, %v; want t2", got, ok)
		}
	})
}

// ------------------------------------------------------ 5. mark lifetime

func TestTheMarkLivesUntilTheSessionEndsAndNeverLessThanTheGraceWindow(t *testing.T) {
	cases := []struct {
		name     string
		absolute time.Duration
		// lead is the time passed before the rotation.
		lead time.Duration
		// life is how long the mark must live from the rotation.
		life time.Duration
	}{
		{"uncapped is the refresh lifetime", 0, 0, gcLifetime},
		{"capped by the absolute limit", 10 * time.Minute, 0, 10 * time.Minute},
		{"capped, rotated late in the session", 10 * time.Minute, 7 * time.Minute, 3 * time.Minute},
		{"never less than the grace window", time.Minute, 50 * time.Second, refreshGrace},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gcEachKind(t, c.absolute, false, func(t *testing.T, e *gcEnv) {
				e.open(t, "t0")
				e.advance(c.lead)
				if _, _, ok := e.refresh(t, "t0", "t1"); !ok {
					t.Fatal("the rotation was refused")
				}
				e.advance(c.life - time.Second)
				if _, found := e.rawPointer(t, "t0"); !found {
					t.Errorf("the mark lapsed before %v from the rotation", c.life)
				}
				e.advance(2 * time.Second)
				if _, found := e.rawPointer(t, "t0"); found {
					t.Errorf("the mark outlived %v from the rotation", c.life)
				}
			})
		})
	}
}

// With no absolute limit, or no auth_time to measure it from, the mark lasts
// to the end the rotation set; with both, to the family's deadline. Never
// less than the grace window.
func TestTheMarkLifetimeIsTheFamilyDeadlineTheSessionEndOrTheGraceWindow(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name     string
		absolute time.Duration
		authAgo  time.Duration // auth_time = now - authAgo; negative means none
		end      time.Duration
		want     time.Duration
	}{
		{"no limit: the session's end", 0, time.Hour, 12 * time.Hour, 12 * time.Hour},
		{"no limit, short end: the grace window", 0, time.Hour, 10 * time.Second, 30 * time.Second},
		{"no auth_time: the session's end", 24 * time.Hour, -1, 12 * time.Hour, 12 * time.Hour},
		{"limit: the family's deadline, past the session's end", 24 * time.Hour, 2 * time.Hour, 12 * time.Hour, 22 * time.Hour},
		{"limit: the deadline", 10 * time.Minute, 7 * time.Minute, 10 * time.Minute, 3 * time.Minute},
		{"limit, deadline imminent: the grace window", time.Minute, 50 * time.Second, time.Minute, 30 * time.Second},
		{"limit, deadline passed: the grace window", time.Minute, time.Hour, time.Minute, 30 * time.Second},
		{"end passed: the grace window", 0, time.Hour, -time.Hour, 30 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := NewSessions(NewMemoryState(), gcLifetime, c.absolute)
			session := Session{ExpiresAt: now.Add(c.end)}
			if c.authAgo >= 0 {
				session.AuthTime = now.Add(-c.authAgo)
			}
			if got := s.spentLifetime(session, now); got != c.want {
				t.Errorf("spentLifetime = %v, want %v", got, c.want)
			}
		})
	}
}

// A session that keeps refreshing slides its end forward; the mark of an old
// token must not lapse while the family it would end is still alive.
func TestAnOldMarkOutlivesTheSessionEndItWasSpentUnder(t *testing.T) {
	gcEachKind(t, 24*time.Hour, false, func(t *testing.T, e *gcEnv) {
		session := e.open(t, "t0")
		e.refresh(t, "t0", "t1")
		e.advance(11 * time.Hour)
		e.refresh(t, "t1", "t2")
		e.advance(2 * time.Hour) // 13 h: past the 12 h end t0's rotation set

		if _, found := e.rawPointer(t, "t0"); !found {
			t.Fatal("the first mark lapsed at the session's old end while the family was alive")
		}
		if p, ok := e.present(t, "t0"); ok || p.reused != session.ID {
			t.Errorf("present(t0) = %+v, %v; want the reuse detected at 13 h", p, ok)
		}
		e.advance(10*time.Hour + 59*time.Minute) // 23 h 59 m: the family's last minute
		if _, found := e.rawPointer(t, "t0"); !found {
			t.Error("the mark lapsed before the family's deadline")
		}
		e.advance(2 * time.Minute)
		if _, found := e.rawPointer(t, "t0"); found {
			t.Error("the mark outlived the family's deadline")
		}
	})
}

// ---------------------------------------- 5. malformed and tampered marks

func rawMark(t *testing.T, e *gcEnv, token string) spentMark {
	t.Helper()
	raw, found := e.rawPointer(t, token)
	mark, ok := readSpent([]byte(raw))
	if !found || !ok {
		t.Fatalf("the pointer of %s is %q (found %v), want a mark", token, raw, found)
	}
	return mark
}

func (m spentMark) encode() string {
	return spentPrefix + strconv.FormatInt(m.at.UnixMilli(), 10) + ":" + m.sealed + ":" + m.session
}

func TestATamperedMarkIsRefusedAndRevokesNothing(t *testing.T) {
	flip := func(sealed string) string {
		raw, err := base64.RawURLEncoding.DecodeString(sealed)
		if err != nil {
			panic(err)
		}
		raw[len(raw)-1] ^= 0x01
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	flipNonce := func(sealed string) string {
		raw, _ := base64.RawURLEncoding.DecodeString(sealed)
		raw[0] ^= 0x80
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	cases := []struct {
		name   string
		tamper func(m spentMark) string
	}{
		{"a bit flipped in the sealed part", func(m spentMark) string { m.sealed = flip(m.sealed); return m.encode() }},
		{"a bit flipped in the nonce", func(m spentMark) string { m.sealed = flipNonce(m.sealed); return m.encode() }},
		{"another session's id", func(m spentMark) string { m.session = "s-other"; return m.encode() }},
		{"the time moved a millisecond", func(m spentMark) string { m.at = m.at.Add(time.Millisecond); return m.encode() }},
		{"the time moved to a later one", func(m spentMark) string { m.at = m.at.Add(10 * time.Second); return m.encode() }},
		{"a sealed part too short to hold a nonce", func(m spentMark) string { m.sealed = "AAAA"; return m.encode() }},
		{"a sealed part that is not base64", func(m spentMark) string { m.sealed = "!!not*base64!!"; return m.encode() }},
	}
	for _, c := range cases {
		for _, age := range []time.Duration{time.Second, 31 * time.Second, time.Hour} {
			t.Run(c.name+"/"+age.String(), func(t *testing.T) {
				gcEachKind(t, 0, false, func(t *testing.T, e *gcEnv) {
					ctx := context.Background()
					session := e.open(t, "t0")
					e.refresh(t, "t0", "t1")
					forged := c.tamper(rawMark(t, e, "t0"))
					if err := e.state.Set(ctx, sessionTokenKey("t0"), []byte(forged), gcLifetime); err != nil {
						t.Fatal(err)
					}
					e.advance(age)

					if _, successor, ok := e.refresh(t, "t0", "t-x"); ok {
						t.Fatalf("a tampered mark was answered with %q", successor)
					}
					if p, ok := e.present(t, "t0"); ok || p.reused != "" || p.successor != "" {
						t.Errorf("present = %+v, %v; want a refusal that names no reuse and no successor", p, ok)
					}
					if !e.sessionLive(t, session.ID) || !e.live(t, "t1") {
						t.Error("a tampered mark ended the session or its live token")
					}
					if e.live(t, "t-x") {
						t.Error("a tampered mark minted a live token")
					}
				})
			})
		}
	}
}

// A valid mark moved under another token's key is somebody else's seal: it
// does not open with the token presented, so it ends nothing, however old.
func TestAMarkCopiedUnderAnotherTokenEndsNothing(t *testing.T) {
	gcEachKind(t, 0, false, func(t *testing.T, e *gcEnv) {
		ctx := context.Background()
		session := e.open(t, "t0")
		e.refresh(t, "t0", "t1")
		raw, _ := e.rawPointer(t, "t0")
		if err := e.state.Set(ctx, sessionTokenKey("planted"), []byte(raw), gcLifetime); err != nil {
			t.Fatal(err)
		}
		for _, age := range []time.Duration{0, time.Minute} {
			e.advance(age)
			if p, ok := e.present(t, "planted"); ok || p.reused != "" || p.session.ID != "" {
				t.Errorf("after %v: present(planted) = %+v, %v; want an unknown token", age, p, ok)
			}
		}
		if !e.sessionLive(t, session.ID) || !e.live(t, "t1") {
			t.Error("a planted mark ended the session")
		}
	})
}

func TestAMarkThatCannotBeReadIsNeverAReuseOrAReplay(t *testing.T) {
	for _, forged := range []string{
		"spent:",
		"spent:123",
		"spent:123:abc",
		"spent:123::s1",
		"spent:123:abc:",
		"spent:notanumber:abc:s1",
		"spent:-:abc:s1",
		"spent::abc:s1",
	} {
		t.Run(forged, func(t *testing.T) {
			gcEachKind(t, 0, false, func(t *testing.T, e *gcEnv) {
				ctx := context.Background()
				session := e.open(t, "t0")
				e.refresh(t, "t0", "t1")
				if err := e.state.Set(ctx, sessionTokenKey("t0"), []byte(forged), gcLifetime); err != nil {
					t.Fatal(err)
				}
				for _, after := range []time.Duration{0, time.Minute} {
					e.advance(after)
					if p, ok := e.present(t, "t0"); ok || p.reused != "" || p.successor != "" {
						t.Errorf("after %v: present(%q) = %+v, %v; want nothing", after, forged, p, ok)
					}
					if e.live(t, "t0") {
						t.Errorf("after %v: %q reads as a live pointer", after, forged)
					}
				}
				if !e.sessionLive(t, session.ID) || !e.live(t, "t1") {
					t.Error("an unreadable mark ended the session")
				}
			})
		})
	}
}

// A mark moved to the present, to look younger than it is, does not open: the
// time is bound into the seal.
func TestAMarkAgedBackIntoTheWindowDoesNotOpen(t *testing.T) {
	gcEachKind(t, 0, false, func(t *testing.T, e *gcEnv) {
		ctx := context.Background()
		session := e.open(t, "t0")
		e.refresh(t, "t0", "t1")
		e.advance(time.Minute)

		mark := rawMark(t, e, "t0")
		mark.at = e.clock.now()
		if err := e.state.Set(ctx, sessionTokenKey("t0"), []byte(mark.encode()), gcLifetime); err != nil {
			t.Fatal(err)
		}
		if _, got, ok := e.refresh(t, "t0", "t-x"); ok {
			t.Errorf("a re-dated mark was answered with %q", got)
		}
		if !e.sessionLive(t, session.ID) {
			t.Error("a re-dated mark ended the session")
		}
	})
}

// -------------------------------------- 5. the successor is never in plain

const gcSecret = "SUCCESSOR-9f3b2c7a-plain-text-canary"

// everything a MemoryState holds, keys and values and set members.
func dumpMemory(m *MemoryState) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	for k, v := range m.values {
		fmt.Fprintf(&b, "%s=%s\n", k, v.value)
	}
	for k, members := range m.sets {
		for member := range members {
			fmt.Fprintf(&b, "%s+%s\n", k, member)
		}
	}
	return b.String()
}

func dumpPorts(t *testing.T, s *memory.Store) string {
	t.Helper()
	var b strings.Builder
	page := ""
	for {
		got, err := s.List(context.Background(), "", page, 0)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, r := range got.Records {
			fmt.Fprintf(&b, "%s=%s\n", r.Key, r.Value)
		}
		if got.Next == "" {
			return b.String()
		}
		page = got.Next
	}
}

func TestNoPlaintextSuccessorIsEverKeptInTheState(t *testing.T) {
	ctx := context.Background()
	b64 := base64.RawURLEncoding.EncodeToString([]byte(gcSecret))
	hasPlain := func(dump string) bool {
		return strings.Contains(dump, gcSecret) || strings.Contains(dump, b64) || strings.Contains(dump, base64.StdEncoding.EncodeToString([]byte(gcSecret)))
	}

	run := func(t *testing.T, sessions *Sessions, clock *gcClock, dump func() string, set func(key string, v []byte)) {
		session, err := sessions.Record(ctx, Opened{Identity: gcPerson, ClientID: "cli", How: HowCode, Token: "t0", AuthTime: clock.now()})
		if err != nil {
			t.Fatal(err)
		}
		if _, got, ok, err := sessions.Refreshed(ctx, "t0", gcSecret); err != nil || !ok || got != gcSecret {
			t.Fatalf("Refreshed = %q, %v, %v", got, ok, err)
		}
		if hasPlain(dump()) {
			t.Fatal("the successor is in the State in plain after a rotation")
		}

		// A replay inside the grace window, then tampered marks of every sort.
		if _, got, ok, _ := sessions.Refreshed(ctx, "t0", "t-replay"); !ok || got != gcSecret {
			t.Fatalf("replay = %q, %v", got, ok)
		}
		raw, _, _, _ := getVersion(ctx, sessions.state, sessionTokenKey("t0"))
		mark, _ := readSpent(raw)
		for _, forged := range []string{
			func() string { m := mark; m.session = "s-other"; return m.encode() }(),
			func() string { m := mark; m.at = m.at.Add(time.Second); return m.encode() }(),
			func() string { m := mark; m.sealed = m.sealed[:len(m.sealed)-2]; return m.encode() }(),
		} {
			set(sessionTokenKey("t0"), []byte(forged))
			_, _, _, _ = sessions.Refreshed(ctx, "t0", "t-forged")
			if hasPlain(dump()) {
				t.Fatalf("the successor is in the State in plain after presenting %q", forged)
			}
		}

		// Reuse after the window and the ending of the session.
		set(sessionTokenKey("t0"), raw)
		clock.advance(time.Minute)
		p, ok, _ := sessions.present(ctx, "t0")
		if ok || p.reused != session.ID {
			t.Fatalf("present = %+v, %v; want the reuse", p, ok)
		}
		if err := sessions.deleteSession(ctx, p.session); err != nil {
			t.Fatalf("deleteSession: %v", err)
		}
		if hasPlain(dump()) {
			t.Fatal("the successor is in the State in plain after the session ended")
		}
	}

	t.Run("memory-state", func(t *testing.T) {
		clock := newGCClock()
		state := NewMemoryState()
		state.SetClock(clock.now)
		sessions := NewSessions(state, gcLifetime, 0)
		sessions.SetClock(clock.now)
		run(t, sessions, clock, func() string { return dumpMemory(state) },
			func(key string, v []byte) { _ = state.Set(ctx, key, v, gcLifetime) })
	})
	t.Run("ports", func(t *testing.T) {
		clock := newGCClock()
		store := memory.New(memory.WithClock(clock.now))
		sessions := NewSessions(NewPortState(store, store), gcLifetime, 0)
		sessions.SetClock(clock.now)
		run(t, sessions, clock, func() string { return dumpPorts(t, store) },
			func(key string, v []byte) {
				if _, err := store.Put(ctx, key, v, gcLifetime); err != nil {
					t.Fatal(err)
				}
			})
	})
}

// ------------------------------------------------ 6. who can open a mark

func TestTheSealedSuccessorOpensOnlyWithTheSpentToken(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 123_000_000, time.UTC)
	raw, err := markSpent("spent-token", gcSecret, "s1", at)
	if err != nil {
		t.Fatal(err)
	}
	mark, ok := readSpent(raw)
	if !ok || mark.session != "s1" || !mark.at.Equal(at) {
		t.Fatalf("readSpent = %+v, %v", mark, ok)
	}
	if string(raw) != spentPrefix+strconv.FormatInt(at.UnixMilli(), 10)+":"+mark.sealed+":s1" {
		t.Errorf("the mark %q is not spent:<ms>:<sealed>:<session>", raw)
	}
	if got, ok := mark.successor("spent-token"); !ok || got != gcSecret {
		t.Errorf("opened with the spent token = %q, %v", got, ok)
	}
	for _, wrong := range []string{gcSecret, "another-token", "", "spent-token ", "SPENT-TOKEN"} {
		if got, ok := mark.successor(wrong); ok || got != "" {
			t.Errorf("opened with %q = %q, %v; want it sealed", wrong, got, ok)
		}
	}

	// Bound to its time and its session.
	moved := mark
	moved.session = "s2"
	if _, ok := moved.successor("spent-token"); ok {
		t.Error("the seal opened under another session id")
	}
	moved = mark
	moved.at = at.Add(time.Millisecond)
	if _, ok := moved.successor("spent-token"); ok {
		t.Error("the seal opened under another time")
	}

	// Sealed afresh each time, and not a hash of the token that keys its pointer.
	again, _ := markSpent("spent-token", gcSecret, "s1", at)
	if string(again) == string(raw) {
		t.Error("two seals of one successor are identical: the nonce is not fresh")
	}
	if strings.Contains(string(raw), gcSecret) {
		t.Error("the mark holds the successor in plain")
	}
	if strings.Contains(string(raw), strings.TrimPrefix(sessionTokenKey("spent-token"), "issuer:session-token:")) {
		t.Error("the mark carries the hash the spent token's pointer is keyed by")
	}
}

// ------------------------------------------------------ 7. the legacy key

func TestALegacyRotatedKeyAfterItsGraceIsRefusedWithoutARevocation(t *testing.T) {
	gcEachKind(t, 0, false, func(t *testing.T, e *gcEnv) {
		ctx := context.Background()
		session := e.open(t, "t0")
		if err := e.state.Delete(ctx, sessionTokenKey("t0")); err != nil {
			t.Fatal(err)
		}
		if err := e.state.Set(ctx, sessionTokenKey("t1"), []byte(session.ID), gcLifetime); err != nil {
			t.Fatal(err)
		}
		if err := e.state.Set(ctx, sessionRotatedKey("t0"), []byte("t1"), refreshGrace); err != nil {
			t.Fatal(err)
		}

		e.advance(10 * time.Second)
		if _, got, ok := e.refresh(t, "t0", "t-in"); !ok || got != "t1" {
			t.Errorf("legacy replay inside grace = %q, %v; want t1", got, ok)
		}

		e.advance(21 * time.Second)
		if _, _, ok := e.refresh(t, "t0", "t-out"); ok {
			t.Error("a legacy-rotated token was answered after its grace")
		}
		if p, ok := e.present(t, "t0"); ok || p.reused != "" {
			t.Errorf("present = %+v, %v; want a plain refusal, no reuse named", p, ok)
		}
		if !e.sessionLive(t, session.ID) || !e.live(t, "t1") {
			t.Error("a legacy token presented after its grace ended the session")
		}
	})
}

// ------------------------------------------- 8. a reuse and a rotation race

// A reuse that ends the session while a legitimate rotation of the live
// token is between its read and either of its conditional writes: the
// rotation is refused, the session stays ended, and nothing is written back.
func TestAReuseDuringARotationIsNotUndoneByIt(t *testing.T) {
	ctx := context.Background()
	for _, c := range []string{"the old pointer is spent", "the record is written"} {
		t.Run(c, func(t *testing.T) {
			gcEachKind(t, 0, true, func(t *testing.T, e *gcEnv) {
				session := e.open(t, "t0")
				e.refresh(t, "t0", "t1")
				e.advance(31 * time.Second)

				fired := false
				e.counting.before = func(op, key string) {
					want := sessionTokenKey("t1")
					if c == "the record is written" {
						want = sessionKey(session.ID)
					}
					if op == "replace" && key == want && !fired {
						fired = true
						p, ok, err := e.sessions.present(ctx, "t0")
						if err != nil || ok || p.reused != session.ID {
							t.Errorf("present(t0) = %+v, %v, %v; want the reuse", p, ok, err)
							return
						}
						if err = e.sessions.deleteSession(ctx, p.session); err != nil {
							t.Errorf("deleteSession: %v", err)
						}
					}
				}

				if _, _, ok := e.refresh(t, "t1", "t2"); ok {
					t.Error("a rotation of a session ended meanwhile was answered")
				}
				if !fired {
					t.Fatal("the conditional write was never reached, so the guard is not exercised")
				}
				if e.sessionLive(t, session.ID) {
					t.Error("the session was resurrected by the rotation")
				}
				if _, found, _ := e.state.Get(ctx, sessionKey(session.ID)); found {
					t.Error("the ended session's record was written back")
				}
				if e.live(t, "t2") || e.live(t, "t1") {
					t.Error("a token of the ended session works")
				}
				if p, a, all := listings(t, e.sessions); p+a+all != 0 {
					t.Errorf("listings = %d/%d/%d, want empty", p, a, all)
				}
			})
		})
	}
}

// The other order: the rotation read the token, then the session ended, then
// the rotation writes.
func TestARotationReadBeforeAReuseEndedTheSessionIsRefused(t *testing.T) {
	ctx := context.Background()
	gcEachKind(t, 0, true, func(t *testing.T, e *gcEnv) {
		session := e.open(t, "t0")
		e.refresh(t, "t0", "t1")
		e.advance(31 * time.Second)

		p, ok := e.present(t, "t1")
		if !ok {
			t.Fatal("the live token did not resolve")
		}
		if _, err := e.sessions.RevokeID(ctx, session.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, ok, err := e.sessions.rotate(ctx, p, "t2"); err != nil || ok {
			t.Fatalf("rotate = %v, %v; want refused", ok, err)
		}
		if e.sessionLive(t, session.ID) || e.live(t, "t2") {
			t.Error("the session came back")
		}
	})
}

// ---------------------------------------- a replica whose clock runs ahead

// A mark written by a replica whose clock is ahead of this one's is dated in
// this one's future. It is read as inside the window -- the safe side -- and
// it is a replay for as long as that replica's clock says it is, never a
// reuse that ends a session early.
func TestAMarkDatedAheadOfThisReplicaIsAReplayNotAReuse(t *testing.T) {
	gcEachKind(t, 0, false, func(t *testing.T, e *gcEnv) {
		ctx := context.Background()
		session := e.open(t, "t0")
		e.refresh(t, "t0", "t1")

		for _, ahead := range []time.Duration{time.Second, 10 * time.Second, 5 * time.Minute} {
			mark, err := markSpent("t0", "t1", session.ID, e.clock.now().Add(ahead))
			if err != nil {
				t.Fatal(err)
			}
			if err = e.state.Set(ctx, sessionTokenKey("t0"), mark, gcLifetime); err != nil {
				t.Fatal(err)
			}
			p, ok := e.present(t, "t0")
			if !ok || p.successor != "t1" || p.reused != "" {
				t.Errorf("a mark %v ahead: present = %+v, %v; want a replay answered with t1", ahead, p, ok)
			}
		}
		if !e.sessionLive(t, session.ID) {
			t.Error("a mark dated ahead ended the session")
		}
	})
}
