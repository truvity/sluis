package issuer_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// A refresh token whose chain has ended, presented again and again as a
// looping host presents it (docs/decisions/0040-agent-class-sessions.md,
// decisions 5 and 10): refused visibly and once, then from memory with no
// State read, and never at the cost of a grace-window replay or a reuse.

const deadPerson = "ada@north.example"

// deadDirectory is a directory whose answer a test can change.
type deadDirectory struct {
	mu       sync.Mutex
	standing issuer.Standing
	err      error
}

func (d *deadDirectory) set(s issuer.Standing, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.standing, d.err = s, err
}

func (d *deadDirectory) ResolveUser(_ context.Context, email string) (issuer.Standing, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if email != deadPerson {
		return issuer.Standing{}, nil
	}
	return d.standing, d.err
}

// readFailState fails every read of a key with the armed prefix.
type readFailState struct {
	*faultState
	failRead atomic.Pointer[string]
}

func (s *readFailState) failing(key string) bool {
	prefix := s.failRead.Load()
	return prefix != nil && strings.HasPrefix(key, *prefix)
}

func (s *readFailState) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if s.failing(key) {
		return nil, false, errInjected
	}
	return s.faultState.Get(ctx, key)
}

func (s *readFailState) GetVersion(ctx context.Context, key string) ([]byte, string, bool, error) {
	if s.failing(key) {
		return nil, "", false, errInjected
	}
	return s.faultState.GetVersion(ctx, key)
}

type deadRig struct {
	t       *testing.T
	iss     *issuer.Issuer
	storage *issuer.Storage
	state   *readFailState
	dir     *deadDirectory
	trail   *audittest.Recorder
	clock   *rigClock
	logs    *recordingHandler
	server  *httptest.Server
}

// newDeadRig is the issuer behind its HTTP surface, over a State that
// counts its calls, with the person live and in the group local-dev
// requires. absolute is the installation's absolute limit (0: the default).
func newDeadRig(t *testing.T, absolute time.Duration) *deadRig {
	t.Helper()
	metricsReader()

	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("policy set: %v", err)
	}

	clock := &rigClock{t: time.Now().UTC()}
	inner := issuer.NewMemoryState()
	inner.SetClock(clock.now)
	state := &readFailState{faultState: &faultState{MemoryState: inner}}

	dir := &deadDirectory{}
	dir.set(issuer.Standing{Found: true, Authoritative: true, Groups: []string{"engineering@north.example"}}, nil)

	iss := issuer.New(issuer.Config{
		URL: "http://issuer.example", AllowInsecure: true, AbsoluteLifetime: absolute,
	}, set, dir, state)
	iss.Sessions().SetClock(clock.now)

	trail := audittest.New(t)
	iss.UseAudit(trail)

	storage, err := issuer.NewStorage(iss, fakeVerifier{}, nil, nil, nil, state)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	logs := &recordingHandler{}
	storage.UseLog(slog.New(logs))

	h, err := handler(iss, storage)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)

	return &deadRig{t: t, iss: iss, storage: storage, state: state, dir: dir, trail: trail, clock: clock, logs: logs, server: server}
}

// open records a session of local-dev for the person under token.
func (r *deadRig) open(token string) issuer.Session {
	r.t.Helper()
	session, err := r.iss.Sessions().Record(context.Background(), issuer.Opened{
		Identity: deadPerson, ClientID: "local-dev", How: issuer.HowCode, Token: token,
		Scopes: []string{"openid", "profile", "email"}, AuthTime: r.clock.now(),
	})
	if err != nil {
		r.t.Fatalf("record the session: %v", err)
	}
	return session
}

// answer is what the token endpoint said to one refresh.
type answer struct {
	status  int
	refresh string
	err     string
}

