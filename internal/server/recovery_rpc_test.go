package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/recoverygate"
)

// The console turns the recovery sign-in off, and only off.

// logBuffer collects what the console logs, with its level.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

type recoveryRig struct {
	console *Console
	trail   *audittest.Recorder
	state   *memory.Store
	gate    *recoverygate.Gate
	logs    *logBuffer
	// ended is what the sweep reports and how often it ran.
	endedSignIns, endedSessions int
	sweeps                      int
	sweepErr                    error
}

func newRecoveryRig(t *testing.T, generation string, state port.State) *recoveryRig {
	t.Helper()
	rig := &recoveryRig{trail: audittest.New(t), logs: &logBuffer{}}
	if state == nil {
		rig.state = memory.New()
		state = rig.state
	}
	rig.gate = recoverygate.New(state, generation, recoverygate.WithTTL(time.Nanosecond))
	rig.console = githubConsole(t, nil)
	rig.console.deps.Audit = rig.trail
	rig.console.deps.Recovery = oneRecovery{}
	rig.console.deps.RecoveryGate = rig.gate
	rig.console.deps.Instance = "acme"
	rig.console.deps.Log = slog.New(slog.NewTextHandler(rig.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rig.console.recoveryEnd.set(func(context.Context) (int, int, error) {
		rig.sweeps++
		return rig.endedSignIns, rig.endedSessions, rig.sweepErr
	})
	return rig
}

func (r *recoveryRig) disable(id access.Identity) (*directoryrosterv1.DisableRecoveryResponse, error) {
	resp, err := r.console.DisableRecovery(WithIdentity(context.Background(), id), &directoryrosterv1.DisableRecoveryRequest{})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (r *recoveryRig) status(id access.Identity) (*directoryrosterv1.GetRecoveryResponse, error) {
	resp, err := r.console.GetRecovery(WithIdentity(context.Background(), id), &directoryrosterv1.GetRecoveryRequest{})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

var (
	idpOperator = access.Identity{Email: "ada@example.com", Subject: "ada@example.com", Source: access.SourceOIDC, Role: access.RoleOperator}
	recoveryID  = access.Identity{Email: "cluster:k8s:ops:recovery", Subject: "cluster:k8s:ops:recovery", Source: access.SourceRecovery, Role: access.RoleOperator}
)

// The caller must be an operator signed in through the identity provider, and
// the RPC says so itself: it does not rely on a hidden button.
func TestDisableRecoveryRefusesEveryoneButAnIdPOperator(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		id   access.Identity
		want string
	}{
		"a recovery sign-in, which is an operator by construction": {recoveryID, "recovery sign-in"},
		"a workload, whatever role it holds": {access.Identity{Subject: "system:serviceaccount:ops:ci", Source: access.SourceWorkload,
			Role: access.RoleOperator}, "signed in through the identity provider"},
		"a viewer": {access.Identity{Email: "v@example.com", Source: access.SourceOIDC, Role: access.RoleViewer}, "operator role"},
		"an operator of one workspace only": {access.Identity{Email: "w@example.com", Source: access.SourceDirectory, Role: access.RoleNone,
			Scopes: map[string]access.Role{"acme": access.RoleOperator}}, "operator role"},
		"an operator with no address": {access.Identity{Subject: "opaque", Source: access.SourceOIDC, Role: access.RoleOperator}, "person"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newRecoveryRig(t, "", nil)
			_, err := rig.disable(tc.id)
			if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want permission denied naming %q", err, tc.want)
			}
			if off, _ := rig.gate.Off(t.Context()); off {
				t.Fatal("a refused caller turned recovery off")
			}
			if rig.sweeps != 0 {
				t.Errorf("the sweep ran %d times for a refused caller", rig.sweeps)
			}
			got := rig.trail.Find("roster.recovery.disabled")
			if len(got) != 1 || got[0].GetOutcome().GetResult() != auditv1.Outcome_RESULT_DENIED {
				t.Errorf("recorded %v, want one denied record", rig.trail.Actions())
			}
		})
	}
}

func TestDisableRecoveryAcceptsAnOperatorOfEveryIdentityProviderPath(t *testing.T) {
	t.Parallel()
	for _, source := range []access.Source{access.SourceOIDC, access.SourceDirectory, access.SourceForwarded} {
		t.Run(string(source), func(t *testing.T) {
			t.Parallel()
			rig := newRecoveryRig(t, "", nil)
			id := idpOperator
			id.Source = source
			if _, err := rig.disable(id); err != nil {
				t.Fatalf("an operator signed in by %s was refused: %v", source, err)
			}
		})
	}
}

// There is nothing to turn off where recovery is not built.
func TestDisableRecoveryWithNoRecoveryIsAFailedPrecondition(t *testing.T) {
	t.Parallel()
	rig := newRecoveryRig(t, "", nil)
	rig.console.deps.Recovery = nil
	if _, err := rig.disable(idpOperator); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("err = %v, want failed precondition", err)
	}
	if rig.sweeps != 0 {
		t.Error("the sweep ran with nothing to turn off")
	}
}

