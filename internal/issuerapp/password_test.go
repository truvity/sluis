package issuerapp

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/internal/secretstore/secretrec"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/srp"
)

func policySet(t *testing.T, text string) *policy.Set {
	t.Helper()
	declared, err := policy.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// credentialOf is an account's credential for the password "pw", with the least
// stretching a credential may name.
func credentialOf(account string) srp.Credential {
	return srp.Default.WithIterations(srp.MinIterations).NewCredential(bytes.Repeat([]byte{7}, 32), account, "pw")
}

func v5With(t *testing.T, recovery, conformance bool) *secretstore.StoresV5 {
	t.Helper()
	v5 := secretstore.FromStoreV5(secretrec.New(), "")
	ctx := context.Background()
	if recovery {
		if _, err := v5.OIDC().RecoveryVerifier().Put(ctx, credentialOf("recovery").Document(), ""); err != nil {
			t.Fatal(err)
		}
	}
	if conformance {
		if _, err := v5.OIDC().ConformanceVerifier().Put(ctx, credentialOf("conformance").Document(), ""); err != nil {
			t.Fatal(err)
		}
	}
	return v5
}

func open(t *testing.T, cfg Config, set *policy.Set) (*issuer.PasswordDoor, error) {
	t.Helper()
	return openPassword(context.Background(), cfg, &store.Stores{}, set, access.StaticKey([]byte("a state key")), slog.New(slog.DiscardHandler))
}

const plainPolicy = `
version: 1
groups:
  all:sluis:operator:
    matchers: [{service_account: {namespace: sluis, name: recovery}}]
clients:
  rp: {kind: public, redirects: [http://localhost/cb], requires: [all:sluis:operator], harness: true}
`

// A cluster install, which keeps no verifier, builds no password door: the
// token door is its recovery, and nothing changes.
func TestNoVerifierMeansNoPasswordDoor(t *testing.T) {
	t.Parallel()
	door, err := open(t, Config{recoveryPassword: true}, policySet(t, plainPolicy))
	if err != nil || door != nil {
		t.Fatalf("door = %v, %v, want none", door, err)
	}
}

func TestTheRecoveryAccountIsOfferedWhenAVerifierIsAndRecoveryIsNotOff(t *testing.T) {
	t.Parallel()
	set := policySet(t, plainPolicy)
	door, err := open(t, Config{recoveryPassword: true, v5: v5With(t, true, false)}, set)
	if err != nil || door == nil || door.Recovery == nil || door.Conformance != nil {
		t.Fatalf("door = %+v, %v", door, err)
	}
	if door.RecoverySubject != "system:serviceaccount:sluis:recovery" {
		t.Errorf("subject = %q", door.RecoverySubject)
	}
	// The named account and namespace of a cluster, when there are some.
	door, err = open(t, Config{recoveryPassword: true, recoveryAccount: "sluis-recovery", v5: v5With(t, true, false)}, set)
	if err != nil || door.RecoverySubject != "system:serviceaccount:sluis:sluis-recovery" {
		t.Errorf("subject = %q, %v", door.RecoverySubject, err)
	}
	// Recovery turned off in the document: no account.
	if door, err = open(t, Config{recoveryPassword: false, v5: v5With(t, true, false)}, set); err != nil || door != nil {
		t.Errorf("recovery off: door = %v, %v", door, err)
	}
}

func TestTheConformanceAccountIsBuiltOnlyWhenTheFlagIsOnAndNeedsItsVerifier(t *testing.T) {
	t.Parallel()
	set := policySet(t, plainPolicy)
	// Flag off: the verifier is there and is not used.
	door, err := open(t, Config{recoveryPassword: true, v5: v5With(t, true, true)}, set)
	if err != nil || door == nil || door.Conformance != nil {
		t.Fatalf("flag off: door = %+v, %v", door, err)
	}
	// Flag on.
	door, err = open(t, Config{recoveryPassword: true, conformanceEnabled: true, v5: v5With(t, true, true)}, set)
	if err != nil || door == nil || door.Conformance == nil || door.ConformanceSubject != "system:serviceaccount:sluis:conformance" {
		t.Fatalf("flag on: door = %+v, %v", door, err)
	}
	// Flag on, nowhere to read a verifier from: the service does not start.
	if _, err = open(t, Config{recoveryPassword: true, conformanceEnabled: true}, set); err == nil ||
		!strings.Contains(err.Error(), "conformance.enabled") {
		t.Errorf("flag on with no verifier source = %v, want a refusal naming conformance.enabled", err)
	}
}

func TestTheServiceWillNotStartWithTheConformanceAccountAnOperator(t *testing.T) {
	t.Parallel()
	bad := strings.Replace(plainPolicy, "matchers: [{service_account: {namespace: sluis, name: recovery}}]",
		"matchers: [{service_account: {namespace: sluis, name: recovery}}, {service_account: {namespace: sluis, name: conformance}}]", 1)
	_, err := open(t, Config{recoveryPassword: true, conformanceEnabled: true, v5: v5With(t, true, true)}, policySet(t, bad))
	if err == nil || !strings.Contains(err.Error(), "operator") {
		t.Errorf("err = %v, want a refusal naming the operator group", err)
	}
	// With the account off the policy is the operator's business.
	if _, err = open(t, Config{recoveryPassword: true, v5: v5With(t, true, true)}, policySet(t, bad)); err != nil {
		t.Errorf("flag off: %v", err)
	}
}

// The credential a door reads is the one stored, and never the password.
func TestADoorReadsTheVerifierAndNotThePassword(t *testing.T) {
	t.Parallel()
	rec := secretrec.New()
	v5 := secretstore.FromStoreV5(rec, "")
	cred := srp.Default.WithIterations(srp.MinIterations).NewCredential(bytes.Repeat([]byte{7}, 32), "recovery", "pw")
	if _, err := v5.OIDC().RecoveryVerifier().Put(context.Background(), cred.Document(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := v5.OIDC().RecoveryPassword().Put(context.Background(), []byte("pw"), ""); err != nil {
		t.Fatal(err)
	}
	rec.Reset()
	door, err := open(t, Config{recoveryPassword: true, v5: v5}, policySet(t, plainPolicy))
	if err != nil {
		t.Fatal(err)
	}
	got, err := door.Recovery(context.Background())
	if err != nil || string(got.Verifier) != string(cred.Verifier) {
		t.Fatalf("credential = %v, %v", got, err)
	}
	for _, read := range rec.Addresses("get") {
		if strings.Contains(read, "password") {
			t.Errorf("the issuer read %s", read)
		}
	}
}

// The conformance account is the console's never: a browser signed in by it is
// nobody there.
func TestTheConsoleNeverTakesTheConformanceAccount(t *testing.T) {
	t.Parallel()
	for how, wantOK := range map[string]bool{issuer.ConformanceHow: false, issuer.RecoveryHow: true} {
		read := signedIn(func(http.ResponseWriter, *http.Request) (issuer.SSOSession, bool) {
			return issuer.SSOSession{Identity: "system:serviceaccount:sluis:" + how, How: how}, true
		})
		principal, ok := read(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		if ok != wantOK {
			t.Errorf("%s: ok = %v, want %v (%+v)", how, ok, wantOK, principal)
		}
		if ok && principal.Source != access.SourceRecovery {
			t.Errorf("%s: source = %v", how, principal.Source)
		}
	}
}
