package issuer

import (
	"context"
	"errors"
	"net/http"
)

// RecoveryGate says whether the recovery sign-in has been switched off at run
// time (docs/decisions/0075, internal/recoverygate). Off answers true for a
// door that has been closed AND for one whose state cannot be read: the way in
// that bypasses the directory stays shut when it cannot be checked.
type RecoveryGate interface {
	Off(ctx context.Context) (bool, error)
}

// errRecoveryOff is the refusal of everything a recovery sign-in opens once
// recovery has been switched off. A fresh error each time is not needed: it is
// never wrapped by the callers that return it.
var errRecoveryOff = errors.New("issuer: the recovery sign-in has been turned off")

// errRecoveryUnknown is the refusal of a recovery refresh while the switch cannot
// be read: a server error the client retries, never an invalid grant that ends
// its session.
var errRecoveryUnknown = errors.New("issuer: the recovery switch could not be read")

// ErrRecoveryOff is returned when a sign-in made by recovery is asked to
// complete, or to open a session, after an operator switched recovery off.
var ErrRecoveryOff = errRecoveryOff

// UseRecoveryGate gives the issuer the switch the console throws. Call it
// before the issuer serves; nil is a gate that is never off.
func (i *Issuer) UseRecoveryGate(g RecoveryGate) { i.recoveryGate = g }

// recoveryVerdict is what the switch says about a request that involves recovery.
type recoveryVerdict int

const (
	// recoveryOn: nothing to refuse.
	recoveryOn recoveryVerdict = iota
	// recoveryUnknown: the switch could not be read. The request is refused,
	// and NOTHING is ended: a transient fault in the State must not destroy a
	// session an operator could not have meant to.
	recoveryUnknown
	// recoveryEnded: an operator turned recovery off. The request is refused,
	// and what recovery opened may be ended.
	recoveryEnded
)

// refuse reports whether the request is refused.
func (v recoveryVerdict) refuse() bool { return v != recoveryOn }

// recoveryCheck asks the switch.
func (i *Issuer) recoveryCheck(ctx context.Context) recoveryVerdict {
	if i == nil || i.recoveryGate == nil {
		return recoveryOn
	}
	off, err := i.recoveryGate.Off(ctx)
	switch {
	case err != nil:
		return recoveryUnknown
	case off:
		return recoveryEnded
	default:
		return recoveryOn
	}
}

// RecoveryEnded counts what [Issuer.EndRecovery] ended.
type RecoveryEnded struct {
	// SignIns are browser sign-ins made by recovery.
	SignIns int
	// Sessions are the client sessions (refresh tokens) opened by recovery.
	Sessions int
	// Ended are the client sessions to tell, one per client and sign-in, for
	// [Storage.AnnounceEnded]: the revoked sessions and the clients a recovery
	// sign-in only ever issued an ID token to.
	Ended []Session
}

// EndRecovery ends every sign-in and every client session that recovery
// opened, including their refresh tokens, and reports how many.
//
// It is the sweep that follows switching recovery off. The switch itself is
// what makes recovery sessions unusable: the verdict is asked at every point a
// recovery session is read, completed or refreshed, in every container. This
// removes the records, so that nothing lists them, counts them for the audit
// record, and collects the clients to tell. It walks every session of the
// installation, which is the price of an act an operator performs once.
//
// A relying party is told by back-channel logout when its client declares one
// ([Storage.AnnounceEnded]); one that does not finds out at its next refresh.
// An access token already issued stays valid until it expires wherever it is
// verified offline.
func (i *Issuer) EndRecovery(ctx context.Context) (RecoveryEnded, error) {
	var out RecoveryEnded

	told := map[string]bool{}
	tell := func(one Session) {
		key := one.ClientID + "\x00" + one.SSO + "\x00" + one.Identity
		if !told[key] {
			told[key] = true
			out.Ended = append(out.Ended, one)
		}
	}

	if i.sso != nil {
		signIns, err := i.sso.List(ctx, "")
		if err != nil {
			return out, err
		}

		for _, signIn := range signIns {
			if !signIn.recovered() {
				continue
			}

			// Read before the sign-in goes: afterwards nothing says which
			// clients to tell.
			clients, err := i.sso.Involved(ctx, signIn.ID)
			if err != nil {
				return out, err
			}

			for _, client := range clients {
				tell(Session{ClientID: client, Identity: signIn.Identity, SSO: signIn.ID})
			}

			if err = i.sso.End(ctx, signIn.ID); err != nil {
				return out, err
			}

			out.SignIns++
		}
	}

	sessions, err := i.sessions.List(ctx, Query{})
	if err != nil {
		return out, err
	}

	for k := range sessions {
		if sessions[k].Method != RecoveryHow {
			continue
		}

		gone, err := i.sessions.RevokeID(ctx, sessions[k].ID)
		if err != nil {
			return out, err
		}

		if gone {
			out.Sessions++
			tell(sessions[k])
		}
	}

	return out, nil
}

// refuseRecoveryOff writes the page a recovery form's POST gets once recovery
// is off.
func refuseRecoveryOff(w http.ResponseWriter, r *http.Request) {
	refusalPage(w, r, http.StatusForbidden, "the recovery sign-in has been turned off", nil)
}