// refresh posts one refresh_token grant for local-dev.
func (r *deadRig) refresh(token string) answer {
	r.t.Helper()
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {token},
		"client_id":     {"local-dev"},
	}
	request, err := http.NewRequestWithContext(r.t.Context(), http.MethodPost,
		r.server.URL+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		r.t.Fatalf("build the request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := r.server.Client().Do(request)
	if err != nil {
		r.t.Fatalf("post the refresh: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	var body struct {
		Refresh string `json:"refresh_token"`
		Error   string `json:"error"`
	}
	_ = json.NewDecoder(response.Body).Decode(&body)
	return answer{status: response.StatusCode, refresh: body.Refresh, err: body.Error}
}

// lookup is the library's first reading of a refresh token, called as the
// library calls it, and its error as the storage returned it. Over HTTP the
// library answers every error from it as invalid_grant, keeping this one as
// the parent, so the server_error a refusal that ends nothing returns is
// told apart here.
func (r *deadRig) lookup(token string) error {
	r.t.Helper()
	_, err := r.storage.TokenRequestByRefreshToken(r.t.Context(), token)
	return err
}

func oidcErrorType(err error) string {
	var oe *oidc.Error
	if errors.As(err, &oe) {
		return string(oe.ErrorType)
	}
	return ""
}

// cost is one refresh and the State reads and writes it made.
func (r *deadRig) cost(token string) (answer, int64, int64) {
	r.t.Helper()
	reads, writes := r.state.reads.Load(), r.state.writes.Load()
	got := r.refresh(token)
	return got, r.state.reads.Load() - reads, r.state.writes.Load() - writes
}

func (r *deadRig) wantRefused(got answer, what string) {
	r.t.Helper()
	if got.status == http.StatusOK || got.err != "invalid_grant" {
		r.t.Fatalf("%s: %d %q, want invalid_grant", what, got.status, got.err)
	}
}

// deadWarnings are the WARN lines a dead refresh token wrote, as attrs.
func (r *deadRig) deadWarnings() []map[string]string {
	r.logs.mu.Lock()
	defer r.logs.mu.Unlock()
	var out []map[string]string
	for i := range r.logs.records {
		rec := &r.logs.records[i]
		if rec.Level != slog.LevelWarn || rec.Message != "refused a refresh token that names no live session" {
			continue
		}
		attrs := map[string]string{}
		rec.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.String()
			return true
		})
		out = append(out, attrs)
	}
	return out
}

// everythingLogged is every line the storage wrote, message and attrs.
func (r *deadRig) everythingLogged() string {
	r.logs.mu.Lock()
	defer r.logs.mu.Unlock()
	var b strings.Builder
	for i := range r.logs.records {
		rec := &r.logs.records[i]
		b.WriteString(rec.Message)
		rec.Attrs(func(a slog.Attr) bool {
			b.WriteString(" " + a.Key + "=" + a.Value.String())
			return true
		})
		b.WriteString("\n")
	}
	return b.String()
}

// The second identical dead refresh makes no State call at all, for each
// terminal state: a token never issued (no pointer), a token whose session
// was ended (a pointer naming an absent record), and a token spent past
// its grace window in a session that has since ended.
func TestASecondDeadRefreshMakesNoStateCall(t *testing.T) {
	t.Parallel()

	for name, deadToken := range map[string]func(r *deadRig) string{
		"never issued": func(*deadRig) string { return "never-issued-refresh-token" },
		"session ended": func(r *deadRig) string {
			session := r.open("ended-0")
			if ended, err := r.iss.Sessions().RevokeID(context.Background(), session.ID); err != nil || !ended {
				t.Fatalf("RevokeID = %v, %v", ended, err)
			}
			return "ended-0"
		},
		"spent in an ended session": func(r *deadRig) string {
			session := r.open("spent-0")
			if got := r.refresh("spent-0"); got.status != http.StatusOK {
				t.Fatalf("refresh: %d %q", got.status, got.err)
			}
			if ended, err := r.iss.Sessions().RevokeID(context.Background(), session.ID); err != nil || !ended {
				t.Fatalf("RevokeID = %v, %v", ended, err)
			}
			r.clock.advance(time.Minute)
			return "spent-0"
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newDeadRig(t, 0)
			token := deadToken(r)

			first, reads, _ := r.cost(token)
			r.wantRefused(first, "the first presentation")
			if reads == 0 {
				t.Fatal("the first presentation read nothing; it cannot have known the token was dead")
			}

			hits := counted(t, "access_issuer.dead_refresh_token_hits")
			second, reads, writes := r.cost(token)
			r.wantRefused(second, "the second presentation")
			if reads != 0 || writes != 0 {
				t.Errorf("the second presentation made %d State reads and %d writes, want none", reads, writes)
			}
			if got := counted(t, "access_issuer.dead_refresh_token_hits") - hits; got < 1 {
				t.Errorf("access_issuer.dead_refresh_token_hits rose by %d, want the hit counted", got)
			}
		})
	}
}