// Disabling switches recovery off, ends the sessions it opened, counts them,
// and leaves one record naming the operator.
func TestDisableRecoveryEndsRecoverySessionsAndRecordsIt(t *testing.T) {
	t.Parallel()
	rig := newRecoveryRig(t, "2026-10", nil)
	rig.endedSignIns, rig.endedSessions = 2, 3

	got, err := rig.disable(idpOperator)
	if err != nil {
		t.Fatal(err)
	}
	if got.GetAlreadyDisabled() || got.GetDisabledBy() != "ada@example.com" || got.GetSignInsEnded() != 2 || got.GetSessionsEnded() != 3 || got.GetAuditPending() {
		t.Errorf("response = %v", got)
	}
	if off, _ := rig.gate.Off(t.Context()); !off {
		t.Fatal("recovery is still on")
	}
	records := rig.trail.Find("roster.recovery.disabled")
	if len(records) != 1 {
		t.Fatalf("recorded %v, want one disable", rig.trail.Actions())
	}
	r := records[0]
	data := r.GetData().AsMap()
	if r.GetOutcome().GetResult() != auditv1.Outcome_RESULT_SUCCESS || r.GetActor().GetId() != "ada@example.com" || r.GetActor().GetKind() != "person" ||
		r.GetTargets()[0].GetId() != "acme" || data["generation"] != "2026-10" || data["sign_ins_ended"] != float64(2) || data["sessions_ended"] != float64(3) {
		t.Errorf("record = %v", r)
	}
	if strings.Contains(rig.logs.String(), "level=ERROR") {
		t.Errorf("an ERROR was logged for a clean disable:\n%s", rig.logs)
	}
}

// A second call, by anyone, changes nothing and keeps the first record's author.
func TestDisableRecoveryIsIdempotent(t *testing.T) {
	t.Parallel()
	rig := newRecoveryRig(t, "", nil)
	first, err := rig.disable(idpOperator)
	if err != nil {
		t.Fatal(err)
	}
	other := idpOperator
	other.Email, other.Subject = "bob@example.com", "bob@example.com"
	again, err := rig.disable(other)
	if err != nil {
		t.Fatal(err)
	}
	if !again.GetAlreadyDisabled() || again.GetDisabledBy() != "ada@example.com" || !again.GetDisabledAt().AsTime().Equal(first.GetDisabledAt().AsTime()) {
		t.Errorf("second = %v, first = %v", again, first)
	}
	if n := len(rig.trail.Find("roster.recovery.disabled")); n != 1 {
		t.Errorf("%d records after a repeat that ended nothing, want 1", n)
	}
	// A repeat that finds a straggler session ends it and says so.
	rig.endedSessions = 1
	if _, err = rig.disable(other); err != nil {
		t.Fatal(err)
	}
	records := rig.trail.Find("roster.recovery.disabled")
	if len(records) != 2 || records[1].GetData().AsMap()["repeat"] != true {
		t.Errorf("recorded %v, want a second record marked repeat", rig.trail.Actions())
	}
}

