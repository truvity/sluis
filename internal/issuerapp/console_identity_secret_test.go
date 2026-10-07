package issuerapp

import (
	"testing"
	"time"

	"github.com/truvity/sluis/internal/issuer"
)

// The console reads the browser's cookie and nothing else: the sign-in's id,
// which the console itself lists and links, signs nobody in.
func TestTheConsoleIsNotSignedInByASignInID(t *testing.T) {
	t.Parallel()

	c := newConsoleUnderTest(t, issuer.Config{})
	id, secret := c.signIn(t, consolePerson, time.Now())

	asking := func(value string) (string, bool) {
		who, ok, _ := c.ask(value)
		return who.Subject, ok
	}

	if who, ok := asking(secret); !ok || who != consolePerson {
		t.Errorf("the real cookie: %q, %v", who, ok)
	}

	if who, ok := asking(id); ok {
		t.Errorf("the sign-in id signed %q in to the console", who)
	}

	// The cookie of a sign-in that has ended signs nobody in either.
	if err := c.iss.SSO().End(t.Context(), id); err != nil {
		t.Fatal(err)
	}

	if who, ok := asking(secret); ok {
		t.Errorf("an ended sign-in's cookie signed %q in", who)
	}
}
