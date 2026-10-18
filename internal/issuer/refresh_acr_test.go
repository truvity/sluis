package issuer_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// A relying party that gates on `acr` has to see it on every ID token of a
// session, not only the first. The library takes an ID token's `acr` from an
// authorization request, which a refresh does not have, so the issuer
// carries it from the session's recorded sign-in method.

func idTokenClaims(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	raw, _ := body["id_token"].(string)
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("no ID token in %v", body)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err = json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

// recoveryPolicy lets the recovery ServiceAccount into local-dev, as an
// installation that signs its operators in through recovery would.
var recoveryPolicy = strings.NewReplacer(
	"  all:gitops:builder:\n", "  all:recovery:operator:\n    matchers:\n      - service_account: { namespace: sluis, name: recovery }\n  all:gitops:builder:\n",
	"    requires: [devel:k8s:viewer]\n", "    requires: [devel:k8s:viewer, all:recovery:operator]\n",
).Replace(demo.Policy)

func refreshedIDToken(t *testing.T, identity, method string) map[string]any {
	t.Helper()
	declared, err := policy.Parse([]byte(recoveryPolicy))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set, adaDirectory(), issuer.NewMemoryState())
	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := handler(iss, storage)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	rig := &sluisctlRig{server: server, iss: iss, storage: storage}
	if _, err := rig.iss.Sessions().Record(t.Context(), issuer.Opened{
		Identity: identity, ClientID: "local-dev", How: issuer.HowCode, Method: method,
		Token: "refresh-acr", Scopes: []string{"openid", "email"}, AuthTime: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"refresh-acr"}, "client_id": {"local-dev"}, "scope": {"openid email"}}
	return idTokenClaims(t, postToken(t, rig.server, form, "local-dev", 200))
}

func TestARefreshedRecoveryIDTokenStillMarksRecovery(t *testing.T) {
	t.Parallel()
	claims := refreshedIDToken(t, "system:serviceaccount:sluis:recovery", issuer.RecoveryHow)
	if claims["acr"] != issuer.ACRRecovery {
		t.Errorf("acr = %v, want %s", claims["acr"], issuer.ACRRecovery)
	}
	amr, _ := claims["amr"].([]any)
	if len(amr) != 1 || amr[0] != "swk" {
		t.Errorf("amr = %v, want [swk]", claims["amr"])
	}
}

func TestARefreshedDirectoryIDTokenKeepsItsClassAndSaysNoPassword(t *testing.T) {
	t.Parallel()
	claims := refreshedIDToken(t, "ada@north.example", "google")
	if claims["acr"] != issuer.ACRDirectory {
		t.Errorf("acr = %v, want %s", claims["acr"], issuer.ACRDirectory)
	}
	if _, has := claims["amr"]; has {
		t.Errorf("amr = %v: the sign-in presented nothing it reports, and a refresh says no password either", claims["amr"])
	}
}
