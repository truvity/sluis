package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/audit/sdk/emit"
	auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"
	"github.com/truvity/sluis/audit/sdk/record"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit/audittest"
)

// read is the middleware's reading of a request with these headers from
// this peer.
func read(header http.Header, peer string, hops int) *record.Context {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header = header
	r.RemoteAddr = peer
	return auditRequest(r, hops)
}

// The address an event keeps is read from the right of X-Forwarded-For,
// past the deployment's own proxies, and only when the deployment says how
// many there are; anywhere else, and wherever the header cannot have come
// through them, the peer is the address. A caller's own entries at the left
// are never taken.
func TestTheClientAddressIsReadFromTheRightPastTrustedHops(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, forwarded, peer string
		hops                  int
		want                  string
	}{
		// The count is the emitter's: the peer is the last entry of the chain
		// and counts as one of the trusted hops.
		{"no hops, header ignored", "198.51.100.1, 10.0.0.1", "192.0.2.10:5555", 0, "192.0.2.10"},
		{"one proxy that appended the client", "198.51.100.1", "10.0.0.2:5555", 1, "198.51.100.1"},
		{"one proxy: a caller's own entry is passed over", "203.0.113.66, 198.51.100.1", "10.0.0.2:5555", 1, "198.51.100.1"},
		{"an edge and a gateway: the client the edge appended", "198.51.100.1, 10.0.0.1", "10.0.0.2:5555", 2, "198.51.100.1"},
		{"an edge and a gateway: a caller's own entry is passed over", "203.0.113.66, 198.51.100.1, 10.0.0.1", "10.0.0.2:5555", 2, "198.51.100.1"},
		{"no longer than the hops: not through them", "10.0.0.1", "10.0.0.2:5555", 2, "10.0.0.2"},
		{"IPv6 with a port", "[2001:db8::7]:443, 10.0.0.1", "10.0.0.2:5555", 2, "2001:db8::7"},
		{"no header", "", "10.0.0.2:5555", 1, "10.0.0.2"},
		{"not an address", "unknown, 10.0.0.1", "10.0.0.2:5555", 2, "10.0.0.2"},
		{"a forged line", "198.51.100.1\nevent.outcome=success, 10.0.0.1", "10.0.0.2:5555", 2, "10.0.0.2"},
		{"IPv6 peer", "", "[2001:db8::9]:5555", 0, "2001:db8::9"},
	} {
		header := http.Header{}
		if tc.forwarded != "" {
			header["X-Forwarded-For"] = []string{tc.forwarded}
		}
		if got := emit.Client(read(header, tc.peer, tc.hops)); got != tc.want {
			t.Errorf("%s: client address = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Two X-Forwarded-For headers are one list, in order.
func TestForwardedForHeadersAreOneList(t *testing.T) {
	t.Parallel()
	header := http.Header{"X-Forwarded-For": []string{"203.0.113.66, 198.51.100.1", "10.0.0.1"}}
	if got := emit.Client(read(header, "10.0.0.2:1", 2)); got != "198.51.100.1" {
		t.Errorf("client address = %q, want the entry just left of the two trusted hops", got)
	}
}

// A User-Agent or a request id is the caller's own text: what is kept of
// it cannot carry a line break, and is cut to its bound.
func TestRequestHeadersAreSanitisedAndBounded(t *testing.T) {
	t.Parallel()
	header := http.Header{
		"User-Agent":   []string{"curl/8.0\r\n{\"audit\":true,\"event.action\":\"sign-in\"}" + strings.Repeat("a", 400)},
		"X-Request-Id": []string{"abc\ndef" + strings.Repeat("0", 200)},
	}
	got := read(header, "192.0.2.1:1", 0)
	for name, value := range map[string]string{"user agent": got.GetUserAgent(), "request id": got.GetRequestId()} {
		if strings.ContainsAny(value, "\r\n") {
			t.Errorf("the %s kept a line break: %q", name, value)
		}
	}
	if len(got.GetUserAgent()) > maxUserAgent || len(got.GetRequestId()) > maxRequestID {
		t.Errorf("user agent %d bytes, request id %d bytes; want at most %d and %d",
			len(got.GetUserAgent()), len(got.GetRequestId()), maxUserAgent, maxRequestID)
	}
	if !strings.HasPrefix(got.GetRequestId(), "abcdef") {
		t.Errorf("request id = %q", got.GetRequestId())
	}
}

// oneRecovery admits one subject with one proof.
type oneRecovery struct{}

func (oneRecovery) Kind() string   { return "token" }
func (oneRecovery) Prompt() Prompt { return Prompt{Label: "Recovery token"} }
func (oneRecovery) Verify(_ context.Context, proof string) (string, error) {
	if proof != "the-proof" {
		return "", ErrRecoveryRefused
	}
	return "cluster:k8s:ops:recovery", nil
}

// recoveryServer is a console with recovery, recording into trail.
func recoveryServer(t *testing.T, trail *audittest.Recorder, trustedHops int) http.Handler {
	t.Helper()
	console := githubConsole(t, nil)
	console.deps.Audit = trail
	sessions, err := access.NewSessions(make([]byte, access.SessionKeyBytes), time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	return NewConsoleServer(ConsoleServerDeps{
		Console: console, Authorizer: console.deps.Authorizer, Sessions: sessions, Recovery: oneRecovery{},
		ForwardedForTrustedHops: trustedHops, Log: slog.New(slog.DiscardHandler),
	}).Handler()
}

func recoverThroughConsole(handler http.Handler) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/login/recovery", nil)
	request.RemoteAddr = "10.0.0.2:40000"
	request.Header.Set("Authorization", "Bearer the-proof")
	request.Header.Set("X-Forwarded-For", "198.51.100.23, 10.0.0.1")
	request.Header.Set("User-Agent", "curl/8.9")
	request.Header.Set("X-Request-Id", "gw-7")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// A recovery sign-in is the one event that does not fail open. Its record
// is written durably before there is a session; when it cannot be, there
// is no session, the operator is told why, and the refusal is recorded the
// ordinary way. Once the trail can be written, the same proof signs in.
func TestARecoverySignInIsRefusedWithoutItsRecord(t *testing.T) {
	t.Parallel()
	trail := audittest.New(t)
	handler := recoveryServer(t, trail, 2)

	trail.Fail = errors.New("the writer refused the record")
	refused := recoverThroughConsole(handler)
	if refused.Code != http.StatusServiceUnavailable || !strings.Contains(refused.Body.String(), "audit trail could not be written") {
		t.Errorf("recovery while the trail cannot be written = %d %q, want 503 saying why", refused.Code, refused.Body.String())
	}
	if cookie := refused.Header().Get("Set-Cookie"); cookie != "" {
		t.Errorf("a refused recovery set a session: %s", cookie)
	}
	written := trail.Records()
	if len(written) != 1 || written[0].GetAction() != "roster.recovery.signed_in" ||
		written[0].GetOutcome().GetResult() != auditv1.Outcome_RESULT_DENIED || written[0].GetOutcome().GetReason() != reasonUnaudited {
		t.Fatalf("written = %v, want only the refusal", trail.Actions())
	}

	trail.Fail = nil
	allowed := recoverThroughConsole(handler)
	if allowed.Code >= http.StatusBadRequest || allowed.Header().Get("Set-Cookie") == "" {
		t.Fatalf("recovery once the trail can be written = %d, cookie %q", allowed.Code, allowed.Header().Get("Set-Cookie"))
	}
	written = trail.Records()
	if len(written) != 2 {
		t.Fatalf("recorded %v, want the refusal and then the recovery sign-in", trail.Actions())
	}
	got := written[1]
	if got.GetOutcome().GetResult() != auditv1.Outcome_RESULT_SUCCESS || got.GetActor().GetId() != "cluster:k8s:ops:recovery" ||
		got.GetActor().GetKind() != "recovery" || got.GetTargets()[0].GetId() != "console" {
		t.Errorf("the recovery record = %v", got)
	}
	// Through the handler, so the request is read as a deployment reads it.
	if c := got.GetContext(); emit.Client(c) != "198.51.100.23" || c.GetUserAgent() != "curl/8.9" || c.GetRequestId() != "gw-7" {
		t.Errorf("request = %v, want the forwarded client, its agent and the gateway's id", c)
	}
}

// Without the deployment's count of its own proxies, the same request
// records its peer.
func TestAnUntrustedDeploymentRecordsThePeer(t *testing.T) {
	t.Parallel()
	trail := audittest.New(t)
	if response := recoverThroughConsole(recoveryServer(t, trail, 0)); response.Code >= http.StatusBadRequest {
		t.Fatalf("recovery = %d %q", response.Code, response.Body.String())
	}
	if records := trail.Records(); len(records) != 1 || emit.Client(records[0].GetContext()) != "10.0.0.2" {
		t.Errorf("recorded %v, want the peer's address", records)
	}
}

// Every attempt leaves a record, the refused ones too, and a refusal names
// nobody: the caller has not proved who it is.
func TestARefusedRecoveryAttemptIsRecorded(t *testing.T) {
	t.Parallel()
	trail := audittest.New(t)
	handler := recoveryServer(t, trail, 0)

	request := httptest.NewRequest(http.MethodPost, "/login/recovery", nil)
	request.Header.Set("Authorization", "Bearer not-the-proof")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("a wrong proof = %d, cookie %q", response.Code, response.Header().Get("Set-Cookie"))
	}
	written := trail.Records()
	if len(written) != 1 || written[0].GetAction() != "roster.recovery.signed_in" ||
		written[0].GetOutcome().GetResult() != auditv1.Outcome_RESULT_DENIED ||
		written[0].GetOutcome().GetReason() != reasonRecoveryRefused || written[0].GetActor().GetKind() != "anonymous" {
		t.Fatalf("written = %v %v, want one denied recovery record", trail.Actions(), written)
	}
	if strings.Contains(written[0].String(), "not-the-proof") {
		t.Error("the record carries the proof")
	}
}

// recovery.enabled: false keeps the credential and refuses the sign-in, with
// a message that says why, and the attempt is recorded.
func TestRecoveryTurnedOffRefusesWithAMessageAndARecord(t *testing.T) {
	t.Parallel()
	trail := audittest.New(t)
	console := githubConsole(t, nil)
	console.deps.Audit = trail
	sessions, err := access.NewSessions(make([]byte, access.SessionKeyBytes), time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewConsoleServer(ConsoleServerDeps{
		Console: console, Authorizer: console.deps.Authorizer, Sessions: sessions, Recovery: nil,
		Log: slog.New(slog.DiscardHandler),
	}).Handler()

	request := httptest.NewRequest(http.MethodPost, "/login/recovery", nil)
	request.Header.Set("Authorization", "Bearer the-proof")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusForbidden || !strings.Contains(body, "turned off") || !strings.Contains(body, "recovery.enabled") {
		t.Errorf("recovery turned off = %d %q, want 403 saying it is turned off", response.Code, body)
	}
	if response.Header().Get("Set-Cookie") != "" {
		t.Error("a refused recovery set a session")
	}
	written := trail.Records()
	if len(written) != 1 || written[0].GetOutcome().GetResult() != auditv1.Outcome_RESULT_DENIED ||
		written[0].GetOutcome().GetReason() != reasonRecoveryOff {
		t.Errorf("written = %v, want one denied record saying it is off", trail.Actions())
	}
}

// A refusal that costs nothing is anybody's to send in a loop: it is recorded
// once a window with a count, while a real check is recorded every time.
func TestCheapRecoveryRefusalsAreRecordedOncePerWindow(t *testing.T) {
	t.Parallel()
	var c cheapRefusals
	start := time.Now()
	if n, ok := c.note("off", start); !ok || n != 1 {
		t.Fatalf("first = %d, %v", n, ok)
	}
	for i := 1; i <= 5; i++ {
		if _, ok := c.note("off", start.Add(time.Duration(i)*time.Second)); ok {
			t.Fatalf("attempt %d inside the window was recorded", i)
		}
	}
	if _, ok := c.note("throttled", start.Add(time.Second)); !ok {
		t.Error("another reason shares the window")
	}
	if n, ok := c.note("off", start.Add(cheapWindow)); !ok || n != 6 {
		t.Errorf("next window = %d, %v, want one record standing for 6", n, ok)
	}

	trail := audittest.New(t)
	handler := recoveryServer(t, trail, 0)
	for range 3 {
		recoverThroughConsole(handler)
	}
	if len(trail.Records()) != 3 {
		t.Errorf("real checks recorded %d times, want 3", len(trail.Records()))
	}
}
