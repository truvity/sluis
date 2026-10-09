package issuer_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"
	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
	"go.opentelemetry.io/otel/attribute"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// A refresh token presented again after its grace window ends its session,
// seen from the storage the OpenID library calls: the audit record, the log
// line, the metric, the access tokens of the session, and what a failure to
// end it does.

const reusePerson = "ada@north.example"

type rigClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *rigClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *rigClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// faultState is a State that counts what is written and read, and can fail
// or intercept a call. It keeps the revision-aware calls of the MemoryState
// it wraps, as the issuer's rotation needs them.
type faultState struct {
	*issuer.MemoryState
	writes, reads atomic.Int64

	mu sync.Mutex
	// failDelete and failRemove fail the deletion of a key with that prefix
	// or the removal from a set with it, while set.
	failDelete, failRemove string
	// beforeReplace runs once before a Replace of a key with this prefix.
	replacePrefix string
	beforeReplace func()
	// beforeDelete runs before each conditional delete of a session record
	// while deleteHooks is above zero, counting it down.
	beforeDelete func()
	deleteHooks  int
	// ghost, when set, makes a conditional delete of a session record apply
	// and then report this error: a delete that committed but says it failed.
	ghost error
}

var errInjected = errors.New("injected State failure")

func (s *faultState) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	s.writes.Add(1)
	return s.MemoryState.Set(ctx, key, value, ttl)
}

func (s *faultState) SetIfAbsent(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	s.writes.Add(1)
	return s.MemoryState.SetIfAbsent(ctx, key, value, ttl)
}

func (s *faultState) SetVersion(ctx context.Context, key string, value []byte, ttl time.Duration) (string, error) {
	s.writes.Add(1)
	return s.MemoryState.SetVersion(ctx, key, value, ttl)
}

func (s *faultState) Add(ctx context.Context, key, member string, ttl time.Duration) error {
	s.writes.Add(1)
	return s.MemoryState.Add(ctx, key, member, ttl)
}

func (s *faultState) Get(ctx context.Context, key string) ([]byte, bool, error) {
	s.reads.Add(1)
	return s.MemoryState.Get(ctx, key)
}

func (s *faultState) GetVersion(ctx context.Context, key string) ([]byte, string, bool, error) {
	s.reads.Add(1)
	return s.MemoryState.GetVersion(ctx, key)
}

func (s *faultState) Members(ctx context.Context, key string) ([]string, error) {
	s.reads.Add(1)
	return s.MemoryState.Members(ctx, key)
}

func (s *faultState) Delete(ctx context.Context, key string) error {
	s.writes.Add(1)
	s.mu.Lock()
	fail := s.failDelete != "" && strings.HasPrefix(key, s.failDelete)
	s.mu.Unlock()
	if fail {
		return errInjected
	}
	return s.MemoryState.Delete(ctx, key)
}

// DeleteVersion is the conditional delete that ends a session: it fails while
// failDelete names the key, and counts a write only when it succeeds.
func (s *faultState) DeleteVersion(ctx context.Context, key, version string) error {
	s.mu.Lock()
	fail := s.failDelete != "" && strings.HasPrefix(key, s.failDelete)
	hook, ghost := s.beforeDelete, s.ghost
	if s.deleteHooks > 0 && strings.HasPrefix(key, "issuer:session:") {
		s.deleteHooks--
	} else {
		hook = nil
	}
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	if ghost != nil && strings.HasPrefix(key, "issuer:session:") {
		err := s.MemoryState.DeleteVersion(ctx, key, version)
		if err == nil {
			s.writes.Add(1)
			return ghost
		}
		return err
	}
	if fail {
		return errInjected
	}
	err := s.MemoryState.DeleteVersion(ctx, key, version)
	if err == nil {
		s.writes.Add(1)
	}
	return err
}

func (s *faultState) Remove(ctx context.Context, key, member string) error {
	s.writes.Add(1)
	s.mu.Lock()
	fail := s.failRemove != "" && strings.HasPrefix(key, s.failRemove)
	s.mu.Unlock()
	if fail {
		return errInjected
	}
	return s.MemoryState.Remove(ctx, key, member)
}

