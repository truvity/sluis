package server

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/kube"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/srpauth"
	"github.com/truvity/sluis/internal/throttle"
	"github.com/truvity/sluis/srp"
)

// passwordHelper builds a password recovery over a fixed credential and an
// in-memory State, the way the console assembles one.
func newPasswordRecovery(t *testing.T, password string) *PasswordRecovery {
	t.Helper()
	p := NewPasswordRecovery(srpauth.Fixed(srp.Default.NewCredential(srpauth.RecoveryUser, password)), "")
	p.Open(memory.New(), func() ([]byte, error) { return []byte("a key every container shares"), nil })
	return p
}

// prove runs the exchange the page's script runs, from source, and returns what
// the server concluded.
func prove(t *testing.T, p *PasswordRecovery, source, user, password string) (srpauth.Proved, error) {
	t.Helper()
	ctx := context.Background()
	begun, err := p.Service.Start(ctx, source, "bound", user, p.lookup())
	if err != nil {
		return srpauth.Proved{}, err
	}
	client, err := srp.Default.NewClient(user, password, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m1, err := client.Respond(begun.Salt, begun.B)
	if err != nil {
		t.Fatal(err)
	}
	return p.Service.Finish(ctx, source, "bound", begun.Handle, client.Public(), m1, p.lookup())
}

// Outside a cluster there is nothing to prove access to, so recovery is a
// password -- proved by an exchange that never carries it, against a verifier.
func TestThePasswordRecoveryIsProvedAndNeverPresented(t *testing.T) {
	t.Parallel()

	account := newPasswordRecovery(t, "correct horse battery staple")
	if proved, err := prove(t, account, "src", "recovery", "correct horse battery staple"); err != nil || proved.User != "recovery" {
		t.Fatalf("the right password was refused: %+v, %v", proved, err)
	}
	if _, err := prove(t, account, "src", "recovery", "wrong"); !errors.Is(err, srpauth.ErrRefused) {
		t.Errorf("a wrong password = %v, want refused", err)
	}
	if _, err := prove(t, account, "src", "recovery", ""); !errors.Is(err, srpauth.ErrRefused) {
		t.Errorf("an empty password = %v, want refused", err)
	}
	// The conformance account is not the console's, and neither is any name.
	if _, err := prove(t, account, "src", "conformance", "correct horse battery staple"); !errors.Is(err, srpauth.ErrRefused) {
		t.Errorf("the conformance account signed in to the console: %v", err)
	}

	// A password is not a proof a console takes whole: the type has no Verify,
	// so no form field, body or header can carry one.
	if _, ok := Recovery(account).(ProofRecovery); ok {
		t.Error("a password recovery takes a presented proof")
	}

	// A deployment with no recovery path at all has no Recovery, and the
	// handler must see that rather than a disabled one.
	var absent Recovery
	if recoveryEnabled(absent) {
		t.Error("a deployment with no recovery path reported one")
	}
}

// Anyone who can reach the console can start an exchange. Without a ceiling the
// door becomes a way to fill the State.
func TestTheRecoveryPasswordStopsAnsweringAfterTooManyAttempts(t *testing.T) {
	t.Parallel()

	account := newPasswordRecovery(t, "the-real-one")
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	account.Service.Refusals = throttle.New(recoveryAttempts, recoveryCeiling, recoveryWindow, 0, func() time.Time { return now })

	for i := range recoveryAttempts {
		if _, err := prove(t, account, "src", "recovery", "guess"); errors.Is(err, srpauth.ErrThrottled) {
			t.Fatalf("stopped answering after %d attempts, want %d", i, recoveryAttempts)
		}
	}
	if _, err := prove(t, account, "src", "recovery", "guess"); !errors.Is(err, srpauth.ErrThrottled) {
		t.Errorf("still answering after the limit: %v", err)
	}
	// The right password is refused too while it is blocked: otherwise
	// the limit is a hint about which guess was close.
	if _, err := prove(t, account, "src", "recovery", "the-real-one"); !errors.Is(err, srpauth.ErrThrottled) {
		t.Errorf("the block is not applied to a correct password: %v", err)
	}

	now = now.Add(recoveryWindow + time.Second)
	if _, err := prove(t, account, "src", "recovery", "the-real-one"); err != nil {
		t.Errorf("the block outlived its window: %v", err)
	}

	// A success clears the count, so an operator who mistypes twice and
	// then gets it right is not one attempt from being locked out.
	for range recoveryAttempts - 1 {
		_, _ = prove(t, account, "src", "recovery", "guess")
	}
	if _, err := prove(t, account, "src", "recovery", "the-real-one"); err != nil {
		t.Errorf("failures were not cleared by a success: %v", err)
	}
}

// An identity arriving in a header is only as trustworthy as the gateway
// that sets it, which the hub cannot check. What it can check is that the
// value is an address at all — everything above routes on the domain
// after the '@', and a value carrying a line break would be written into
// an audit line as two.
func TestTheForwardedIdentityMustBeAnAddress(t *testing.T) {
	t.Parallel()

	server := &ConsoleServer{
		forwarded: ForwardedIdentity{EmailHeader: "X-Forwarded-Email", Issuer: "gateway"},
		sessions:  &access.Sessions{},
		log:       slog.New(slog.DiscardHandler),
	}
	for _, tc := range []struct {
		value string
		want  string
	}{
		{"Ada@North.Example", "ada@north.example"},
		{"  ada@north.example  ", "ada@north.example"},
		{"", ""},
		{"admin", ""},
		{"@north.example", ""},
		{"ada@", ""},
		{"ada\n@north.example", ""},
		{"ada@north.example evil", ""},
	} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.value != "" {
			request.Header.Set("X-Forwarded-Email", tc.value)
		}
		principal, ok := server.principal(httptest.NewRecorder(), request)
		switch {
		case tc.want == "" && ok:
			t.Errorf("%q was taken as the identity %q", tc.value, principal.Email)
		case tc.want != "" && (!ok || principal.Email != tc.want):
			t.Errorf("%q became %q (%v), want %q", tc.value, principal.Email, ok, tc.want)
		}
	}
}

