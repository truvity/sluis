package issuer_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/issuer"
)

// A token's subject is evaluated as a ServiceAccount only when the sign-in
// was MADE as one -- by recovery, which the sign-in records -- and never
// because the subject string reads like one. A subject is whatever an
// identity provider said; one that happens to read as the recovery
// ServiceAccount was handed what the policy grants that ServiceAccount
// (here, the operators' group) in every token and every refresh.

const lookAlike = "system:serviceaccount:sluis:recovery"

// lookAlikePolicy grants the recovery ServiceAccount the operators' group
// and the client, as an installation that recovers through the console
// does, and grants the client to engineering.
const lookAlikePolicy = `
version: 1
groups:
  devel:k8s:viewer:
    members: [engineering@north.example]
    matchers:
      - service_account: { namespace: sluis, name: recovery }
  all:access-roster:operator:
    matchers:
      - service_account: { namespace: sluis, name: recovery }
clients:
  local-dev:
    kind: public
    redirects: [http://localhost:8000/callback]
    requires: [devel:k8s:viewer]
`

const operators = "all:access-roster:operator"

// groupsIn reads a JWT's `groups`.
func groupsIn(t *testing.T, raw string) []string {
	t.Helper()
	values, _ := payloadOf(t, raw)["groups"].([]any)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if name, ok := value.(string); ok {
			out = append(out, name)
		}
	}
	return out
}

// refreshLocalDev posts one refresh and returns the status and the body.
func refreshLocalDev(t *testing.T, server string, token string) (int, map[string]any) {
	t.Helper()
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {token},
		"client_id":     {"local-dev"},
	}
	response, err := http.Post(server+"/token", //nolint:noctx // a test
		"application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("token response: %v", err)
	}
	return response.StatusCode, body
}

// tokenGroups checks the access and ID tokens in a token response.
func tokenGroups(t *testing.T, when string, body map[string]any, want string, refused string) {
	t.Helper()
	for _, kind := range []string{"access_token", "id_token"} {
		raw, _ := body[kind].(string)
		if raw == "" {
			t.Errorf("%s: no %s", when, kind)
			continue
		}
		groups := groupsIn(t, raw)
		if want != "" && !slices.Contains(groups, want) {
			t.Errorf("%s: the %s carries %v, want %s", when, kind, groups, want)
		}
		if refused != "" && slices.Contains(groups, refused) {
			t.Errorf("%s: the %s carries %v, which holds %s", when, kind, groups, refused)
		}
	}
}

// A provider's sign-in whose subject reads as the recovery ServiceAccount
// is a person: the directory's groups, in the tokens from the code and in
// every refresh, and never the ServiceAccount's.
func TestAProviderSubjectThatReadsAsAServiceAccountIsEvaluatedAsAPerson(t *testing.T) {
	t.Parallel()
	server, _ := signInServerWith(t, lookAlike, lookAlikePolicy)
	b := newBrowser(t, server)
	b.signIn()

	tokens := redeem(t, b, b.authorizeWith(map[string]string{"scope": "openid offline_access"}, ""))
	tokenGroups(t, "the code's tokens", tokens, "devel:k8s:viewer", operators)

	refresh, _ := tokens["refresh_token"].(string)
	if refresh == "" {
		t.Fatal("offline_access was issued no refresh token")
	}
	status, renewed := refreshLocalDev(t, server.URL, refresh)
	if status != http.StatusOK {
		t.Fatalf("refresh: %d %v", status, renewed)
	}
	tokenGroups(t, "the refresh's tokens", renewed, "devel:k8s:viewer", operators)
}

// A session opened by recovery refreshes as its ServiceAccount, by the
// method it recorded, with no directory to ask; a session that recorded
// no method (one opened before the field existed) is a person, and the
// directory, which knows no such person, refuses it; an exchange's
// session keeps the subject this issuer minted from its verified proof.
func TestARefreshIsEvaluatedByHowTheSessionBegan(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		opened  issuer.Opened
		granted bool
	}{
		"recovery": {
			opened:  issuer.Opened{How: issuer.HowCode, Method: issuer.RecoveryHow},
			granted: true,
		},
		"a provider's look-alike": {
			opened: issuer.Opened{How: issuer.HowCode, Method: "google"},
		},
		"recorded before the method was": {
			opened: issuer.Opened{How: issuer.HowCode},
		},
		"an exchange": {
			opened:  issuer.Opened{How: issuer.HowExchange},
			granted: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server, iss := signInServerWith(t, "ada@north.example", lookAlikePolicy)
			opened := tc.opened
			opened.Identity, opened.ClientID, opened.Token = lookAlike, "local-dev", "refresh-"+strings.ReplaceAll(name, " ", "-")
			opened.Scopes = []string{"openid"}
			if _, err := iss.Sessions().Record(t.Context(), opened); err != nil {
				t.Fatalf("record: %v", err)
			}

			status, body := refreshLocalDev(t, server.URL, opened.Token)
			if !tc.granted {
				if status == http.StatusOK {
					t.Errorf("refreshed as %v; a person the directory does not know is refused", groupsIn(t, body["access_token"].(string)))
				}
				return
			}
			if status != http.StatusOK {
				t.Fatalf("refresh: %d %v", status, body)
			}
			tokenGroups(t, "the refresh's tokens", body, operators, "")
		})
	}
}

// A person's exchange session is a person's, even when their address
// parses as a ServiceAccount: `k8s:<ns>:<name>` with an `@` in the name is
// an address, and the directory decides it at every refresh.
func TestAPersonsExchangeSessionIsAPersonWhateverTheirAddress(t *testing.T) {
	t.Parallel()
	const address = "k8s:sluis:ops@corp.example"
	server, iss := signInServerWith(t, address, lookAlikePolicy)
	if _, err := iss.Sessions().Record(t.Context(), issuer.Opened{
		Identity: address, ClientID: "local-dev", How: issuer.HowExchange,
		Token: "refresh-exchange-address", Scopes: []string{"openid"},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	status, body := refreshLocalDev(t, server.URL, "refresh-exchange-address")
	if status != http.StatusOK {
		t.Fatalf("refresh: %d %v; want the directory's groups for the person", status, body)
	}
	tokenGroups(t, "the refresh's tokens", body, "devel:k8s:viewer", operators)
}