func (s *faultState) Replace(ctx context.Context, key string, value []byte, ttl time.Duration, version string) error {
	s.writes.Add(1)
	s.mu.Lock()
	hook := s.beforeReplace
	if hook != nil && strings.HasPrefix(key, s.replacePrefix) {
		s.beforeReplace = nil
	} else {
		hook = nil
	}
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	return s.MemoryState.Replace(ctx, key, value, ttl, version)
}

func (s *faultState) arm(failDelete, failRemove string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failDelete, s.failRemove = failDelete, failRemove
}

// reuseDirectory is a directory that is safe to ask from many goroutines.
type reuseDirectory map[string]issuer.Standing

func (d reuseDirectory) ResolveUser(_ context.Context, email string) (issuer.Standing, error) {
	return d[email], nil
}

type reuseRig struct {
	t       *testing.T
	iss     *issuer.Issuer
	storage *issuer.Storage
	state   *faultState
	trail   *audittest.Recorder
	clock   *rigClock
	logs    *recordingHandler
}

func newReuseRig(t *testing.T, absolute time.Duration) *reuseRig {
	t.Helper()
	return newReuseRigWith(t, absolute, "")
}

// newReuseRigWith is the rig with argocd asking for back-channel logout at
// listener, when one is given.
func newReuseRigWith(t *testing.T, absolute time.Duration, listener string) *reuseRig {
	t.Helper()
	metricsReader()

	text := demo.Policy
	if listener != "" {
		text = strings.Replace(text, "    ttl_cap: 2h", "    ttl_cap: 2h\n    backchannel_logout_uri: "+listener+"/backchannel", 1)
	}
	declared, err := policy.Parse([]byte(text))
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
	state := &faultState{MemoryState: inner}

	iss := issuer.New(issuer.Config{URL: "https://issuer.example", AbsoluteLifetime: absolute}, set, reuseDirectory{
		reusePerson: {Found: true, Authoritative: true, Groups: []string{"directory-admins@north.example"}},
	}, state)
	iss.Sessions().SetClock(clock.now)

	trail := audittest.New(t)
	iss.UseAudit(trail)

	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, state)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	logs := &recordingHandler{}
	storage.UseLog(slog.New(logs))

	return &reuseRig{t: t, iss: iss, storage: storage, state: state, trail: trail, clock: clock, logs: logs}
}

// grant is a sign-in and the redemption of its code: the access token and the
// refresh token a client starts with.
func (r *reuseRig) grant() (access, refresh string) {
	r.t.Helper()
	ctx := context.Background()

	id, err := r.storage.CreateAuthRequestForTest(ctx, reusePerson, "argocd")
	if err != nil {
		r.t.Fatalf("create request: %v", err)
	}
	if err = r.storage.SaveAuthCode(ctx, id, "the-code"); err != nil {
		r.t.Fatalf("SaveAuthCode: %v", err)
	}
	request, err := r.storage.AuthRequestByCode(ctx, "the-code")
	if err != nil {
		r.t.Fatalf("redeem: %v", err)
	}
	access, refresh, _, err = r.storage.CreateAccessAndRefreshTokens(ctx, request, "")
	if err != nil {
		r.t.Fatalf("CreateAccessAndRefreshTokens: %v", err)
	}
	return access, refresh
}

// refresh is the library's refresh grant: resolve the token, then rotate it.
func (r *reuseRig) refresh(old string) (access, next string, err error) {
	r.t.Helper()
	ctx := context.Background()
	request, err := r.storage.TokenRequestByRefreshToken(ctx, old)
	if err != nil {
		return "", "", err
	}
	access, next, _, err = r.storage.CreateAccessAndRefreshTokens(ctx, request, old)
	return access, next, err
}

func (r *reuseRig) sessions() []issuer.Session {
	r.t.Helper()
	var first []issuer.Session
	for i, q := range []issuer.Query{{Identity: reusePerson}, {ClientID: "argocd"}, {}} {
		got, err := r.iss.Sessions().List(context.Background(), q)
		if err != nil {
			r.t.Fatalf("List(%+v): %v", q, err)
		}
		if i == 0 {
			first = got
		} else if len(got) != len(first) {
			r.t.Fatalf("the listings disagree: %+v has %d, the first has %d", q, len(got), len(first))
		}
	}
	return first
}

func (r *reuseRig) userinfo(access string) error {
	return r.storage.SetUserinfoFromToken(context.Background(), &oidc.UserInfo{}, access, "", "")
}

