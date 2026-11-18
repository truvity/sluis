//go:build sluis_test

package sessiond_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	configv2alpha1 "github.com/truvity/sluis/v2/gen/go/sluis/config/v2alpha1"
	sessionv2alpha1 "github.com/truvity/sluis/v2/gen/go/sluis/session/v2alpha1"
)

// Each audience gets the lifetime of its own client: the issuer's lifetime of the type, shortened by the client's
// ttl_cap, and never lengthened by it (ADR 0020, "Lifetimes"; v1: the lifetime of the deployment narrowed by the client's
// ttl_cap, on the ID token as on the access token). Three clients share one issuer whose ID token lives 10 minutes and
// whose access token lives 4: the first has a cap of 5 minutes, the second one of 2, the third one of 30 and the fourth
// none. The cap shortens the tokens at the sign-in and at every refresh, the response states the access token's
// lifetime, and it does not touch the chain: the refresh token lasts as long as it does for a client with no cap.
//
// verifies: REQ-TOKEN-037, REQ-TOKEN-049
func TestEachAudienceGetsTheLifetimeOfItsOwnClient(t *testing.T) {
	mk := func(id string, ttlCap time.Duration) *configv2alpha1.InstallationClient {
		c := clientOf(id)
		c.Requires = []string{testGroup}
		if ttlCap > 0 {
			c.TtlCap = durationpb.New(ttlCap)
		}
		return c
	}
	e := newEnv(t, func(o *options) {
		o.lifetime.IdToken = durationpb.New(10 * time.Minute)
		o.lifetime.AccessToken = durationpb.New(4 * time.Minute)
		o.client = mk(testClient, 5*time.Minute)
		o.extra = []*configv2alpha1.InstallationClient{mk("cap-2m", 2*time.Minute), mk("cap-30m", 30*time.Minute), mk("no-cap", 0)}
	})
	for _, c := range []struct {
		client     string
		wantID     int64
		wantAccess int64
	}{
		{testClient, 300, 240}, // the cap shortens the ID token only: the access token's own lifetime is shorter already
		{"cap-2m", 120, 120},
		{"cap-30m", 600, 240}, // a cap above the issuer's lifetime changes nothing
		{"no-cap", 600, 240},
	} {
		t.Run(c.client, func(t *testing.T) {
			resp := e.signInClient(c.client)
			sid, _, _ := strings.Cut(resp["refresh_token"].(string), ".")
			raw, _, ok := e.store.raw("session-chain", testTenant, sid)
			if !ok {
				t.Fatalf("no chain %q", sid)
			}
			ends := protoOf(t, raw, &sessionv2alpha1.SessionChain{}).GetEndsAt().AsTime()
			if want := e.clk.now().Add(12 * time.Hour); !ends.Equal(want) {
				t.Errorf("the chain ends at %s, want %s: a cap on tokens does not shorten the chain", ends, want)
			}
			req := refreshRequest(resp["refresh_token"].(string))
			req.ClientId = c.client
			r := e.token(req)
			if r.GetStatus() != http.StatusOK {
				t.Fatalf("refresh: %d %s", r.GetStatus(), r.GetBody())
			}
			for what, tokens := range map[string]map[string]any{"sign-in": resp, "refresh": body(t, r)} {
				id, at := tokenClaimsOf(t, tokens["id_token"].(string)), tokenClaimsOf(t, tokens["access_token"].(string))
				if got := num(id, "exp") - num(id, "iat"); got != c.wantID {
					t.Errorf("%s: ID token exp-iat = %d, want %d", what, got, c.wantID)
				}
				if got := num(at, "exp") - num(at, "iat"); got != c.wantAccess {
					t.Errorf("%s: access token exp-iat = %d, want %d", what, got, c.wantAccess)
				}
				if got := num(tokens, "expires_in"); got != c.wantAccess {
					t.Errorf("%s: expires_in = %d, want %d", what, got, c.wantAccess)
				}
			}
		})
	}
}