// The refusal is visible: one WARN per cache entry, naming the client the
// request named and a fingerprint of the token, never the token itself.
// Once the entry has expired, the next presentation reads again and warns
// again.
func TestADeadRefreshWarnsOnceWithTheClientAndAFingerprint(t *testing.T) {
	t.Parallel()
	r := newDeadRig(t, 0)
	const token = "a-dead-refresh-token-0123456789abcdef"

	for range 3 {
		r.wantRefused(r.refresh(token), "a dead token")
	}

	warnings := r.deadWarnings()
	if len(warnings) != 1 {
		t.Fatalf("%d WARN lines for three presentations, want 1:\n%s", len(warnings), r.everythingLogged())
	}
	if got := warnings[0]["client_id"]; got != "local-dev" {
		t.Errorf("client_id = %q, want local-dev", got)
	}
	if got := warnings[0]["token_fingerprint"]; !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(got) {
		t.Errorf("token_fingerprint = %q, want 8 hex", got)
	}
	logged := r.everythingLogged()
	for _, part := range []string{token, token[:16], token[len(token)-16:]} {
		if strings.Contains(logged, part) {
			t.Fatalf("the log carries the token (%q):\n%s", part, logged)
		}
	}

	// Another dead token is another entry, with another fingerprint.
	r.wantRefused(r.refresh("another-dead-refresh-token"), "another dead token")
	if warnings = r.deadWarnings(); len(warnings) != 2 || warnings[0]["token_fingerprint"] == warnings[1]["token_fingerprint"] {
		t.Fatalf("two dead tokens wrote %v, want two lines with two fingerprints", warnings)
	}

	// Past the fixed five minutes the entry is gone: read again, warned again.
	r.clock.advance(5*time.Minute + time.Second)
	got, reads, _ := r.cost(token)
	r.wantRefused(got, "a dead token after the TTL")
	if reads == 0 {
		t.Error("a dead token was still refused from memory after the TTL")
	}
	if warnings = r.deadWarnings(); len(warnings) != 3 {
		t.Errorf("%d WARN lines after the TTL, want a third for the new entry", len(warnings))
	}
}

// A replay inside the grace window is answered with the successor every
// time, and nothing about it is remembered as dead.
func TestAGraceReplayIsAnsweredAndNeverCached(t *testing.T) {
	t.Parallel()
	r := newDeadRig(t, 0)
	r.open("grace-0")

	rotated := r.refresh("grace-0")
	if rotated.status != http.StatusOK || rotated.refresh == "" {
		t.Fatalf("refresh: %d %q", rotated.status, rotated.err)
	}
	for i := range 3 {
		r.clock.advance(5 * time.Second)
		replay, reads, _ := r.cost("grace-0")
		if replay.status != http.StatusOK || replay.refresh != rotated.refresh {
			t.Fatalf("replay %d in the grace window: %d %q, same successor=%v; want 200 and the successor",
				i, replay.status, replay.err, replay.refresh == rotated.refresh)
		}
		if reads == 0 {
			t.Fatalf("replay %d was answered without reading the token", i)
		}
	}
	if warnings := r.deadWarnings(); len(warnings) != 0 {
		t.Errorf("a grace-window replay was logged as a dead token: %v", warnings)
	}
}