// revoked is the roster.session.revoked records so far: the sign-in's own
// record is not what these tests are about.
func (r *reuseRig) revoked() []*record.Record {
	return r.trail.Find("roster.session.revoked")
}

// wantInvalidGrant is a refusal the client reads as invalid_grant: the
// library's invalid refresh token, or an invalid_grant of the issuer's own.
func wantInvalidGrant(t *testing.T, err error) {
	t.Helper()
	var oe *oidc.Error
	if errors.Is(err, op.ErrInvalidRefreshToken) || (errors.As(err, &oe) && oe.ErrorType == oidc.InvalidGrant) {
		return
	}
	t.Fatalf("err = %v, want a refusal that reads as invalid_grant", err)
}

func isInvalidGrant(err error) bool {
	var oe *oidc.Error
	return errors.Is(err, op.ErrInvalidRefreshToken) || (errors.As(err, &oe) && oe.ErrorType == oidc.InvalidGrant)
}

func wantServerError(t *testing.T, err error) {
	t.Helper()
	var oe *oidc.Error
	if !errors.As(err, &oe) || oe.ErrorType != oidc.ServerError {
		t.Fatalf("err = %v, want server_error", err)
	}
	if errors.Is(err, op.ErrInvalidRefreshToken) {
		t.Fatalf("err = %v is invalid_grant, want server_error so that the client retries", err)
	}
}

func (r *reuseRig) warnings(contains string) int {
	r.logs.mu.Lock()
	defer r.logs.mu.Unlock()
	n := 0
	for i := range r.logs.records {
		if r.logs.records[i].Level == slog.LevelWarn && strings.Contains(r.logs.records[i].Message, contains) {
			n++
		}
	}
	return n
}

// ------------------------------------------------------------- the window

func TestAReplayAt29SecondsIsAnsweredTheBandTo32SecondsEndsNothingAndAReuseAt33SecondsEndsTheSession(t *testing.T) {
	t.Run("29 seconds", func(t *testing.T) {
		r := newReuseRig(t, 0)
		_, t0 := r.grant()
		_, t1, err := r.refresh(t0)
		if err != nil {
			t.Fatal(err)
		}
		r.clock.advance(29 * time.Second)
		_, replayed, err := r.refresh(t0)
		if err != nil || replayed != t1 {
			t.Fatalf("replay = %q, %v; want %q", replayed, err, t1)
		}
		if len(r.sessions()) != 1 || len(r.revoked()) != 0 || r.warnings("reused") != 0 {
			t.Error("a replay inside the window ended the session, was audited or was logged as a reuse")
		}
		if _, _, err = r.refresh(t1); err != nil {
			t.Errorf("the successor stopped working after a replay: %v", err)
		}
	})
	t.Run("31 seconds", func(t *testing.T) {
		r := newReuseRig(t, 0)
		_, t0 := r.grant()
		_, t1, err := r.refresh(t0)
		if err != nil {
			t.Fatal(err)
		}
		r.clock.advance(33 * time.Second)
		_, _, err = r.refresh(t0)
		wantInvalidGrant(t, err)
		if len(r.sessions()) != 0 {
			t.Error("the session is still listed after a reuse")
		}
		if _, _, err = r.refresh(t1); err == nil {
			t.Error("the successor still refreshes after its predecessor was reused")
		}
	})
}

// ----------------------------------------------------- the record of a reuse

