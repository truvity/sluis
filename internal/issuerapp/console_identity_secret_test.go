package issuerapp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/truvity/sluis/internal/issuer"
)

// The console reads the browser's cookie and nothing else: the sign-in's id,
// which the console itself lists and links, signs nobody in.
func TestTheConsoleIsNotSignedInByASignInID(t *testing.T) {
	t.Parallel()

	iss := issuer.New(issuer.Config{URL: "https://access.example"}, nil, nil, issuer.NewMemoryState())
	signedIn := signedIn(iss, true)
	if signedIn == nil {
		t.Fatal("no signed-in reader for an issuer with a sign-in store")
	}

	session, secret, err := iss.SSO().Begin(t.Context(), "ada@north.example", "google")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	asking := func(value string) (string, bool) {
		r := httptest.NewRequest(http.MethodGet, "/console/", nil)
		r.AddCookie(iss.SSO().Cookie(value, true))
		who, ok := signedIn(r)

		return who.Subject, ok
	}

	if who, ok := asking(secret); !ok || who != "ada@north.example" {
		t.Errorf("the real cookie: %q, %v", who, ok)
	}

	if who, ok := asking(session.ID); ok {
		t.Errorf("the sign-in id signed %q in to the console", who)
	}

	// The cookie of a sign-in that has ended signs nobody in either.
	if err = iss.SSO().End(t.Context(), session.ID); err != nil {
		t.Fatal(err)
	}

	if who, ok := asking(secret); ok {
		t.Errorf("an ended sign-in's cookie signed %q in", who)
	}

}
