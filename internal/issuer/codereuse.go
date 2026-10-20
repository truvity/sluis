package issuer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/storage/logattr"
)

// An authorization code is spent once (RFC 6749 4.1.2): a second
// presentation is denied, and what the first one issued is revoked, because
// a code presented twice is a code somebody else holds and the tokens of its
// first use are the ones in doubt.
//
// The first redemption leaves a tombstone of the code once its tokens are
// minted: which access token it produced and which session (the refresh
// token family) it opened. A second presentation reads the tombstone and
// revokes both, so `userinfo` refuses the access token and every refresh
// token of the session is dead.
//
// The tombstone is also what makes one redemption win. It is written with
// the store's conditional create (DynamoDB's conditional PutItem, Valkey's
// SET NX), so of two redemptions of one code racing each other -- both read
// the request before either deleted it -- exactly one writes it. The other
// withdraws what it minted, revokes what the winner minted, and is refused:
// that is a reuse too, and which presentation is the thief's cannot be told.
//
// Writing it after the minting rather than before keeps a code's first
// redemption at one write more than the tokens themselves, the one the note
// of the session it opened used to cost, and lets a redemption that fails
// before minting anything (a wrong verifier, a client that does not match)
// leave the code redeemable by its rightful client.

// spentCode is the tombstone of one redeemed authorization code. It holds
// ids only: never the code, never a token.
type spentCode struct {
	// Client is the client the code was issued to, for the log line a
	// reuse leaves.
	Client string `json:"client"`
	// Token is the id (`jti`) of the access token the redemption minted.
	Token string `json:"token,omitempty"`
	// Session is the session it opened, empty when it opened none (a
	// client that did not ask for `offline_access`).
	Session string `json:"session,omitempty"`
}

// codeSpentKey is where a code's tombstone lives: under the code's SHA-256,
// so a store that keeps the tombstone longer than the code lived never holds
// the code itself.
func codeSpentKey(code string) string {
	sum := sha256.Sum256([]byte(code))
	return "issuer:code-spent:" + hex.EncodeToString(sum[:])
}

// The reuse outcomes a refusal's log line carries as `revocation`.
const (
	// reuseRevoked: something the code issued was live, and is no longer.
	reuseRevoked = "revoked"
	// reuseNothingLive: the tombstone was found and what it names had
	// already gone (an earlier reuse revoked it, or it expired).
	reuseNothingLive = "nothing_live"
	// reuseFailed: the store failed part-way; what was not reached is
	// still live.
	reuseFailed = "failed"
	// reuseNoRecord: the code's request is gone and no tombstone names what
	// it issued, as for a code redeemed by a version before tombstones.
	reuseNoRecord = "no_record"
)

var errCodeReused = errors.New("the authorization code was already redeemed")

// spend writes the tombstone of the code request was redeemed with, once
// issued is minted and kept. A request not redeemed by a code (an implicit
// grant, a device flow) has none to write.
//
// It returns the refusal the token endpoint answers with when another
// redemption of the same code wrote the tombstone first, and a server error
// when the store could not say.
func (s *Storage) spend(ctx context.Context, request *authRequest, issued *token) error {
	if request.spent == "" || maintenance.ReadOnly(ctx) {
		return nil
	}

	raw, err := json.Marshal(spentCode{Client: request.Req.ClientID, Token: issued.ID, Session: issued.Session})
	if err != nil {
		return fault(ctx, fmt.Errorf("issuer: encode a spent code: %w", err))
	}

	// As long as the access token it names lives, and never shorter than
	// the code itself: a reuse after that has nothing left to reach but the
	// session, and is refused as an unknown code is.
	ttl := max(time.Until(issued.Expires), authCodeTTL)

	won, err := s.state.SetIfAbsent(ctx, request.spent, raw, ttl)
	if err == nil && won {
		return nil
	}

	// Lost, or unknown whether it was won: either way what this redemption
	// minted is never handed out, so it is withdrawn.
	s.withdraw(ctx, spentCode{Token: issued.ID, Session: issued.Session})
	if err != nil {
		return fault(ctx, err)
	}

	return s.reuseCode(ctx, request.spent)
}

