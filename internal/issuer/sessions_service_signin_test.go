package issuer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	accessissuerv1 "github.com/truvity/sluis/gen/accessissuer/v1"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// The session service lists every session and revokes anyone's, for an
// operator. It reads a browser's sign-in cookie, and it used to ask only
// whether that cookie named a live sign-in: a sign-in past the absolute
// limit, or one whose person the directory had since suspended, kept that
// power until the record expired, and a suspended person still listed and
// revoked their own as themselves. It now asks the issuer's own question,
// the one the silent sign-in and the console ask.

const (
	accountOperator = "root@north.example"
	accountVictim   = "eli@south.example"
	accountRecovery = "system:serviceaccount:sluis:recovery"
)

// accountsPolicy makes the directory's admins operators, and the recovery
// ServiceAccount too, as an installation that recovers through the console
// does.
const accountsPolicy = `
version: 1
groups:
  all:access-roster:operator:
    members: [directory-admins@north.example]
    matchers:
      - service_account: { namespace: sluis, name: recovery }
`

type accountsRig struct {
	iss *issuer.Issuer
	dir *fakeDirectory
	svc *issuer.SessionsService
}

func newAccountsRig(t *testing.T, cfg issuer.Config) *accountsRig {
	t.Helper()
	declared, err := policy.Parse([]byte(accountsPolicy))
	if err != nil {
		t.Fatalf("parse the policy: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("policy set: %v", err)
	}
	dir := &fakeDirectory{standing: map[string]issuer.Standing{
		accountOperator: {Found: true, Authoritative: true, Groups: []string{"directory-admins@north.example"}},
		accountVictim:   {Found: true, Authoritative: true},
	}}
	cfg.URL = "https://issuer.example"
	iss := issuer.New(cfg, set, dir, issuer.NewMemoryState())
	return &accountsRig{iss: iss, dir: dir, svc: issuer.NewSessionsServiceForCookieTest(iss, verifier())}
}

// signIn begins a sign-in made by how, authenticated at authTime, and
// returns its id and the cookie's secret.
func (g *accountsRig) signIn(t *testing.T, identity, how string, authTime time.Time) (id, secret string) {
	t.Helper()
	g.iss.SSO().SetClock(func() time.Time { return authTime })
	defer g.iss.SSO().SetClock(time.Now)
	session, secret, err := g.iss.SSO().Begin(context.Background(), identity, how)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	return session.ID, secret
}

func (g *accountsRig) list(secret string, req *accessissuerv1.ListSessionsRequest) error {
	r := connect.NewRequest(req)
	r.Header().Set("Cookie", issuer.SSOCookieName+"="+secret)
	_, err := g.svc.ListSessions(context.Background(), r)
	return err
}

func (g *accountsRig) revoke(secret string, req *accessissuerv1.RevokeSessionsRequest) (int32, error) {
	r := connect.NewRequest(req)
	r.Header().Set("Cookie", issuer.SSOCookieName+"="+secret)
	got, err := g.svc.RevokeSessions(context.Background(), r)
	if err != nil {
		return 0, err
	}
	return got.Msg.GetEnded(), nil
}

func (g *accountsRig) live(t *testing.T, id string) bool {
	t.Helper()
	_, live, err := g.iss.SSO().Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return live
}

// An operator's sign-in past the absolute limit lists nothing and ends
// nobody's.
func TestTheSessionServiceRefusesASignInPastTheAbsoluteLimit(t *testing.T) {
	t.Parallel()
	g := newAccountsRig(t, issuer.Config{AbsoluteLifetime: time.Hour})
	_, stale := g.signIn(t, accountOperator, "google", time.Now().Add(-2*time.Hour))
	victim, _ := g.signIn(t, accountVictim, "google", time.Now())

	if err := g.list(stale, &accessissuerv1.ListSessionsRequest{}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("listing every session past the limit: %v, want unauthenticated", err)
	}
	if err := g.list(stale, &accessissuerv1.ListSessionsRequest{Identity: accountOperator}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("listing their own past the limit: %v, want unauthenticated", err)
	}
	_, err := g.revoke(stale, &accessissuerv1.RevokeSessionsRequest{Identity: accountVictim, Sso: victim})
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("revoking somebody's sign-in past the limit: %v, want unauthenticated", err)
	}
	if !g.live(t, victim) {
		t.Error("a sign-in past the limit ended somebody else's sign-in")
	}
	if g.dir.calls != 0 {
		t.Errorf("the directory was asked %d times about a sign-in past the limit", g.dir.calls)
	}
}

// A person the directory no longer admits is not even themselves here:
// the cookie proves nobody, where it used to fall back to the person
// without their groups.
func TestTheSessionServiceRefusesAPersonTheDirectoryNoLongerAdmits(t *testing.T) {
	t.Parallel()
	for name, standing := range map[string]issuer.Standing{
		"suspended": {Found: true, Suspended: true, Authoritative: true, Groups: []string{"directory-admins@north.example"}},
		"removed":   {Found: false, Authoritative: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			g := newAccountsRig(t, issuer.Config{})
			g.dir.standing[accountOperator] = standing
			own, secret := g.signIn(t, accountOperator, "google", time.Now())
			victim, _ := g.signIn(t, accountVictim, "google", time.Now())

			if err := g.list(secret, &accessissuerv1.ListSessionsRequest{Identity: accountOperator}); connect.CodeOf(err) != connect.CodeUnauthenticated {
				t.Errorf("listing their own: %v, want unauthenticated", err)
			}
			if err := g.list(secret, &accessissuerv1.ListSessionsRequest{}); connect.CodeOf(err) != connect.CodeUnauthenticated {
				t.Errorf("listing every session: %v, want unauthenticated", err)
			}
			for _, target := range []struct{ identity, sso string }{{accountOperator, own}, {accountVictim, victim}} {
				_, err := g.revoke(secret, &accessissuerv1.RevokeSessionsRequest{Identity: target.identity, Sso: target.sso})
				if connect.CodeOf(err) != connect.CodeUnauthenticated {
					t.Errorf("revoking %s's sign-in: %v, want unauthenticated", target.identity, err)
				}
			}
			if !g.live(t, victim) {
				t.Error("a person the directory refused ended somebody else's sign-in")
			}
		})
	}
}