// The switch is thrown whether or not the trail can be written; the failure
// is loud and the record is retried through the ordinary queue.
func TestDisableRecoveryStillDisablesWhenTheTrailIsDown(t *testing.T) {
	t.Parallel()
	rig := newRecoveryRig(t, "", nil)
	rig.trail.Fail = errors.New("the writer is down")

	got, err := rig.disable(idpOperator)
	if err != nil {
		t.Fatalf("the trail being down stopped the disable: %v", err)
	}
	if !got.GetAuditPending() {
		t.Error("the response does not say the record is pending")
	}
	if off, _ := rig.gate.Off(t.Context()); !off {
		t.Fatal("recovery is still on")
	}
	if logs := rig.logs.String(); !strings.Contains(logs, "level=ERROR") || !strings.Contains(logs, "audit trail could not be written") {
		t.Errorf("no ERROR for the unwritten record:\n%s", logs)
	}
	if n := len(rig.trail.Find("roster.recovery.disabled")); n != 1 {
		t.Errorf("%d records queued for retry, want 1", n)
	}
}

// A State that cannot keep the switch is refused: a disable gone at the next
// restart would be a lie.
func TestDisableRecoveryWithAStateThatCannotKeepItIsRefused(t *testing.T) {
	t.Parallel()
	rig := newRecoveryRig(t, "", nil)
	rig.gate = recoverygate.New(nil, "")
	rig.console.deps.RecoveryGate = rig.gate
	_, err := rig.disable(idpOperator)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "recovery.enabled") {
		t.Fatalf("err = %v, want failed precondition pointing at the configuration", err)
	}
	st, err := rig.status(idpOperator)
	if err != nil {
		t.Fatal(err)
	}
	if st.GetCanDisable() || st.GetDurable() || st.GetCannotDisableReason() == "" {
		t.Errorf("status = %v, want the action withheld with a reason", st)
	}
}

// GetRecovery tells an operator the state, and a caller who cannot disable why.
func TestGetRecoveryReportsTheStateToOperators(t *testing.T) {
	t.Parallel()
	rig := newRecoveryRig(t, "g1", nil)

	st, err := rig.status(idpOperator)
	if err != nil {
		t.Fatal(err)
	}
	if !st.GetConfigured() || !st.GetEffective() || !st.GetCanDisable() || st.GetKind() != "token" || st.GetGeneration() != "g1" || !st.GetDurable() {
		t.Errorf("before = %v", st)
	}
	// Recovery's own session may look, and is told it cannot act.
	st, err = rig.status(recoveryID)
	if err != nil {
		t.Fatal(err)
	}
	if st.GetCanDisable() || !strings.Contains(st.GetCannotDisableReason(), "recovery sign-in") {
		t.Errorf("recovery's view = %v", st)
	}
	viewer := access.Identity{Email: "v@example.com", Source: access.SourceOIDC, Role: access.RoleViewer}
	if _, err = rig.status(viewer); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer read the state: %v", err)
	}

	if _, err = rig.disable(idpOperator); err != nil {
		t.Fatal(err)
	}
	st, err = rig.status(idpOperator)
	if err != nil {
		t.Fatal(err)
	}
	if st.GetEffective() || st.GetCanDisable() || st.GetDisabledBy() != "ada@example.com" || st.GetDisabledAt() == nil || st.GetSupersededAt() != nil {
		t.Errorf("after = %v", st)
	}

	// The configuration moves on to another generation: recovery is on again,
	// and the page says the old turn-off was overridden.
	next := newRecoveryRig(t, "g2", rig.state)
	st, err = next.status(idpOperator)
	if err != nil {
		t.Fatal(err)
	}
	if !st.GetEffective() || !st.GetCanDisable() || st.GetDisabledAt() != nil || st.GetSupersededBy() != "ada@example.com" {
		t.Errorf("after the bump = %v", st)
	}
}

