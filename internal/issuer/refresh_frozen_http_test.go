package issuer_test

import (
	"net/http"
	"testing"

	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/maintenance"
)

// During a migration freeze a refresh renews the access token and rotates
// nothing: the caller gets the token it presented back, and that token is still
// the live one afterwards, so the session the copy carries is the one in use.
func TestAFrozenRefreshDoesNotRotate(t *testing.T) {
	t.Parallel()

	server, iss := serveIssuerFor(t, &fakeDirectory{standing: map[string]issuer.Standing{
		"ada@north.example": {Found: true, Authoritative: true, Groups: []string{"engineering@north.example"}},
	}})
	if _, err := iss.Sessions().Record(t.Context(), issuer.Opened{
		Identity: "ada@north.example", ClientID: "local-dev", How: issuer.HowCode,
		Token: "refresh-frozen", Scopes: []string{"openid", "profile", "email"},
	}); err != nil {
		t.Fatal(err)
	}

	live := server.Config.Handler
	frozen := true
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if frozen {
			r = r.WithContext(maintenance.WithReadOnly(r.Context()))
		}
		live.ServeHTTP(w, r)
	})

	for i := 0; i < 2; i++ {
		status, next := refreshWith(t, server.URL, "refresh-frozen")
		if status != http.StatusOK {
			t.Fatalf("frozen refresh %d: %d, want 200", i, status)
		}
		if next != "refresh-frozen" {
			t.Fatalf("frozen refresh %d returned %q, want the presented token back", i, next)
		}
	}

	frozen = false
	status, next := refreshWith(t, server.URL, "refresh-frozen")
	if status != http.StatusOK || next == "refresh-frozen" || next == "" {
		t.Fatalf("after the freeze: %d, %q; want 200 and a rotated token: the frozen refreshes spent nothing", status, next)
	}
}