// In a cluster there is already an authority that says who is trusted, so
// recovery proves access to it and the hub stores nothing. Three things
// have to hold, and each of them is a way in if it does not.
func TestRecoveryByClusterAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const (
		audience = "directory-roster-recovery"
		allowed  = "system:serviceaccount:directory-roster:directory-roster-recovery"
	)
	var asked []string
	recovery := &TokenRecovery{
		Audience: audience,
		Subjects: []string{allowed},
		Review: func(_ context.Context, token string, audiences []string) (string, error) {
			asked = audiences
			switch token {
			case "recovery-token":
				return allowed, nil
			case "some-other-pod":
				return "system:serviceaccount:business:web", nil
			case "unreachable":
				return "", io.ErrUnexpectedEOF
			default:
				return "", kube.ErrTokenRejected
			}
		},
	}

	// The session names who recovered, which a shared password could not.
	who, err := recovery.Verify(ctx, "recovery-token")
	if err != nil || who != allowed {
		t.Fatalf("the right token = %q, %v", who, err)
	}
	// The audience is what stops every mounted token in the cluster from
	// being a recovery token.
	if !slices.Equal(asked, []string{audience}) {
		t.Errorf("reviewed for audiences %v, want just the recovery one", asked)
	}
	// Another workload's token authenticates perfectly well and still may
	// not recover: authentication is not authorisation.
	if _, err = recovery.Verify(ctx, "some-other-pod"); !errors.Is(err, ErrRecoveryRefused) {
		t.Errorf("another account's token = %v, want refused", err)
	}
	if _, err = recovery.Verify(ctx, "nonsense"); !errors.Is(err, kube.ErrTokenRejected) {
		t.Errorf("a forged token = %v, want rejected", err)
	}
	// A check that could not run is not a refusal. Reporting it as one
	// would send an operator hunting for the wrong thing on the worst
	// possible day.
	_, err = recovery.Verify(ctx, "unreachable")
	if errors.Is(err, ErrRecoveryRefused) || err == nil {
		t.Errorf("an unreachable API server = %v, want neither success nor a refusal", err)
	}

	// The page tells a person how to obtain one, with this installation's
	// own names rather than placeholders: the alternative is guessing a
	// release name during an outage. None of it is a secret, and none of
	// it works without the RBAC to mint the token.
	recovery.Namespace, recovery.Account = "identity", "hub-recovery"
	prompt := recovery.Prompt()
	for _, want := range []string{"kubectl -n identity", "hub-recovery", "--audience " + audience, "--duration"} {
		if !strings.Contains(prompt.Command, want) {
			t.Errorf("command = %q, want it to contain %q", prompt.Command, want)
		}
	}
	// Showing it is only defensible with the warning beside it: it is an
	// instruction to mint operator access, on a page anyone may load.
	if !strings.Contains(prompt.Caution, "Never run it") {
		t.Errorf("caution = %q, want it to warn against running it on request", prompt.Caution)
	}
	// It degrades rather than printing a lie when the namespace is unknown.
	bare := &TokenRecovery{Audience: audience, Subjects: []string{allowed}}
	if got := bare.Prompt().Command; !strings.Contains(got, "<namespace>") ||
		!strings.Contains(got, "directory-roster-recovery") {
		t.Errorf("command with no namespace = %q", got)
	}

	// The password shape says something else entirely: there is nothing
	// to run, and the danger is handing it over rather than running it.
	password := NewPasswordRecovery(nil, "").Prompt()
	if password.Command != "" || !strings.Contains(password.Caution, "Never give it") {
		t.Errorf("password prompt = %+v", password)
	}
}

