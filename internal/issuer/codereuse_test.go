package issuer_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// Reusing an authorization code ends the session it opened.
//
// RFC 6749 4.1.2: a code used more than once must be denied and SHOULD
// revoke the tokens already issued from it. Denying alone leaves the
// first redemption working while telling us the code was stolen —
// conformance saw it as a resource endpoint answering 200 where it
// wanted a 4xx.
//
// PKCE already defeats most code interception here, so this is defence
// in depth. It is also the cheap half: the code is presented twice, and
// that is all the signal needed.
func TestReusingACodeEndsTheSessionItOpened(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatalf("policy: %v", err)
	}

	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("policy set: %v", err)
	}

	shared := issuer.NewMemoryState()
	iss := issuer.New(issuer.Config{URL: "https://issuer.example"}, set, &fakeDirectory{
		standing: map[string]issuer.Standing{
			"ada@north.example": {Found: true, Authoritative: true, Groups: []string{"directory-admins@north.example"}},
		},
	}, shared)

	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, shared)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}

	// A sign-in, a code, and the first redemption.
	request, err := storage.CreateAuthRequestForTest(ctx, "ada@north.example", "console")
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	if err = storage.SaveAuthCode(ctx, request, "the-code"); err != nil {
		t.Fatalf("SaveAuthCode: %v", err)
	}

	found, err := storage.AuthRequestByCode(ctx, "the-code")
	if err != nil {
		t.Fatalf("first redemption: %v", err)
	}

	if _, _, _, err = storage.CreateAccessAndRefreshTokens(ctx, found, ""); err != nil {
		t.Fatalf("CreateAccessAndRefreshTokens: %v", err)
	}

	open, err := iss.Sessions().List(ctx, issuer.Query{Identity: "ada@north.example"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(open) != 1 {
		t.Fatalf("sessions after the first redemption = %d, want 1", len(open))
	}

	// The library deletes the request once the code is spent, which is
	// what makes a second presentation recognisable.
	if err = storage.DeleteAuthRequest(ctx, request); err != nil {
		t.Fatalf("DeleteAuthRequest: %v", err)
	}

	// The second presentation: denied, and the session goes with it.
	if _, err = storage.AuthRequestByCode(ctx, "the-code"); err == nil {
		t.Error("a reused code was accepted")
	}

	open, err = iss.Sessions().List(ctx, issuer.Query{Identity: "ada@north.example"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(open) != 0 {
		t.Errorf("sessions after the reuse = %d, want 0: the tokens that code issued are in doubt", len(open))
	}
}

// A revoked session's access token stops answering at userinfo.
//
// This is the half RFC 6749 4.1.2 asks for that was missing: a reused
// code revoked the session and the access token from the FIRST
// redemption went on answering, because an access token is a JWT and
// nothing consults the store to honour one. Userinfo does hold the
// record, so it is the one place a revocation can reach a token already
// in circulation — and fixing it only for code reuse would have left
// every other revocation with the same hole.
func TestUserinfoRefusesARevokedSessionsToken(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	iss := newIssuer(t, &fakeDirectory{})

	opened, err := iss.Sessions().Record(ctx, issuer.Opened{
		Identity: "ada@north.example", ClientID: "argocd",
		How: issuer.HowCode, Token: "a-refresh-token",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	if _, live, err := iss.Sessions().ByID(ctx, opened.ID); err != nil || !live {
		t.Fatalf("the session should be live before it is revoked (live=%v, err=%v)", live, err)
	}

	if _, err := iss.Sessions().RevokeID(ctx, opened.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	_, live, err := iss.Sessions().ByID(ctx, opened.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	if live {
		t.Fatal("the session is still live after being revoked")
	}
}

// redeemCode is a sign-in and a redemption of its code, as the library makes
// it: the code resolves, the tokens are created, the request is deleted. With
// refresh the client asked for `offline_access` and a session is opened;
// without it the code buys an access token alone, as the conformance suite's
// client does.
func (r *reuseRig) redeemCode(code string, refresh bool) (access, next string) {
	r.t.Helper()
	ctx := context.Background()

	id, err := r.storage.CreateAuthRequestForTest(ctx, reusePerson, "argocd")
	if err != nil {
		r.t.Fatalf("create request: %v", err)
	}
	if err = r.storage.SaveAuthCode(ctx, id, code); err != nil {
		r.t.Fatalf("SaveAuthCode: %v", err)
	}
	request, err := r.storage.AuthRequestByCode(ctx, code)
	if err != nil {
		r.t.Fatalf("the first redemption: %v", err)
	}
	if refresh {
		access, next, _, err = r.storage.CreateAccessAndRefreshTokens(ctx, request, "")
	} else {
		access, _, err = r.storage.CreateAccessToken(ctx, request)
	}
	if err != nil {
		r.t.Fatalf("the first redemption's tokens: %v", err)
	}
	if err = r.storage.DeleteAuthRequest(ctx, id); err != nil {
		r.t.Fatalf("DeleteAuthRequest: %v", err)
	}
	return access, next
}

// presentAgain is a second presentation of a code: it must be refused.
func (r *reuseRig) presentAgain(code string) {
	r.t.Helper()
	request, err := r.storage.AuthRequestByCode(context.Background(), code)
	if err == nil {
		r.t.Fatalf("a reused code resolved to request %v", request.GetID())
	}
}

// The case the conformance suite's oidcc-codereuse-30seconds module drives: a
// client that asked for no refresh token redeems a code, calls userinfo, and
// 30 seconds later presents the code again. The access token of the first
// redemption names no session, so ending a session could never reach it: the
// tombstone names the token itself.
func TestReusingACodeRevokesTheAccessTokenItIssuedAlone(t *testing.T) {
	t.Parallel()
	r := newReuseRig(t, 0)

	access, _ := r.redeemCode("code-alone", false)
	if err := r.userinfo(access); err != nil {
		t.Fatalf("userinfo before the reuse: %v", err)
	}

	r.clock.advance(30 * time.Second)
	r.presentAgain("code-alone")

	if err := r.userinfo(access); err == nil {
		t.Fatal("userinfo answered the first redemption's access token after its code was reused")
	}
	if n := r.warnings("an authorization code was reused; what it issued has been revoked"); n != 1 {
		t.Errorf("revocation warnings = %d, want 1", n)
	}

	// Idempotent: a third presentation is refused and has nothing left
	// to revoke.
	r.presentAgain("code-alone")
	if n := r.warnings("an authorization code was reused; what it issued has been revoked"); n != 1 {
		t.Errorf("revocation warnings after a third presentation = %d, want still 1", n)
	}
}

// With `offline_access` the code also opened a session: the reuse ends it, so
// the refresh token stops refreshing and userinfo refuses every access token
// of the family, the first one and one minted by a refresh since.
func TestReusingACodeKillsTheRefreshTokenFamily(t *testing.T) {
	t.Parallel()
	r := newReuseRig(t, 0)

	first, refresh := r.redeemCode("code-family", true)
	renewed, next, err := r.refresh(refresh)
	if err != nil {
		t.Fatalf("refresh before the reuse: %v", err)
	}

	r.clock.advance(30 * time.Second)
	r.presentAgain("code-family")

	for name, access := range map[string]string{"the first access token": first, "a refreshed access token": renewed} {
		if err = r.userinfo(access); err == nil {
			t.Errorf("userinfo answered %s after the code was reused", name)
		}
	}
	if _, _, err = r.refresh(next); err == nil {
		t.Error("the refresh token family still refreshes after its code was reused")
	}
	if got := r.sessions(); len(got) != 0 {
		t.Errorf("sessions after the reuse = %d, want 0", len(got))
	}
}

// A code is good for five minutes, and its tombstone for as long as the
// access token it bought: a reuse after the code itself has expired still
// reaches that token.
func TestReusingACodeAfterItExpiredStillRevokes(t *testing.T) {
	t.Parallel()
	r := newReuseRig(t, 0)

	access, _ := r.redeemCode("code-late", false)
	r.clock.advance(6 * time.Minute)

	if err := r.userinfo(access); err != nil {
		t.Fatalf("userinfo before the reuse: %v", err)
	}
	r.presentAgain("code-late")
	if err := r.userinfo(access); err == nil {
		t.Fatal("userinfo answered after a reuse of an expired code")
	}
}

// The tombstone lives as long as the token it names and no longer, and holds
// no code: it is keyed by the code's hash and names ids only.
func TestACodesTombstoneExpiresAndHoldsNoSecret(t *testing.T) {
	t.Parallel()
	r := newReuseRig(t, 0)

	access, refresh := r.redeemCode("code-secret-canary", true)

	var tombstones []string
	for _, key := range r.state.Keys() {
		if strings.HasPrefix(key, "issuer:code-spent:") {
			tombstones = append(tombstones, key)
		}
		if strings.Contains(key, "code-secret-canary") && !strings.HasPrefix(key, "issuer:code:") {
			t.Errorf("key %q holds the code", key)
		}
	}
	if len(tombstones) != 1 {
		t.Fatalf("tombstones = %v, want one", tombstones)
	}
	raw, _, err := r.state.Get(context.Background(), tombstones[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"code-secret-canary", refresh} {
		if strings.Contains(string(raw), secret) {
			t.Error("the tombstone holds a secret")
		}
	}
	if !strings.Contains(string(raw), access) {
		t.Error("the tombstone does not name the access token's id")
	}

	// Past the access token's lifetime (an hour by default) it is gone,
	// and a presentation then is refused as an unknown code, revoking
	// nothing.
	r.clock.advance(time.Hour + time.Minute)
	if _, found, _ := r.state.Get(context.Background(), tombstones[0]); found {
		t.Fatal("the tombstone outlived the access token it names")
	}
	r.presentAgain("code-secret-canary")
	if n := r.warnings("what it issued has been revoked"); n != 0 {
		t.Errorf("a presentation after the tombstone expired revoked something (%d warnings)", n)
	}
	if got := r.sessions(); len(got) != 1 {
		t.Errorf("sessions = %d, want the session left alone once the tombstone is gone", len(got))
	}
}

// One redemption is unaffected: the access token answers and the session
// lives on after the 30 seconds the conformance module waits.
func TestASingleRedemptionIsUnaffected(t *testing.T) {
	t.Parallel()
	r := newReuseRig(t, 0)

	access, refresh := r.redeemCode("code-once", true)
	r.clock.advance(30 * time.Second)

	if err := r.userinfo(access); err != nil {
		t.Errorf("userinfo after one redemption: %v", err)
	}
	if _, _, err := r.refresh(refresh); err != nil {
		t.Errorf("refresh after one redemption: %v", err)
	}
	if n := r.warnings("authorization code was reused"); n != 0 {
		t.Errorf("one redemption logged %d reuse warnings", n)
	}
}

// Two redemptions of one code racing each other -- both resolve the code
// before either deletes the request -- cannot both succeed: the tombstone is a
// conditional create, the one that loses is refused, and what both minted is
// revoked.
func TestTwoRedemptionsRacingOneCodeLeaveNothingLive(t *testing.T) {
	t.Parallel()
	for _, refresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "access token alone", true: "with a session"}[refresh], func(t *testing.T) {
			t.Parallel()
			r := newReuseRig(t, 0)
			ctx := context.Background()

			id, err := r.storage.CreateAuthRequestForTest(ctx, reusePerson, "argocd")
			if err != nil {
				t.Fatal(err)
			}
			if err = r.storage.SaveAuthCode(ctx, id, "code-race"); err != nil {
				t.Fatal(err)
			}
			first, err := r.storage.AuthRequestByCode(ctx, "code-race")
			if err != nil {
				t.Fatal(err)
			}
			second, err := r.storage.AuthRequestByCode(ctx, "code-race")
			if err != nil {
				t.Fatal(err)
			}

			create := func(request op.AuthRequest) (string, error) {
				if refresh {
					access, _, _, err := r.storage.CreateAccessAndRefreshTokens(ctx, request, "")
					return access, err
				}
				access, _, err := r.storage.CreateAccessToken(ctx, request)
				return access, err
			}
			won, err := create(first)
			if err != nil {
				t.Fatalf("the first redemption: %v", err)
			}
			if _, err = create(second); !isInvalidGrant(err) {
				t.Fatalf("the second redemption: %v, want invalid_grant", err)
			}

			if err = r.userinfo(won); err == nil {
				t.Error("the winning redemption's access token still answers after the race was seen")
			}
			if got := r.sessions(); len(got) != 0 {
				t.Errorf("sessions after the race = %d, want 0", len(got))
			}
			for _, key := range r.state.Keys() {
				if strings.HasPrefix(key, "issuer:token:") {
					t.Errorf("an access token record survived the race: %s", key)
				}
			}
		})
	}
}