func TestAReuseIsAuditedLoggedCountedAndKillsTheSessionsTokens(t *testing.T) {
	r := newReuseRig(t, 0)
	reuse := []attribute.KeyValue{attribute.String("kind", "refresh_token")}
	before := counted(t, "access_issuer.reuse_detected", reuse...)

	access0, t0 := r.grant()
	access1, t1, err := r.refresh(t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{access0, access1} {
		if err = r.userinfo(a); err != nil {
			t.Fatalf("userinfo before the reuse: %v", err)
		}
	}
	r.clock.advance(33 * time.Second)

	_, _, err = r.refresh(t0)
	wantInvalidGrant(t, err)

	// The audit record: one, exactly as the issuer's vocabulary spells it.
	written := r.revoked()
	if len(written) != 1 {
		t.Fatalf("recorded %v, want one roster.session.revoked", r.trail.Actions())
	}
	rec := written[0]
	if rec.GetAction() != "roster.session.revoked" {
		t.Errorf("action = %q", rec.GetAction())
	}
	if rec.GetActor().GetKind() != "system" || rec.GetActor().GetId() != "" {
		t.Errorf("actor = %v, want the system", rec.GetActor())
	}
	if rec.GetSubject().GetKind() != "person" || rec.GetSubject().GetId() != reusePerson {
		t.Errorf("subject = %v, want the person %s", rec.GetSubject(), reusePerson)
	}
	if got := rec.GetTargets(); len(got) != 1 || got[0].GetType() != "client" || got[0].GetId() != "argocd" {
		t.Errorf("targets = %v, want the client argocd", got)
	}
	if got := rec.GetData().GetFields()["scope"].GetStringValue(); got != "refresh_token_reuse" || audit.ScopeRefreshTokenReuse != "refresh_token_reuse" {
		t.Errorf("scope = %q, want refresh_token_reuse", got)
	}
	if got := rec.GetData().GetFields()["ended"].GetNumberValue(); got != 1 {
		t.Errorf("ended = %v, want 1", got)
	}
	if len(rec.GetData().GetFields()) != 2 {
		t.Errorf("data = %v, want scope and ended only for a session opened under no browser sign-in", rec.GetData())
	}
	if rec.GetOutcome().GetResult() != auditv1.Outcome_RESULT_SUCCESS || rec.GetOutcome().GetReason() != "" {
		t.Errorf("outcome = %v, want success", rec.GetOutcome())
	}
	for _, secret := range []string{t0, t1, access0, access1} {
		if strings.Contains(rec.String(), secret) {
			t.Error("the audit record carries a credential")
		}
	}

	// The WARN line and the metric.
	if r.warnings("reused") != 1 {
		t.Errorf("WARN lines about a reuse = %d, want 1", r.warnings("reused"))
	}
	r.logs.mu.Lock()
	for i := range r.logs.records {
		r.logs.records[i].Attrs(func(a slog.Attr) bool {
			if v := a.Value.String(); strings.Contains(v, t0) || strings.Contains(v, t1) {
				t.Errorf("the log line carries a token in %s", a.Key)
			}
			return true
		})
	}
	r.logs.mu.Unlock()
	if got := counted(t, "access_issuer.reuse_detected", reuse...); got < before+1 {
		t.Errorf("reuse metric = %d, want at least %d", got, before+1)
	}

	// The session is gone and every token of it dead.
	if got := r.sessions(); len(got) != 0 {
		t.Errorf("sessions listed = %d, want none", len(got))
	}
	if _, _, err = r.refresh(t1); err == nil {
		t.Error("the successor still refreshes")
	}
	for name, a := range map[string]string{"first access token": access0, "rotated access token": access1} {
		if err = r.userinfo(a); err == nil {
			t.Errorf("userinfo answered the %s of an ended session", name)
		}
	}
	// Introspection is not served at all, for any token.
	if err = r.storage.SetIntrospectionFromToken(context.Background(), &oidc.IntrospectionResponse{}, access1, "", ""); err == nil {
		t.Error("introspection answered for the access token of an ended session")
	}
}

// Nothing more happens the second time, or the tenth: a refused answer, no
// further audit record, no further write.
func TestRepeatedReuseAfterTheSessionEndedWritesAndAuditsNothing(t *testing.T) {
	r := newReuseRig(t, 0)
	_, t0 := r.grant()
	_, t1, err := r.refresh(t0)
	if err != nil {
		t.Fatal(err)
	}
	r.clock.advance(33 * time.Second)
	_, _, err = r.refresh(t0)
	wantInvalidGrant(t, err)
	if len(r.revoked()) != 1 {
		t.Fatalf("recorded %v after the first reuse", r.trail.Actions())
	}
	records := len(r.trail.Records())
	warned := r.warnings("reused")

	for i := range 5 {
		for _, spent := range []string{t0, t1} {
			writes := r.state.writes.Load()
			_, _, err = r.refresh(spent)
			wantInvalidGrant(t, err)
			if got := r.state.writes.Load(); got != writes {
				t.Fatalf("presentation %d of %q: %d writes after the session ended, want none", i, spent[:4], got-writes)
			}
		}
	}
	if got := len(r.trail.Records()); got != records {
		t.Errorf("recorded %v, want nothing after the first reuse", r.trail.Actions())
	}
	if got := r.warnings("reused"); got != warned {
		t.Errorf("WARN lines about a reuse grew from %d to %d for a session that was already ended", warned, got)
	}
}

// ------------------------------------------------- a failure to end the session

// The session record is deleted first, conditionally on the revision read.
// When that fails the session is untouched, nothing is audited (the end did
// not happen), the grant is a server error so that the client retries, and
// the retry meets the reuse again and ends the session, audited once.
func TestAFailureToEndTheSessionIsAServerErrorWithNothingRecordedAndTheRetryEndsIt(t *testing.T) {
	r := newReuseRig(t, 0)
	access0, t0 := r.grant()
	access1, t1, err := r.refresh(t0)
	if err != nil {
		t.Fatal(err)
	}
	r.clock.advance(33 * time.Second)

	r.state.arm("issuer:session:", "")
	_, _, err = r.refresh(t0)
	wantServerError(t, err)
	if r.warnings("could not be ended") != 1 {
		t.Errorf("WARN lines about the failure = %d, want 1", r.warnings("could not be ended"))
	}
	if strings.Contains(strings.Join(r.warningAttrs(), " "), t0) {
		t.Error("the failure line carries the token")
	}
	if got := r.revoked(); len(got) != 0 {
		t.Errorf("recorded %d revocations for an end that did not happen", len(got))
	}
	if got := r.sessions(); len(got) != 1 {
		t.Errorf("sessions listed = %d after a failed end, want the session untouched", len(got))
	}
	if err = r.userinfo(access1); err != nil {
		t.Errorf("a failed end already refused the session's access token: %v", err)
	}

	// The client retries: the reuse is met again and the session ends.
	r.state.arm("", "")
	_, _, err = r.refresh(t0)
	wantInvalidGrant(t, err)
	if len(r.sessions()) != 0 {
		t.Error("the retry did not end the session")
	}
	if _, _, err = r.refresh(t1); err == nil {
		t.Error("the successor refreshes after the retry")
	}
	for _, a := range []string{access0, access1} {
		if err = r.userinfo(a); err == nil {
			t.Error("userinfo answers for the ended session")
		}
	}
	if got := r.revoked(); len(got) != 1 {
		t.Errorf("recorded %d revocations after the failure and its retry, want exactly 1", len(got))
	}

	// Further presentations end nothing more and say nothing more.
	after := len(r.trail.Records())
	_, _, err = r.refresh(t0)
	wantInvalidGrant(t, err)
	if len(r.trail.Records()) != after {
		t.Error("a presentation after the session ended was audited")
	}
}

func (r *reuseRig) warningAttrs() []string {
	r.logs.mu.Lock()
	defer r.logs.mu.Unlock()
	var out []string
	for i := range r.logs.records {
		r.logs.records[i].Attrs(func(a slog.Attr) bool {
			out = append(out, a.Value.String())
			return true
		})
	}
	return out
}

// A failure after the record was deleted, while the index sets are being
// emptied, leaves the session ended -- every token resolves through the
// record -- so it is audited once and answered as any reuse is, with
// invalid_grant; the index ids that remain are dropped by the next listing.
func TestAnIndexFailureAfterTheRecordIsDeletedStillEndsTheSessionAuditedOnce(t *testing.T) {
	url, told := logoutListener(t)
	r := newReuseRigWith(t, 0, url)
	_, t0 := r.grant()
	access1, t1, err := r.refresh(t0)
	if err != nil {
		t.Fatal(err)
	}
	r.clock.advance(33 * time.Second)

	r.state.arm("", "issuer:session")
	_, _, err = r.refresh(t0)
	wantInvalidGrant(t, err)
	r.state.arm("", "")

	if got := r.sessions(); len(got) != 0 {
		t.Errorf("sessions listed = %d after the record was deleted, want none", len(got))
	}
	for i := range 3 {
		if _, _, err = r.refresh(t1); err == nil {
			t.Errorf("attempt %d: the successor refreshes although the session record was deleted", i)
		}
		if _, _, err = r.refresh(t0); err == nil {
			t.Errorf("attempt %d: the spent token is answered on retry", i)
		}
	}
	if err = r.userinfo(access1); err == nil {
		t.Error("userinfo answers for the ended session")
	}
	if got := r.revoked(); len(got) != 1 {
		t.Errorf("revocation records = %d, want exactly 1", len(got))
	}
	if got := told(); got != 1 {
		t.Errorf("%d back-channel logouts, want exactly 1", got)
	}
}

// ------------------------------------------------------------ concurrency

func TestConcurrentRefreshesOfOneTokenConvergeAndRevokeNothing(t *testing.T) {
	for round := range 5 {
		r := newReuseRig(t, 0)
		_, t0 := r.grant()

		const racers = 12
		next := make([]string, racers)
		errs := make([]error, racers)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range racers {
			wg.Go(func() {
				<-start
				_, next[i], errs[i] = r.refresh(t0)
			})
		}
		close(start)
		wg.Wait()

		for i := range racers {
			if errs[i] != nil {
				t.Fatalf("round %d: racer %d: %v", round, i, errs[i])
			}
			if next[i] != next[0] || next[i] == "" || next[i] == t0 {
				t.Fatalf("round %d: racer %d got %q, racer 0 got %q; want one successor", round, i, next[i], next[0])
			}
		}
		if got := r.sessions(); len(got) != 1 {
			t.Fatalf("round %d: %d sessions after a burst, want 1", round, len(got))
		}
		if got := r.revoked(); len(got) != 0 {
			t.Errorf("round %d: recorded %v for a burst of one token", round, r.trail.Actions())
		}
		if _, _, err := r.refresh(next[0]); err != nil {
			t.Errorf("round %d: the converged successor does not refresh: %v", round, err)
		}
	}
}

// A reuse lands while the legitimate holder's rotation is between its read
// and its conditional writes: the session stays ended, and the rotation does
// not put it back.
func TestAReuseDuringALegitimateRotationLeavesTheSessionEnded(t *testing.T) {
	for _, c := range []struct{ name, prefix string }{
		{"before the old pointer is spent", "issuer:session-token:"},
		{"before the record is written", "issuer:session:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newReuseRig(t, 0)
			access0, t0 := r.grant()
			access1, t1, err := r.refresh(t0)
			if err != nil {
				t.Fatal(err)
			}
			r.clock.advance(33 * time.Second)

			fired := false
			r.state.mu.Lock()
			r.state.replacePrefix = c.prefix
			r.state.beforeReplace = func() {
				fired = true
				_, _, reuseErr := r.refresh(t0)
				if !isInvalidGrant(reuseErr) {
					t.Errorf("the reuse inside the rotation = %v, want invalid_grant", reuseErr)
				}
			}
			r.state.mu.Unlock()

			_, t2, err := r.refresh(t1)
			if !fired {
				t.Fatal("the conditional write was never reached, so the guard is not exercised")
			}
			if err == nil {
				// A rotation that reports success must not have left a session behind.
				t.Logf("the rotation reported success with %q", t2)
			}
			if got := r.sessions(); len(got) != 0 {
				t.Errorf("the session was resurrected: %d listed", len(got))
			}
			for _, token := range []string{t1, t2} {
				if token == "" {
					continue
				}
				if _, _, err = r.refresh(token); err == nil {
					t.Error("a token of the ended session refreshes")
				}
			}
			for _, a := range []string{access0, access1} {
				if err = r.userinfo(a); err == nil {
					t.Error("userinfo answers for the ended session")
				}
			}
			if got := r.revoked(); len(got) != 1 {
				t.Errorf("recorded %v, want exactly one roster.session.revoked", r.trail.Actions())
			}
		})
	}
}