// A spent token refused in the tolerance band past the grace window is not
// remembered: presented again once past the band, in a session that is
// still live, it is a reuse and ends the session, exactly once.
func TestAReuseStillEndsTheSessionAfterARefusalInTheBand(t *testing.T) {
	t.Parallel()
	r := newDeadRig(t, 0)
	r.open("band-0")

	rotated := r.refresh("band-0")
	if rotated.status != http.StatusOK {
		t.Fatalf("refresh: %d %q", rotated.status, rotated.err)
	}

	r.clock.advance(31 * time.Second) // past the grace window, inside the 2 s band
	r.wantRefused(r.refresh("band-0"), "a spent token in the band")
	if n := len(r.trail.Find("roster.session.revoked")); n != 0 {
		t.Fatalf("a refusal in the band ended the session (%d revoked records)", n)
	}

	r.clock.advance(2 * time.Second) // past the band
	r.wantRefused(r.refresh("band-0"), "a reuse past the band")
	if n := len(r.trail.Find("roster.session.revoked")); n != 1 {
		t.Fatalf("a reuse past the band recorded %d revocations, want 1: %v", n, r.trail.Actions())
	}
	if after := r.refresh(rotated.refresh); after.status == http.StatusOK {
		t.Error("the successor still refreshes; the reuse did not end the session")
	}

	// The session is gone now, so the spent token is dead, and only now
	// remembered.
	_, reads, _ := r.cost("band-0")
	if reads == 0 {
		t.Error("the first presentation after the reuse ended the session was refused from memory")
	}
	if _, reads, _ = r.cost("band-0"); reads != 0 {
		t.Errorf("a spent token in an ended session cost %d reads on its second presentation, want 0", reads)
	}
	if n := len(r.trail.Find("roster.session.revoked")); n != 1 {
		t.Errorf("%d revocations recorded, want still 1", n)
	}
}

// A read that fails is a server error and is not remembered: the next
// presentation reads again.
func TestAFailedReadIsNotCached(t *testing.T) {
	t.Parallel()

	for _, prefix := range []string{"issuer:session-token:", "issuer:session-rotated:", "issuer:session:"} {
		t.Run(prefix, func(t *testing.T) {
			t.Parallel()
			r := newDeadRig(t, 0)
			session := r.open("fail-0")
			if ended, err := r.iss.Sessions().RevokeID(context.Background(), session.ID); err != nil || !ended {
				t.Fatalf("RevokeID = %v, %v", ended, err)
			}
			token := "fail-0"
			if prefix == "issuer:session-rotated:" {
				token = "never-issued" // the rotated key is read only when there is no pointer
			}

			r.state.failRead.Store(&prefix)
			for range 2 {
				if err := r.lookup(token); oidcErrorType(err) != string(oidc.ServerError) {
					t.Fatalf("a failed read answered %v, want server_error", err)
				}
			}
			r.state.failRead.Store(nil)

			got, reads, _ := r.cost(token)
			r.wantRefused(got, "after the store recovered")
			if reads == 0 {
				t.Fatal("a token whose read failed was refused from memory once the store answered")
			}
			if _, reads, _ = r.cost(token); reads != 0 {
				t.Errorf("once read, the dead token cost %d reads again, want 0", reads)
			}
		})
	}
}

// A record that is still stored but past its end is never remembered: a
// slow rotation on another replica could still write it. Here the
// absolute-limit refusal cannot delete it, so it stays stored past its end,
// and every presentation reads it again.
func TestARecordPastItsEndIsNotCached(t *testing.T) {
	t.Parallel()
	r := newDeadRig(t, 10*time.Minute)
	r.open("past-0")
	r.clock.advance(11 * time.Minute) // past the limit; the record is kept for the refresh window
	r.state.arm("issuer:session:", "")

	for i := range 3 {
		got, reads, _ := r.cost("past-0")
		r.wantRefused(got, "a refresh of a chain past its limit")
		if reads == 0 {
			t.Fatalf("presentation %d of a token whose record is past its end was refused from memory", i)
		}
	}
	if warnings := r.deadWarnings(); len(warnings) != 0 {
		t.Errorf("a refusal at the absolute limit was logged as a dead token: %v", warnings)
	}
}

// A refresh at the absolute limit is refused, ends the session and is
// audited once. It is not remembered: the presentation after it reads, finds
// the record gone and only then remembers the token.
func TestTheAbsoluteLimitRefusalStillAuditsOnce(t *testing.T) {
	t.Parallel()
	r := newDeadRig(t, 10*time.Minute)
	r.open("limit-0")
	r.clock.advance(11 * time.Minute)

	r.wantRefused(r.refresh("limit-0"), "a refresh past the absolute limit")
	got, reads, _ := r.cost("limit-0")
	r.wantRefused(got, "the next presentation")
	if reads == 0 {
		t.Error("the absolute-limit refusal was remembered; it must do its work first")
	}
	got, reads, _ = r.cost("limit-0")
	r.wantRefused(got, "the presentation after")
	if reads != 0 {
		t.Errorf("a token whose chain ended at the limit cost %d reads once remembered, want 0", reads)
	}
	if n := len(r.trail.Find("roster.session.refresh_refused")); n != 1 {
		t.Errorf("%d roster.session.refresh_refused records, want 1: %v", n, r.trail.Actions())
	}
}

