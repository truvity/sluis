package issuer_test

import (
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/issuer"
)

// A silent sign-in reads the browser's sign-in and then completes the
// request; a sign-out in another tab can land in between, or anywhere before
// the relying party redeems the code. The sign-out ends the sessions filed
// under the sign-in and then the sign-in itself, so the code must open
// nothing afterwards: a session filed under a sign-in that no longer exists
// outlives the sign-out the person just made, and no later sign-out reaches
// it. The same for a client that asked for `openid` alone, which holds no
// session but would be handed an ID token it can open a session of its own
// with, under a sign-in that will never announce its end.
func TestACodeCompletedUnderASignInThatHasSinceEndedOpensNothing(t *testing.T) {
	t.Parallel()

	for _, scope := range []string{"openid offline_access", "openid"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()

			rig := newSSORig(t, issuer.Config{})
			b, _ := rig.signedInBrowser(t)
			over := map[string]string{"scope": scope}

			// The path itself works: a code completed under a sign-in that
			// stands is redeemed.
			first := b.authorizeWith(over, "")
			if strings.Contains(first, "/login/") {
				t.Fatalf("the silent sign-in went to %q", first)
			}

			if tokens := redeem(t, b, first); tokens["id_token"] == nil {
				t.Fatalf("the first redemption issued no ID token: %v", tokens)
			}

			opened := rig.sessionsOf(ssoEmail)

			// A second request completes silently under the same sign-in...
			sentTo := b.authorizeWith(over, "")
			if strings.Contains(sentTo, "/login/") {
				t.Fatalf("the second silent sign-in went to %q", sentTo)
			}

			// ...and the person signs out in another tab before the
			// relying party redeems its code.
			tab := newBrowser(t, rig.server)
			tab.cookies = maps.Clone(b.cookies)

			if status, _, _ := tab.do(http.MethodGet, "/logout"); status != http.StatusFound {
				t.Fatalf("sign-out: %d", status)
			}

			if n := rig.sessionsOf(ssoEmail); n != 0 {
				t.Fatalf("%d sessions survived the sign-out itself", n)
			}

			status, body := redeemAnswer(t, b, sentTo)
			if status != http.StatusBadRequest || body["error"] != "invalid_grant" {
				t.Errorf("redeeming after the sign-out = %d %v, want 400 invalid_grant", status, body["error"])
			}

			if body["id_token"] != nil || body["refresh_token"] != nil {
				t.Error("the redemption after the sign-out issued an ID token or a refresh token")
			}

			if n := rig.sessionsOf(ssoEmail); n != 0 {
				t.Errorf("%d sessions opened under a sign-in that had ended (%d before the sign-out)", n, opened)
			}
		})
	}
}