// refuseSpentCode answers a code that resolves to no live request: its
// request was deleted by the redemption that spent it, or the code itself
// has expired. A tombstone means it was redeemed, and this is a reuse.
func (s *Storage) refuseSpentCode(ctx context.Context, code string, requestGone bool) error {
	spent, err := getJSON[spentCode](ctx, s.state, codeSpentKey(code))
	if err != nil {
		return err
	}

	if spent != nil {
		return s.refuseReuse(ctx, spent, nil)
	}

	if requestGone {
		// Known to the store and its request gone, with no tombstone: a
		// code redeemed by a version that wrote none. Refused, and nothing
		// is known to revoke.
		recordReuse(ctx, "authorization_code")

		return deny(ctx, oidc.ErrInvalidGrant, errCodeReused,
			slog.Int("status", http.StatusBadRequest), slog.String("revocation", reuseNoRecord))
	}

	return errors.New("no such authorization code")
}

// reuseCode revokes what the code whose tombstone is under key issued, and
// refuses the grant with invalid_grant.
//
// Idempotent: a second reuse finds the access token and the session already
// gone and revokes nothing. The refusal is the same whatever the revocation
// did, so that a caller presenting a stolen code learns nothing from it; what
// it did is the `revocation` of the refusal's log line.
func (s *Storage) reuseCode(ctx context.Context, key string) error {
	spent, err := getJSON[spentCode](ctx, s.state, key)
	return s.refuseReuse(ctx, spent, err)
}

// refuseReuse is [Storage.reuseCode] given the tombstone as it was read: nil
// when it has gone, or err when it could not be read.
func (s *Storage) refuseReuse(ctx context.Context, spent *spentCode, err error) error {
	recordReuse(ctx, "authorization_code")

	outcome := reuseNothingLive
	client := ""
	switch {
	case err != nil:
		outcome = reuseFailed
		s.logger().WarnContext(ctx, "an authorization code was reused and what it issued could not be read",
			logattr.SafeError("error", err))
	case spent != nil:
		client = spent.Client
		outcome = s.revokeSpent(ctx, *spent)
	}

	return deny(ctx, oidc.ErrInvalidGrant, errCodeReused,
		slog.Int("status", http.StatusBadRequest), slog.String("revocation", outcome),
		logattr.SafeString("issued_to", client))
}

// revokeSpent ends what one redemption issued: its access token's record,
// which `userinfo` answers from, and the session it opened, with every
// refresh token of it. A client that declared back-channel logout is told.
func (s *Storage) revokeSpent(ctx context.Context, spent spentCode) string {
	revoked, ended := false, false

	if spent.Token != "" {
		_, found, err := s.state.Get(ctx, tokenKey(spent.Token))
		if err == nil && found {
			err = s.state.Delete(ctx, tokenKey(spent.Token))
			revoked = err == nil
		}
		if err != nil {
			s.logger().WarnContext(ctx, "an authorization code was reused and its access token could not be revoked",
				logattr.SafeError("error", err))

			return reuseFailed
		}
	}

	if spent.Session != "" {
		session, live, err := s.iss.Sessions().ByID(ctx, spent.Session)
		if err == nil && live {
			ended, err = s.iss.Sessions().RevokeID(ctx, spent.Session)
			if err == nil && ended {
				revoked = true
				s.announceLogout(ctx, s.logger(), []Session{session})
			}
		}
		if err != nil {
			s.logger().WarnContext(ctx, "an authorization code was reused and its session could not be ended",
				logattr.SafeError("error", err))

			return reuseFailed
		}
	}

	if !revoked {
		return reuseNothingLive
	}

	// WARN and not INFO: a code presented twice is either a broken client
	// or a stolen code, and both are worth seeing in a log.
	s.logger().WarnContext(ctx, "an authorization code was reused; what it issued has been revoked",
		logattr.SafeString("client_id", spent.Client), slog.Bool("session_ended", ended))

	return reuseRevoked
}

// withdraw removes what a redemption minted and is not handing out, best
// effort: the redemption is refused either way, and a failure here leaves a
// token nobody was given.
func (s *Storage) withdraw(ctx context.Context, minted spentCode) {
	if minted.Token != "" {
		if err := s.state.Delete(ctx, tokenKey(minted.Token)); err != nil {
			s.logger().WarnContext(ctx, "a refused redemption's access token could not be withdrawn",
				logattr.SafeError("error", err))
		}
	}

	if minted.Session != "" {
		if _, err := s.iss.Sessions().RevokeID(ctx, minted.Session); err != nil {
			s.logger().WarnContext(ctx, "a refused redemption's session could not be withdrawn",
				logattr.SafeError("error", err))
		}
	}
}