// An authoritative "not live" at refresh ends the session: the record, its
// listing and the token's pointer go, roster.session.refresh_refused is
// written, and the client is told invalid_grant.
func TestAnAuthoritativeNotLiveAtRefreshEndsTheSession(t *testing.T) {
	t.Parallel()

	for name, standing := range map[string]issuer.Standing{
		"not found": {Found: false, Authoritative: true},
		"suspended": {Found: true, Suspended: true, Authoritative: true, Groups: []string{"engineering@north.example"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newDeadRig(t, 0)
			r.open("removed-0")
			rotated := r.refresh("removed-0")
			if rotated.status != http.StatusOK {
				t.Fatalf("refresh: %d %q", rotated.status, rotated.err)
			}

			r.dir.set(standing, nil)
			if err := r.lookup(rotated.refresh); oidcErrorType(err) != string(oidc.InvalidGrant) {
				t.Fatalf("a refresh for a person the directory no longer has: %v, want invalid_grant", err)
			}

			if listed, err := r.iss.Sessions().List(context.Background(), issuer.Query{Identity: deadPerson}); err != nil || len(listed) != 0 {
				t.Errorf("List = %d sessions, %v; want the session ended", len(listed), err)
			}
			if _, found, err := r.state.Get(context.Background(), issuer.SessionTokenKeyForTest(rotated.refresh)); err != nil || found {
				t.Errorf("the token's pointer is still stored (found=%v, err=%v)", found, err)
			}
			refused := r.trail.Find("roster.session.refresh_refused")
			if len(refused) != 1 {
				t.Fatalf("%d roster.session.refresh_refused records, want 1: %v", len(refused), r.trail.Actions())
			}
			if !strings.Contains(refused[0].String(), "the directory says this account is not live") {
				t.Errorf("the record does not give the directory's reason: %v", refused[0])
			}

			// The person comes back: the chain stays ended.
			r.dir.set(issuer.Standing{Found: true, Authoritative: true, Groups: []string{"engineering@north.example"}}, nil)
			r.wantRefused(r.refresh(rotated.refresh), "the ended chain after the person came back")
		})
	}
}

// A refusal the directory cannot vouch for is an outage, not a removal:
// server_error, and nothing ended or audited.
func TestANonAuthoritativeRefusalAtRefreshEndsNothing(t *testing.T) {
	t.Parallel()

	for name, answer := range map[string]struct {
		standing issuer.Standing
		err      error
	}{
		"cannot vouch, nothing held": {standing: issuer.Standing{Found: true, Groups: []string{"engineering@north.example"}}},
		"unreachable":                {err: errors.New("the hub is down")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newDeadRig(t, 0)
			// Recorded, never resolved: nothing is held for the person.
			r.open("outage-0")

			r.dir.set(answer.standing, answer.err)
			if err := r.lookup("outage-0"); oidcErrorType(err) != string(oidc.ServerError) {
				t.Fatalf("a refresh in an outage: %v, want server_error", err)
			}
			if listed, err := r.iss.Sessions().List(context.Background(), issuer.Query{Identity: deadPerson}); err != nil || len(listed) != 1 {
				t.Errorf("List = %d sessions, %v; an outage ended the session", len(listed), err)
			}
			if n := len(r.trail.Find("roster.session.refresh_refused")); n != 0 {
				t.Errorf("an outage was audited as a refused refresh: %v", r.trail.Actions())
			}

			r.dir.set(issuer.Standing{Found: true, Authoritative: true, Groups: []string{"engineering@north.example"}}, nil)
			if got := r.refresh("outage-0"); got.status != http.StatusOK {
				t.Fatalf("after the outage: %d %q, want the chain to carry on", got.status, got.err)
			}
		})
	}
}