// What the sign-in page actually renders, for both shapes. The command is
// the interesting half: it carries this installation's own namespace and
// account, which is only defensible with the warning beside it.
func TestTheSignInPageShowsTheRealCommand(t *testing.T) {
	t.Parallel()

	page := func(recovery Recovery) string {
		sessions, err := access.NewSessions(make([]byte, access.SessionKeyBytes), time.Hour, true)
		if err != nil {
			t.Fatal(err)
		}
		server := &ConsoleServer{
			signIn:     true,
			recovery:   recovery,
			sessions:   sessions,
			state:      access.NewStateCodec(make([]byte, 32), time.Minute),
			connectors: map[string]Connector{"google": stubSignIn{}},
			log:        slog.New(slog.DiscardHandler),
		}
		recorder := httptest.NewRecorder()
		server.loginPage(recorder, httptest.NewRequest(http.MethodGet, "/login", nil))
		return recorder.Body.String()
	}

	token := page(&TokenRecovery{
		Namespace: "directory-roster", Account: "directory-roster-recovery",
		Audience: "directory-roster-recovery",
	})
	for _, want := range []string{
		"kubectl -n directory-roster create token directory-roster-recovery",
		"--audience directory-roster-recovery",
		"<pre>",
		"Never run it because someone asked you to",
		// One button per directory kind, and no company anywhere: an
		// anonymous page that lists the tenants has published them.
		"Continue with Google",
	} {
		if !strings.Contains(token, want) {
			t.Errorf("the page is missing %q", want)
		}
	}

	// The password shape has nothing to run, so it shows no command block
	// and warns about the other danger.
	password := page(NewPasswordRecovery(srpauth.Fixed(srp.Credential{}), ""))
	if strings.Contains(password, "<pre>") || strings.Contains(password, "kubectl") {
		t.Error("the password shape rendered a command block")
	}
	if !strings.Contains(password, "Never give it to anyone") {
		t.Error("the password shape lost its warning")
	}

	// A deployment with no recovery at all offers none.
	if none := page(nil); strings.Contains(none, "Recovery sign-in") {
		t.Error("a deployment with no recovery path still offered one")
	}
}

// stubSignIn is a connector that can sign somebody in, which is what
// earns a button on the page.
type stubSignIn struct{}

func (stubSignIn) Kind() string                     { return "google" }
func (stubSignIn) AuthURL(string) (string, error)   { return "https://consent.example", nil }
func (stubSignIn) SignInURL(string) (string, error) { return "https://signin.example", nil }
func (stubSignIn) Identify(context.Context, string) (string, error) {
	return "ada@north.example", nil
}
func (stubSignIn) Exchange(context.Context, string, string) (hub.Workspace, backend.Backend, error) {
	return hub.Workspace{}, nil, nil
}

// The console names this installation's own parameter, never an example's,
// and never the password.
func TestThePasswordLocationIsTheInstallationsOwn(t *testing.T) {
	r := newPasswordRecovery(t, "correct horse battery staple")
	r.Where = "ssm /sluis/example/internal/config/recovery/password"
	if got := recoveryLocationOf(r); got != r.Where {
		t.Errorf("location = %q", got)
	}
	if got := recoveryLocationOf(newPasswordRecovery(t, "generated")); got != "" {
		t.Errorf("a generated password has location %q", got)
	}
	if got := recoveryLocationOf(&TokenRecovery{}); got != "" {
		t.Errorf("token recovery has location %q", got)
	}
	if got := recoveryLocationOf(nil); got != "" {
		t.Errorf("no recovery has location %q", got)
	}
}

// The break-glass path must not be one anybody can shut. Refusals are counted
// per source, so a caller spending its attempts does not stop the operator who
// has the right password and comes from somewhere else.
func TestOneSourcesWrongProofsDoNotLockOutAnother(t *testing.T) {
	t.Parallel()

	account := newPasswordRecovery(t, "the-real-one")
	for range recoveryAttempts {
		_, _ = prove(t, account, "203.0.113.7", "recovery", "guess")
	}
	if _, err := prove(t, account, "203.0.113.7", "recovery", "guess"); !errors.Is(err, srpauth.ErrThrottled) {
		t.Fatalf("the noisy source is still answered: %v", err)
	}
	if _, err := prove(t, account, "198.51.100.20", "recovery", "the-real-one"); err != nil {
		t.Errorf("another source was locked out by the first: %v", err)
	}
}

// Many sources together are bounded too, well above what one may spend, and
// the bound lifts with its window.
func TestTheRecoveryPasswordHasAGlobalCeilingAboveAnySources(t *testing.T) {
	t.Parallel()

	account := newPasswordRecovery(t, "the-real-one")
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	account.Service.Refusals = throttle.New(recoveryAttempts, recoveryCeiling, recoveryWindow, 0, func() time.Time { return now })
	account.Service.Starts = nil

	// Each source stays under its own allowance.
	for i := range recoveryCeiling {
		source := "192.0.2." + strconv.Itoa(i)
		if _, err := prove(t, account, source, "recovery", "guess"); errors.Is(err, srpauth.ErrThrottled) {
			t.Fatalf("source %d was throttled under both limits", i)
		}
	}
	if _, err := prove(t, account, "198.51.100.99", "recovery", "the-real-one"); !errors.Is(err, srpauth.ErrThrottled) {
		t.Errorf("a source nobody has seen is answered past the ceiling: %v", err)
	}
	now = now.Add(recoveryWindow + time.Second)
	if _, err := prove(t, account, "198.51.100.99", "recovery", "the-real-one"); err != nil {
		t.Errorf("the ceiling outlived its window: %v", err)
	}
}
