package console_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/backend/fake"
	"github.com/truvity/sluis/internal/consolecall"
	googleconsole "github.com/truvity/sluis/internal/googleapp/console"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/modcall"
)

var (
	operator = consolecall.Caller{Actor: "ada@north.example", Roles: []string{"operator"}}
	viewer   = consolecall.Caller{Actor: "vi@north.example", Roles: []string{"viewer"}}
	nobody   = consolecall.Caller{Actor: "eve@elsewhere.example"}
)

// stubConsent is a connector whose exchange always fails, so a test can see the
// words the person is told.
type stubConsent struct{ exchange error }

func (stubConsent) Kind() string                   { return "google" }
func (stubConsent) AuthURL(string) (string, error) { return "https://consent.example", nil }
func (s stubConsent) Exchange(context.Context, string, string) (hub.Workspace, backend.Backend, error) {
	return hub.Workspace{}, nil, s.exchange
}

// stubVerifier is a connector whose client check answers as told.
type stubVerifier struct {
	stubConsent
	refuse error
}

func (s stubVerifier) VerifyClient(context.Context) error { return s.refuse }

// tenantConsent is a connector whose consent is for one tenant, to see how the
// module treats a reconnection of another.
type tenantConsent struct {
	stubConsent
	tenant *fake.Backend
	id     string
}

func (c tenantConsent) Exchange(context.Context, string, string) (hub.Workspace, backend.Backend, error) {
	return hub.Workspace{ID: c.id, Admin: "admin@" + c.id, Credential: hub.CredentialOAuth}, c.tenant, nil
}

func quietHub() *hub.Hub {
	return hub.New(hub.NewMemoryStore(), hub.NewMemorySnapshots(), hub.Config{}, slog.New(slog.DiscardHandler))
}

func service(conns ...googleconsole.Connector) *googleconsole.Service {
	return googleconsole.NewService(quietHub(), conns, nil, slog.New(slog.DiscardHandler))
}

// A consent is a real administrator's real grant. Spending one on a client the
// provider will refuse costs that person a second trip, so the check happens
// before they are sent -- and only ever refuses when the provider named the
// client, never because an answer was slow.
func TestAConsentIsNotStartedWithARefusedClient(t *testing.T) {
	t.Parallel()
	begin := func(conn googleconsole.Connector) error {
		t.Helper()
		_, err := service(conn).BeginConnect(context.Background(), operator, googleconsole.BeginRequest{Backend: "google", State: "s"})
		return err
	}

	err := begin(stubVerifier{refuse: errors.New("the OAuth client was refused")})
	var coded *modcall.Error
	if !errors.As(err, &coded) || coded.Code != googleconsole.CodeFailedPrecondition {
		t.Errorf("a consent was started with a client the provider refuses: %v", err)
	}
	// Inconclusive proceeds: a refusal here stops an operator connecting, so it
	// is made only when the provider named the client.
	if err := begin(stubVerifier{}); err != nil {
		t.Errorf("a client the check could not fault was refused anyway: %v", err)
	}
}

// The state is the console's: the module puts it in the address it builds and
// signs nothing.
func TestTheConsentAddressIsBuiltFromTheStateTheConsoleSigned(t *testing.T) {
	t.Parallel()
	got, err := service(stubConsent{}).BeginConnect(context.Background(), operator,
		googleconsole.BeginRequest{Backend: "google", State: "signed"})
	if err != nil || got.URL != "https://consent.example" {
		t.Fatalf("%+v, %v", got, err)
	}
	if _, err := service(stubConsent{}).BeginConnect(context.Background(), operator, googleconsole.BeginRequest{Backend: "google"}); err == nil {
		t.Error("a consent was begun with no state to bring the browser back with")
	}
}

// The exchange's own words must reach the person: the diagnosis was previously
// written to a 502 that the CDN replaced with its own, and survived only in the
// log. It is told as a Problem, which the console shows as a page.
func TestAFailedExchangeIsToldAsAProblemWithTheDirectorysWords(t *testing.T) {
	t.Parallel()
	_, err := service(stubConsent{exchange: errors.New("got past the authority check")}).
		CompleteConsent(context.Background(), operator, googleconsole.CompleteRequest{Backend: "google", Code: "x"})
	var told *consolecall.Problem
	if !errors.As(err, &told) {
		t.Fatalf("the failure is not a Problem: %v", err)
	}
	if !strings.Contains(told.Detail, "got past the authority check") || !strings.Contains(told.Summary, "Google") || len(told.Hints) == 0 {
		t.Errorf("the Problem does not carry what the directory said: %+v", told)
	}
}

// A reconnection is for the tenant being reconnected: a consent given by the
// account the chooser offered last is refused, and nothing is adopted.
func TestAConsentForAnotherTenantDoesNotReconnect(t *testing.T) {
	t.Parallel()
	svc := service(tenantConsent{tenant: fake.New("C0other", "other.example"), id: "C0other"})
	_, err := svc.CompleteConsent(context.Background(), operator,
		googleconsole.CompleteRequest{Backend: "google", Code: "x", Bind: "C0north"})
	var told *consolecall.Problem
	if !errors.As(err, &told) || !strings.Contains(told.Summary, "different tenant") {
		t.Fatalf("a consent for another tenant was not refused as one: %v", err)
	}
	list, err := svc.ListWorkspaces(context.Background(), operator, nil)
	if err != nil || len(list.GetWorkspaces()) != 0 {
		t.Errorf("a refused consent adopted a workspace: %v, %v", list, err)
	}
}

// Connecting a directory needs the operator role, whatever the state said: the
// module checks the snapshot it is given, and refuses in fixed words.
func TestOnlyAnOperatorCompletesAConsent(t *testing.T) {
	t.Parallel()
	for name, caller := range map[string]consolecall.Caller{"viewer": viewer, "nobody": nobody} {
		_, err := service(stubConsent{}).CompleteConsent(context.Background(), caller, googleconsole.CompleteRequest{Backend: "google", Code: "x"})
		var coded *modcall.Error
		if !errors.As(err, &coded) || coded.Code != modcall.CodeForbidden || coded.Message != googleconsole.RefusalText {
			t.Errorf("%s: %v, want the uniform refusal", name, err)
		}
	}
}