// ------------------------------------------------------------ over HTTP

// recoveryHTTP is a console server over rig's console, with a signed session
// codec, as a deployment wires it.
func recoveryHTTP(t *testing.T, rig *recoveryRig) http.Handler {
	t.Helper()
	sessions, err := access.NewSessions(make([]byte, access.SessionKeyBytes), time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	return NewConsoleServer(ConsoleServerDeps{
		Console: rig.console, Authorizer: rig.console.deps.Authorizer, Sessions: sessions,
		State:    access.NewStateCodec(make([]byte, 32), time.Minute),
		Recovery: oneRecovery{}, RecoveryGate: rig.gate, Log: capturedLogger(),
	}).Handler()
}

func whoamiStatus(t *testing.T, handler http.Handler, cookies ...*http.Cookie) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/.access/whoami", nil)
	for _, c := range cookies {
		request.AddCookie(c)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if strings.Contains(response.Body.String(), `"signed-in"`) {
		return "signed-in"
	}
	return "signed-out"
}

func sessionCookieOf(t *testing.T, response *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range response.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no session cookie in %q", response.Header().Values("Set-Cookie"))
	return nil
}

// Turning recovery off ends the console's own recovery sessions at once, and
// closes the door they came through: the proof is refused and the page offers
// no form.
func TestAfterTheSwitchARecoverySessionIsNotASessionAndTheDoorIsShut(t *testing.T) {
	t.Parallel()
	rig := newRecoveryRig(t, "", nil)
	handler := recoveryHTTP(t, rig)

	signedIn := recoverThroughConsole(handler)
	if signedIn.Code >= http.StatusBadRequest {
		t.Fatalf("recovery before the switch = %d %q", signedIn.Code, signedIn.Body.String())
	}
	cookie := sessionCookieOf(t, signedIn)
	if got := whoamiStatus(t, handler, cookie); got != "signed-in" {
		t.Fatalf("the recovery session before the switch = %s", got)
	}
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/login", nil))
	if !strings.Contains(page.Body.String(), "/login/recovery") {
		t.Fatal("the sign-in page offers no recovery before the switch")
	}

	if _, err := rig.disable(idpOperator); err != nil {
		t.Fatal(err)
	}

	if got := whoamiStatus(t, handler, cookie); got != "signed-out" {
		t.Errorf("the recovery session after the switch = %s", got)
	}
	refused := recoverThroughConsole(handler)
	if refused.Code != http.StatusForbidden || refused.Header().Get("Set-Cookie") != "" {
		t.Errorf("recovery after the switch = %d, cookie %q; want 403 and no session", refused.Code, refused.Header().Get("Set-Cookie"))
	}
	// The page says the same to everyone; the record says why.
	var reasons []string
	for _, r := range rig.trail.Find("roster.recovery.signed_in") {
		reasons = append(reasons, r.GetOutcome().GetReason())
	}
	if !slices.Contains(reasons, reasonRecoveryDisabled) {
		t.Errorf("the refused attempt was recorded with reasons %q, want %q", reasons, reasonRecoveryDisabled)
	}
	page = httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/login", nil))
	if strings.Contains(page.Body.String(), "/login/recovery") {
		t.Error("the sign-in page still offers recovery after the switch")
	}
}

// A redeploy that reads the same document finds the record and stays off; one
// that reads a bumped generation is on again. Nothing else turns it on.
func TestARedeployWithTheSameConfigurationStaysDisabledAndABumpedGenerationReenables(t *testing.T) {
	t.Parallel()
	first := newRecoveryRig(t, "2026-10", nil)
	if _, err := first.disable(idpOperator); err != nil {
		t.Fatal(err)
	}

	// A cold start: a new process, the same State, the same generation.
	redeployed := newRecoveryRig(t, "2026-10", first.state)
	handler := recoveryHTTP(t, redeployed)
	if response := recoverThroughConsole(handler); response.Code != http.StatusForbidden {
		t.Fatalf("a redeploy of the same configuration signed recovery in again: %d", response.Code)
	}

	// The operator's deliberate change.
	bumped := newRecoveryRig(t, "2026-11", first.state)
	handler = recoveryHTTP(t, bumped)
	if response := recoverThroughConsole(handler); response.Code >= http.StatusBadRequest {
		t.Fatalf("a bumped generation left recovery off: %d %q", response.Code, response.Body.String())
	}
}

// A switch that cannot be read refuses the door, rather than leaving it open.
func TestAnUnreadableSwitchRefusesRecovery(t *testing.T) {
	t.Parallel()
	rig := newRecoveryRig(t, "", &failingState{State: memory.New()})
	handler := recoveryHTTP(t, rig)
	if response := recoverThroughConsole(handler); response.Code != http.StatusForbidden {
		t.Fatalf("recovery with an unreadable switch = %d", response.Code)
	}
}

type failingState struct{ port.State }

func (*failingState) Get(context.Context, string) (port.Record, error) {
	return port.Record{}, errors.New("dynamodb: throttled")
}

// flakyState fails its reads while fail is set.
type flakyState struct {
	port.State
	fail atomic.Bool
}

func (f *flakyState) Get(ctx context.Context, key string) (port.Record, error) {
	if f.fail.Load() {
		return port.Record{}, errors.New("dynamodb: throttled")
	}
	return f.State.Get(ctx, key)
}

// A fault in the State refuses the recovery session and clears nothing: the
// cookie works again once the State answers, and only a record ends it.
func TestAReadFaultRefusesARecoverySessionWithoutEndingIt(t *testing.T) {
	t.Parallel()
	flaky := &flakyState{State: memory.New()}
	rig := newRecoveryRig(t, "", flaky)
	handler := recoveryHTTP(t, rig)

	cookie := sessionCookieOf(t, recoverThroughConsole(handler))
	if got := whoamiStatus(t, handler, cookie); got != "signed-in" {
		t.Fatalf("before the fault = %s", got)
	}

	flaky.fail.Store(true)
	request := httptest.NewRequest(http.MethodGet, "/.access/whoami", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if strings.Contains(response.Body.String(), `"signed-in"`) {
		t.Fatal("a recovery session was honoured while the switch could not be read")
	}
	for _, c := range response.Result().Cookies() {
		if c.Name == sessionCookie && c.Value == "" {
			t.Error("the session cookie was cleared on a read fault")
		}
	}

	flaky.fail.Store(false)
	if got := whoamiStatus(t, handler, cookie); got != "signed-in" {
		t.Errorf("after the State answered again = %s, want the session back", got)
	}

	if _, err := rig.disable(idpOperator); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/.access/whoami", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	cleared := false
	for _, c := range response.Result().Cookies() {
		cleared = cleared || (c.Name == sessionCookie && c.Value == "")
	}
	honoured := strings.Contains(response.Body.String(), `"signed-in"`)
	if honoured || !cleared {
		t.Errorf("after the record the session was honoured=%v, cookie cleared=%v; want ended and cleared", honoured, cleared)
	}
}

// ------------------------------------------------- tokens recovery issued

func recoveryBearerServer(t *testing.T, rig *recoveryRig, issuer *fakeIssuer) http.Handler {
	t.Helper()
	sessions, err := access.NewSessions(make([]byte, access.SessionKeyBytes), time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	return NewConsoleServer(ConsoleServerDeps{
		Console: rig.console, Authorizer: rig.console.deps.Authorizer, Sessions: sessions,
		State:    access.NewStateCodec(make([]byte, 32), time.Minute),
		Recovery: oneRecovery{}, RecoveryGate: rig.gate, Log: capturedLogger(),
		Forwarded: ForwardedIdentity{Issuer: issuer.URL, Audience: "console"},
	}).Handler()
}

func callAs(handler http.Handler, bearer, method string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/sluis.v1.AccessService/"+method, strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+bearer)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// A token the issuer minted under a recovery sign-in is verified offline here,
// so the console refuses it itself once recovery is off. Until then it may not
// turn recovery off either: it names no person.
func TestARecoveryTokenAtTheForwardedBearerCannotDisableAndDiesWithRecovery(t *testing.T) {
	t.Parallel()
	rig := newRecoveryRig(t, "", nil)
	issuer := newFakeIssuer(t)
	handler := recoveryBearerServer(t, rig, issuer)

	recoveryToken := issuer.mint(t, map[string]any{
		"sub": "system:serviceaccount:ops:recovery", "aud": []string{"console"}, "sluis_recovery": true,
	}, nil)
	workloadToken := issuer.mint(t, map[string]any{"sub": "system:serviceaccount:ops:ci", "aud": []string{"console"}}, nil)

	if got := callAs(handler, recoveryToken, "GetRecovery"); got.Code != http.StatusForbidden {
		t.Fatalf("a recovery token read recovery's state: %d %s (it has no role here, but it is a principal)", got.Code, got.Body.String())
	}
	denied := callAs(handler, recoveryToken, "DisableRecovery")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("a recovery-derived forwarded bearer calling DisableRecovery = %d %s, want it refused", denied.Code, denied.Body.String())
	}
	if off, _ := rig.gate.Off(t.Context()); off {
		t.Fatal("a recovery-derived bearer turned recovery off")
	}

	if _, err := rig.disable(idpOperator); err != nil {
		t.Fatal(err)
	}
	if got := callAs(handler, recoveryToken, "GetRecovery"); got.Code != http.StatusUnauthorized {
		t.Errorf("a recovery token after the switch = %d %s, want it not authenticated", got.Code, got.Body.String())
	}
	// A token that recovery did not mint is not touched.
	if got := callAs(handler, workloadToken, "GetRecovery"); got.Code == http.StatusUnauthorized {
		t.Errorf("a workload's token after the switch = %d, want it still authenticated", got.Code)
	}
}

// An address in a header the gateway set is only as strong as the gateway, and
// is a source of its own: it may not turn recovery off.
func TestAnUnverifiedHeaderIdentityCannotDisableRecovery(t *testing.T) {
	t.Parallel()
	rig := newRecoveryRig(t, "", nil)
	sessions, err := access.NewSessions(make([]byte, access.SessionKeyBytes), time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	server := NewConsoleServer(ConsoleServerDeps{
		Console: rig.console, Authorizer: rig.console.deps.Authorizer, Sessions: sessions,
		Forwarded: ForwardedIdentity{EmailHeader: "X-Email"}, Log: capturedLogger(),
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Email", "ada@example.com")
	principal, ok := server.principal(httptest.NewRecorder(), request)
	if !ok || principal.Source != access.SourceForwardedHeader {
		t.Fatalf("principal = %+v, %v; want the header's own source", principal, ok)
	}
	id := idpOperator
	id.Source = access.SourceForwardedHeader
	_, err = rig.disable(id)
	if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "verified") {
		t.Fatalf("err = %v, want permission denied asking for a verified sign-in", err)
	}
}

// Refused attempts are recorded once a window per caller and reason, with a
// count, not once each.
func TestRefusedDisableAttemptsAreRecordedOncePerWindow(t *testing.T) {
	t.Parallel()
	rig := newRecoveryRig(t, "", nil)
	for range 25 {
		if _, err := rig.disable(recoveryID); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("err = %v", err)
		}
	}
	if n := len(rig.trail.Find("roster.recovery.disabled")); n != 1 {
		t.Errorf("%d denied records for 25 identical refusals, want 1", n)
	}
	// A different caller is its own.
	other := recoveryID
	other.Email, other.Subject = "other", "other"
	_, _ = rig.disable(other)
	if n := len(rig.trail.Find("roster.recovery.disabled")); n != 2 {
		t.Errorf("%d denied records after a second caller, want 2", n)
	}
}
