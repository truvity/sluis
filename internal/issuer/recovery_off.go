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

// ErrRecoveryOff is returned when a sign-in made by recovery is asked to
// complete, or to open a session, after an operator switched recovery off.
var ErrRecoveryOff = errRecoveryOff

// UseRecoveryGate gives the issuer the switch the console throws. Call it
// before the issuer serves; nil is a gate that is never off.
func (i *Issuer) UseRecoveryGate(g RecoveryGate) { i.recoveryGate = g }

// recoveryOff reports whether recovery has been switched off.
func (i *Issuer) recoveryOff(ctx context.Context) bool {
	if i == nil || i.recoveryGate == nil {
		return false
	}
	off, _ := i.recoveryGate.Off(ctx)

	return off
}

// RecoveryEnded counts what [Issuer.EndRecovery] ended.
type RecoveryEnded struct {
	// SignIns are browser sign-ins made by recovery.
	SignIns int
	// Sessions are the client sessions (refresh tokens) opened by recovery.
	Sessions int
}

// EndRecovery ends every sign-in and every client session that recovery
// opened, including their refresh tokens, and reports how many.
//
// It is the sweep that follows switching recovery off. The switch itself is
// what makes recovery sessions unusable: [Issuer.recoveryOff] is asked at every
// point a recovery session is read, completed or refreshed, in every container.
// This removes the records, so that nothing lists them, and counts them for the
// audit record. It walks every session of the installation, which is the price
// of an act an operator performs once.
//
// A relying party is not told (no back-channel logout is sent): it finds out at
// its next refresh, which is refused. An access token already issued stays valid
// until it expires.
func (i *Issuer) EndRecovery(ctx context.Context) (RecoveryEnded, error) {
	var out RecoveryEnded

	if i.sso != nil {
		signIns, err := i.sso.List(ctx, "")
		if err != nil {
			return out, err
		}

		for _, signIn := range signIns {
			if !signIn.recovered() {
				continue
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
		}
	}

	return out, nil
}

// refuseRecoveryOff writes the page a recovery form's POST gets once recovery
// is off.
func refuseRecoveryOff(w http.ResponseWriter, r *http.Request) {
	refusalPage(w, r, http.StatusForbidden, "the recovery sign-in has been turned off", nil)
}