// The same hold window as the silent sign-in: an unreachable directory is
// answered from the last-known groups inside it, so an operator keeps the
// service, and someone nothing is held for is refused.
func TestTheSessionServiceHoldsThroughAnUnreachableDirectory(t *testing.T) {
	t.Parallel()
	g := newAccountsRig(t, issuer.Config{})
	_, operator := g.signIn(t, accountOperator, "google", time.Now())
	_, victim := g.signIn(t, accountVictim, "google", time.Now())
	if err := g.list(operator, &accessissuerv1.ListSessionsRequest{}); err != nil {
		t.Fatalf("an admitted operator was refused: %v", err)
	}

	g.dir.err = errors.New("the directory did not answer")
	if err := g.list(operator, &accessissuerv1.ListSessionsRequest{}); err != nil {
		t.Errorf("inside the hold window an operator was refused: %v", err)
	}
	if err := g.list(victim, &accessissuerv1.ListSessionsRequest{Identity: accountVictim}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("someone with nothing held: %v, want unauthenticated", err)
	}
}

// Somebody admitted, within the limit, keeps exactly what they had: their
// own sessions, and an operator everybody's.
func TestTheSessionServiceAdmitsASignInThatStands(t *testing.T) {
	t.Parallel()
	g := newAccountsRig(t, issuer.Config{AbsoluteLifetime: time.Hour})
	_, operator := g.signIn(t, accountOperator, "google", time.Now().Add(-30*time.Minute))
	victim, own := g.signIn(t, accountVictim, "google", time.Now())

	if err := g.list(own, &accessissuerv1.ListSessionsRequest{Identity: accountVictim}); err != nil {
		t.Errorf("a person listing their own: %v", err)
	}
	if err := g.list(own, &accessissuerv1.ListSessionsRequest{}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a person listing everybody's: %v, want permission denied", err)
	}
	if err := g.list(operator, &accessissuerv1.ListSessionsRequest{}); err != nil {
		t.Errorf("an operator listing every session: %v", err)
	}
	if _, err := g.revoke(operator, &accessissuerv1.RevokeSessionsRequest{Identity: accountVictim, Sso: victim}); err != nil {
		t.Errorf("an operator revoking a sign-in: %v", err)
	}
	if g.live(t, victim) {
		t.Error("the operator's revocation left the sign-in live")
	}
	if g.dir.calls != 4 {
		t.Errorf("the directory was asked %d times for four calls, want once each", g.dir.calls)
	}
}

// Recovery is known by how the sign-in was made, not by its subject: a
// provider's sign-in whose subject reads as the recovery ServiceAccount is
// asked of the directory and never evaluated as that ServiceAccount, and
// a real recovery sign-in is, with no directory to ask.
func TestTheSessionServiceKnowsRecoveryByHowItWasMade(t *testing.T) {
	t.Parallel()

	t.Run("a provider's look-alike", func(t *testing.T) {
		t.Parallel()
		g := newAccountsRig(t, issuer.Config{})
		_, secret := g.signIn(t, accountRecovery, "google", time.Now())
		if err := g.list(secret, &accessissuerv1.ListSessionsRequest{}); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("a look-alike the directory does not know: %v, want unauthenticated", err)
		}
		if g.dir.calls != 1 {
			t.Errorf("the directory was asked %d times, want once", g.dir.calls)
		}

		// Even one the directory admits is a person, evaluated by the
		// directory's groups: not the recovery account's operator role.
		g.dir.standing[accountRecovery] = issuer.Standing{Found: true, Authoritative: true}
		if err := g.list(secret, &accessissuerv1.ListSessionsRequest{}); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("an admitted look-alike listing everybody's: %v, want permission denied", err)
		}
	})

	t.Run("a recovery sign-in", func(t *testing.T) {
		t.Parallel()
		g := newAccountsRig(t, issuer.Config{AbsoluteLifetime: time.Hour})
		g.dir.err = errors.New("the directory did not answer")
		_, secret := g.signIn(t, accountRecovery, issuer.RecoveryHow, time.Now())
		if err := g.list(secret, &accessissuerv1.ListSessionsRequest{}); err != nil {
			t.Errorf("recovery listing every session: %v", err)
		}
		if g.dir.calls != 0 {
			t.Errorf("the directory was asked %d times about a recovery sign-in", g.dir.calls)
		}

		_, stale := g.signIn(t, accountRecovery, issuer.RecoveryHow, time.Now().Add(-2*time.Hour))
		if err := g.list(stale, &accessissuerv1.ListSessionsRequest{}); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("recovery past the limit: %v, want unauthenticated", err)
		}
	})
}