// Past the absolute limit the session ends by the limit, not by a reuse.
func TestAReuseAfterTheAbsoluteLimitIsRefusedAsTheLimit(t *testing.T) {
	r := newReuseRig(t, 10*time.Minute)
	_, t0 := r.grant()
	if _, _, err := r.refresh(t0); err != nil {
		t.Fatal(err)
	}
	r.clock.advance(11 * time.Minute)
	_, _, err := r.refresh(t0)
	wantInvalidGrant(t, err)
	for _, a := range r.trail.Find("roster.session.revoked") {
		if a.GetData().GetFields()["scope"].GetStringValue() == audit.ScopeRefreshTokenReuse {
			t.Error("a session past its absolute limit was audited as ended by a reuse")
		}
	}
}

// ---------------------------------------------------- sso, client, logout

// The record says nothing that could take over the browser sign-in: a
// sign-in's id is the value of its cookie. The issuer ends the one client's
// session and leaves the sign-in, and its other clients' sessions, alone.
func TestTheReuseRecordNamesNoBrowserSignInAndLeavesItAlone(t *testing.T) {
	r := newReuseRig(t, 0)
	ctx := context.Background()
	if _, err := r.iss.Sessions().Record(ctx, issuer.Opened{
		Identity: reusePerson, ClientID: "argocd", How: issuer.HowCode, Token: "tok0", SSO: "sso-cookie-value-42",
		Scopes: []string{"openid", "profile", "email"},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, _, err := r.refresh("tok0"); err != nil {
		t.Fatal(err)
	}
	r.clock.advance(33 * time.Second)
	_, _, err := r.refresh("tok0")
	wantInvalidGrant(t, err)

	got := r.revoked()
	if len(got) != 1 {
		t.Fatalf("recorded %v, want one roster.session.revoked", r.trail.Actions())
	}
	fields := got[0].GetData().GetFields()
	if _, has := fields["sso"]; has || len(fields) != 2 ||
		fields["scope"].GetStringValue() != "refresh_token_reuse" || fields["ended"].GetNumberValue() != 1 {
		t.Errorf("data = %v, want scope and ended only", got[0].GetData())
	}
	for _, rec := range r.trail.Records() {
		if strings.Contains(rec.String(), "sso-cookie-value-42") {
			t.Errorf("the %s record carries the sign-in's id: %v", rec.GetAction(), rec)
		}
	}
	// Only this client's session was ended: the sign-in's other client's
	// session is untouched.
	if _, err = r.iss.Sessions().Record(ctx, issuer.Opened{
		Identity: reusePerson, ClientID: "local-dev", How: issuer.HowCode, Token: "other0", SSO: "sso-cookie-value-42",
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := r.iss.Sessions().List(ctx, issuer.Query{Identity: reusePerson}); err != nil || len(got) != 1 || got[0].ClientID != "local-dev" {
		t.Errorf("sessions = %+v, %v; want the sign-in's other client's session still open", got, err)
	}
}

// Several presentations of one spent token at once: exactly one ends the
// session, one audit record, one set of writes (the record and its three
// index sets) and one WARN line; the others are refused with invalid_grant
// and write nothing.
func TestConcurrentReusesOfOneSpentTokenEndTheSessionOnce(t *testing.T) {
	for round := range 5 {
		r := newReuseRig(t, 0)
		_, t0 := r.grant()
		_, t1, err := r.refresh(t0)
		if err != nil {
			t.Fatal(err)
		}
		r.clock.advance(33 * time.Second)

		const racers = 12
		errs := make([]error, racers)
		writes := r.state.writes.Load()
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range racers {
			wg.Go(func() {
				<-start
				_, _, errs[i] = r.refresh(t0)
			})
		}
		close(start)
		wg.Wait()

		for i, err := range errs {
			if !isInvalidGrant(err) {
				t.Fatalf("round %d: racer %d got %v, want invalid_grant", round, i, err)
			}
		}
		if got := r.state.writes.Load() - writes; got != 4 {
			t.Errorf("round %d: %d successful writes, want one set of 4", round, got)
		}
		if got := r.revoked(); len(got) != 1 {
			t.Errorf("round %d: %d revocation records, want exactly 1", round, len(got))
		}
		if got := r.warnings("its session has been ended"); got != 1 {
			t.Errorf("round %d: %d WARN lines about the end, want 1", round, got)
		}
		if len(r.sessions()) != 0 {
			t.Errorf("round %d: the session is still listed", round)
		}
		if _, _, err = r.refresh(t1); err == nil {
			t.Errorf("round %d: the successor refreshes", round)
		}
	}
}

// ------------------------------------------ a refresh racing a reuse

// logoutListener is a client's back-channel endpoint that counts what it is told.
func logoutListener(t *testing.T) (url string, told func() int) {
	t.Helper()
	var n atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server.URL, func() int { return int(n.Load()) }
}

// reuseBound is the number of conditional deletes before the unconditional one.
const reuseBound = 3

// A thief holding the live successor refreshes it between the reuse's read
// of the record and its delete, on every attempt the delete makes at a
// revision, and beyond: the session is still ended -- by the final
// unconditional delete -- with one audit record and one logout, and the
// rotations that lost are refused or end up with nothing.
func TestAReuseRacingRepeatedRotationsStillEndsTheSessionOnce(t *testing.T) {
	for _, rotations := range []int{1, 3, 6} {
		t.Run(fmt.Sprintf("%d rotations", rotations), func(t *testing.T) {
			url, told := logoutListener(t)
			r := newReuseRigWith(t, 0, url)
			access0, t0 := r.grant()
			access1, t1, err := r.refresh(t0)
			if err != nil {
				t.Fatal(err)
			}
			r.clock.advance(33 * time.Second)

			live, rotated := t1, 0
			var hookReads, hookWrites int64
			r.state.mu.Lock()
			r.state.deleteHooks = rotations
			r.state.beforeDelete = func() {
				r0, w0 := r.state.reads.Load(), r.state.writes.Load()
				defer func() {
					hookReads += r.state.reads.Load() - r0
					hookWrites += r.state.writes.Load() - w0
				}()
				_, next, rerr := r.refresh(live)
				if rerr != nil {
					t.Errorf("rotation %d during the reuse: %v", rotated, rerr)
					return
				}
				live = next
				rotated++
			}
			r.state.mu.Unlock()

			reads, writes := r.state.reads.Load(), r.state.writes.Load()
			_, _, err = r.refresh(t0)
			wantInvalidGrant(t, err)
			reads, writes = r.state.reads.Load()-reads-hookReads, r.state.writes.Load()-writes-hookWrites
			if wantReads := int64(2 + min(rotations, reuseBound)); reads != wantReads || writes != 4 {
				t.Errorf("a reuse racing %d rotations made %d reads and %d landed writes, want %d and 4", rotations, reads, writes, wantReads)
			}
			if rotated == 0 {
				t.Fatal("no rotation raced the delete, so the guard is not exercised")
			}
			if len(r.sessions()) != 0 {
				t.Error("a thief refreshing the successor kept the session alive")
			}
			if _, _, err = r.refresh(live); err == nil {
				t.Error("the newest successor still refreshes")
			}
			for _, a := range []string{access0, access1} {
				if err = r.userinfo(a); err == nil {
					t.Error("userinfo answers for the ended session")
				}
			}
			if got := r.revoked(); len(got) != 1 {
				t.Errorf("%d revocation records, want exactly 1", len(got))
			}
			if got := told(); got != 1 {
				t.Errorf("%d back-channel logouts, want exactly 1", got)
			}
		})
	}
}

// A delete that committed but reports failure ends the session; nobody can
// say who ended it, so there is no audit record, and one WARN names the
// session without a token. The client's answer is invalid_grant.
func TestADeleteThatCommittedButReportsFailureEndsTheSessionWithoutARecord(t *testing.T) {
	r := newReuseRig(t, 0)
	access0, t0 := r.grant()
	_, t1, err := r.refresh(t0)
	if err != nil {
		t.Fatal(err)
	}
	id := r.sessions()[0].ID
	r.clock.advance(33 * time.Second)

	capture := &recordingHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(previous) })

	r.state.mu.Lock()
	r.state.ghost = errors.New("the store is unavailable")
	r.state.mu.Unlock()

	_, _, err = r.refresh(t0)
	wantInvalidGrant(t, err)

	if len(r.sessions()) != 0 {
		t.Error("the session is still listed")
	}
	if _, _, err = r.refresh(t1); err == nil {
		t.Error("the successor refreshes")
	}
	if err = r.userinfo(access0); err == nil {
		t.Error("userinfo answers for the ended session")
	}
	if got := r.revoked(); len(got) != 0 {
		t.Errorf("%d revocation records for an end nobody can claim, want none", len(got))
	}

	capture.mu.Lock()
	defer capture.mu.Unlock()
	warned := 0
	for i := range capture.records {
		rec := &capture.records[i]
		if rec.Level != slog.LevelWarn || !strings.Contains(rec.Message, "already gone") {
			continue
		}
		rec.Attrs(func(a slog.Attr) bool {
			if a.Key == "session" && a.Value.String() == id {
				warned++
			}
			if strings.Contains(a.Value.String(), t0) || strings.Contains(a.Value.String(), t1) {
				t.Errorf("the warning carries a token in %s", a.Key)
			}
			return true
		})
		if strings.Contains(rec.Message, t0) || strings.Contains(rec.Message, t1) {
			t.Error("the warning message carries a token")
		}
	}
	if warned != 1 {
		t.Errorf("%d warnings naming session %s, want 1", warned, id)
	}
}
