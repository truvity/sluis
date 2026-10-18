package issuer

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/srpauth"
	"github.com/truvity/sluis/internal/srpweb"
	"github.com/truvity/sluis/srp"
	"github.com/truvity/sluis/storage/logattr"
)

// PasswordDoor is the sign-in by username and password that never sends the
// password (docs/decisions/0076). The page's script proves it knows the
// password to this issuer with SRP-6a in two requests, and the issuer holds a
// salt and a verifier for it and no password. Two accounts use it and nothing
// else does: the recovery account, behind the recovery switch of
// docs/decisions/0075, and the conformance account, off unless the installation
// turns it on and offered only for a client the policy marks `harness`.
type PasswordDoor struct {
	// Service runs the exchanges.
	Service *srpauth.Service
	// Recovery reads the recovery account's credential; nil offers no recovery
	// account at this door.
	Recovery func(ctx context.Context) (srp.Credential, error)
	// RecoverySubject is the ServiceAccount subject a recovery sign-in
	// completes as, so that the policy's `service_account` matchers decide what
	// it is in, exactly as they do for a recovery token.
	RecoverySubject string
	// Conformance reads the conformance account's credential; nil offers no
	// conformance account. The account is also off while the issuer's
	// [Issuer.UseConformance] flag is.
	Conformance func(ctx context.Context) (srp.Credential, error)
	// ConformanceSubject is the ServiceAccount subject the account completes as.
	ConformanceSubject string
}

// offered says which accounts this request may use. The recovery account
// needs the recovery switch on; the conformance account needs its flag, the
// same switch, and a client the policy marks a harness's.
func (d *PasswordDoor) offered(ctx context.Context, iss *Issuer, pending Pending) (recovery, conformance bool) {
	if d == nil || iss.recoveryCheck(ctx).refuse() {
		return false, false
	}
	recovery = d.Recovery != nil
	conformance = d.Conformance != nil && iss.ConformanceOn() && pending.Client.Harness
	return recovery, conformance
}

// lookup is the accounts this request may use, for the exchange service.
func (d *PasswordDoor) lookup(iss *Issuer, pending Pending) srpauth.Lookup {
	return func(ctx context.Context, username string) (srp.Credential, bool, error) {
		recovery, conformance := d.offered(ctx, iss, pending)
		var read func(context.Context) (srp.Credential, error)
		switch {
		case username == srpauth.RecoveryUser && recovery:
			read = d.Recovery
		case username == srpauth.ConformanceUser && conformance:
			read = d.Conformance
		default:
			return srp.Credential{}, false, nil
		}
		cred, err := read(ctx)
		if errors.Is(err, srpauth.ErrNoCredential) {
			return srp.Credential{}, false, nil
		}
		if err != nil {
			return srp.Credential{}, false, err
		}
		return cred, true, nil
	}
}

// identity is how an account that proved itself signs in.
func (d *PasswordDoor) identity(username string) (subject, how string, ok bool) {
	switch username {
	case srpauth.RecoveryUser:
		return d.RecoverySubject, RecoveryHow, d.RecoverySubject != ""
	case srpauth.ConformanceUser:
		return d.ConformanceSubject, ConformanceHow, d.ConformanceSubject != ""
	}
	return "", "", false
}

// The paths of the door.
const (
	passwordScriptPath = "/login/srp.js"
	passwordStartPath  = "/login/srp/start"
	passwordFinishPath = "/login/srp/finish"
)

// passwordForm is the password block of the sign-in page.
func passwordForm(state string, recovery, conformance bool) string {
	var who []string
	if recovery {
		who = append(who, "the recovery account")
	}
	if conformance {
		who = append(who, "the conformance test account")
	}
	return srpweb.Form(srpweb.FormOptions{
		State: state, Summary: "Password sign-in",
		Note:  "For " + strings.Join(who, " and ") + ". The password is proved in this page and never sent.",
		Start: passwordStartPath, Finish: passwordFinishPath, Script: passwordScriptPath,
	})
}

// passwordRoutes registers the door's three routes.
func (s *signIn) passwordRoutes(mux *http.ServeMux) {
	mux.Handle("GET "+passwordScriptPath, srpweb.Handler())
	mux.HandleFunc("POST "+passwordStartPath, s.passwordStart)
	mux.HandleFunc("POST "+passwordFinishPath, s.passwordFinish)
}

// srpReply writes a JSON answer.
func srpReply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// srpFail writes a refusal the page's script shows: one sentence and nothing a
// caller could use to tell its failure from another's.
func srpFail(w http.ResponseWriter, status int, message string) {
	srpReply(w, status, map[string]string{"error": message})
}

const msgNotAccepted = "That sign-in was not accepted."

// srpBody reads a JSON request, bounded, and refuses any other content type.
func (s *signIn) srpBody(w http.ResponseWriter, r *http.Request, into any) bool {
	if mt, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(mt) != "application/json" {
		recordLoginFailure(r.Context(), LoginBadRequest)
		srpFail(w, http.StatusUnsupportedMediaType, "That request was not understood.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, access.RecoveryFormLimit)
	// Unknown fields are refused: a body that carries a password, or anything
	// else this exchange does not take, is not one the page's script sends.
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		recordLoginFailure(r.Context(), LoginBadRequest)
		srpFail(w, http.StatusBadRequest, "That request was not understood.")
		return false
	}
	return true
}

// srpRequest checks the state a request carries: this browser's, a recovery
// form's and still waiting. It returns the authorization request it is for.
func (s *signIn) srpRequest(w http.ResponseWriter, r *http.Request, state string) (string, Pending, bool) {
	if !access.RecoveryStartedHere(r, state, s.deps.Secure) {
		recordLoginFailure(r.Context(), LoginBadState)
		srpFail(w, http.StatusBadRequest, "This sign-in did not start in this browser.")
		return "", Pending{}, false
	}
	bound, err := s.deps.State.VerifyBinding(state)
	if err != nil || bound.Bind == "" || bound.Owner != access.RecoveryPurpose {
		recordLoginFailure(r.Context(), LoginBadState)
		srpFail(w, http.StatusBadRequest, "This sign-in is not valid any more. Start again.")
		return "", Pending{}, false
	}
	pending, err := s.deps.Storage.Pending(bound.Bind)
	if err != nil {
		recordLoginFailure(r.Context(), LoginNotWaiting)
		srpFail(w, http.StatusBadRequest, "That sign-in is no longer waiting to be completed.")
		return "", Pending{}, false
	}
	return bound.Bind, pending, true
}

// srpError answers an exchange error.
func (s *signIn) srpError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, srpauth.ErrThrottled):
		s.deps.Log.WarnContext(r.Context(), "password sign-in refused: too many attempts")
		recordLoginFailure(r.Context(), LoginRecoveryThrottled)
		srpFail(w, http.StatusTooManyRequests, "Too many attempts. Wait a minute and try again.")
	case errors.Is(err, srpauth.ErrRefused):
		s.deps.Log.WarnContext(r.Context(), "password sign-in refused")
		recordLoginFailure(r.Context(), LoginRecoveryRefused)
		srpFail(w, http.StatusForbidden, msgNotAccepted)
	default:
		s.deps.Log.ErrorContext(r.Context(), "password sign-in could not be checked", logattr.SafeError("error", err))
		srpFail(w, http.StatusServiceUnavailable, "The sign-in could not be checked. Try again.")
	}
}

// passwordStart is the first request: the username, answered with the salt, B
// and a handle.
func (s *signIn) passwordStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		State    string `json:"state"`
		Username string `json:"username"`
	}
	if !s.srpBody(w, r, &in) {
		return
	}
	_, pending, ok := s.srpRequest(w, r, in.State)
	if !ok {
		return
	}
	door := s.deps.Password
	begun, err := door.Service.Start(r.Context(), requestSource(r), in.State, in.Username, door.lookup(s.deps.Issuer, pending))
	if err != nil {
		s.srpError(w, r, err)
		return
	}
	srpReply(w, http.StatusOK, map[string]any{
		"handle": begun.Handle, "username": begun.Username, "salt": hex.EncodeToString(begun.Salt),
		"B": hex.EncodeToString(begun.B), "iterations": begun.Iterations,
	})
}

// passwordFinish is the second: A and the proof M1. A good proof signs the
// browser in as the account's subject, and the answer carries the server's
// proof M2 and where to go.
func (s *signIn) passwordFinish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		State  string `json:"state"`
		Handle string `json:"handle"`
		A      string `json:"A"`
		M1     string `json:"M1"`
	}
	if !s.srpBody(w, r, &in) {
		return
	}
	request, pending, ok := s.srpRequest(w, r, in.State)
	if !ok {
		return
	}
	a, errA := hex.DecodeString(in.A)
	m1, errM := hex.DecodeString(in.M1)
	if errA != nil || errM != nil {
		recordLoginFailure(r.Context(), LoginBadRequest)
		srpFail(w, http.StatusBadRequest, "That request was not understood.")
		return
	}
	door := s.deps.Password
	proved, err := door.Service.Finish(r.Context(), requestSource(r), in.State, in.Handle, a, m1, door.lookup(s.deps.Issuer, pending))
	if err != nil {
		s.srpError(w, r, err)
		return
	}
	subject, how, ok := door.identity(proved.User)
	if !ok {
		s.srpError(w, r, srpauth.ErrRefused)
		return
	}
	// Spent once a proof is accepted, as the callback spends it.
	access.SetCookie(w, access.RecoveryCookie("", s.deps.Secure, 0))

	who, secret := s.established(w, r, subject, how)
	if err = s.deps.Storage.Complete(r.Context(), request, who); err != nil {
		if errors.Is(err, ErrUnaudited) || errors.Is(err, ErrRecoveryOff) || errors.Is(err, ErrConformanceOff) {
			// The browser session this began goes with it, as for a recovery token.
			s.unestablish(r, who)
		} else {
			s.handOver(w, r, who, secret)
		}
		s.srpCompleteError(w, r, err)
		return
	}
	s.handOver(w, r, who, secret)
	recordLoginSuccess(r.Context(), how)
	// WARN: this is a way in that bypasses the directory, and as loud in a log
	// as it is rare.
	s.deps.Log.WarnContext(r.Context(), "password sign-in", logattr.SafeString("subject", subject), logattr.SafeString("account", proved.User))
	srpReply(w, http.StatusOK, map[string]string{
		"M2": hex.EncodeToString(proved.M2), "next": s.deps.Return(r.Context(), request),
	})
}

// srpCompleteError answers a proof that was good and a completion that was not.
func (s *signIn) srpCompleteError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrRecoveryOff), errors.Is(err, ErrConformanceOff):
		recordLoginFailure(r.Context(), LoginRecoveryOff)
		srpFail(w, http.StatusForbidden, "That sign-in has been turned off.")
	case errors.Is(err, ErrUnaudited):
		recordLoginFailure(r.Context(), LoginUnaudited)
		s.deps.log().ErrorContext(r.Context(), "recovery refused: the audit trail could not be written", logattr.SafeError("error", err))
		srpFail(w, http.StatusServiceUnavailable, "Refused: the audit trail could not be written, and a recovery sign-in never happens without its record.")
	case errors.Is(err, ErrNotEntitled):
		recordLoginFailure(r.Context(), LoginNotEntitled)
		s.deps.log().InfoContext(r.Context(), "refused a client the identity is not entitled to", logattr.SafeError("error", err))
		srpFail(w, http.StatusForbidden, "You are signed in, but not for this application.")
	case errors.Is(err, ErrHintMismatch):
		recordLoginFailure(r.Context(), LoginNotWaiting)
		srpFail(w, http.StatusForbidden, "That sign-in is not the person the application asked for.")
	default:
		recordLoginFailure(r.Context(), LoginNotWaiting)
		srpFail(w, http.StatusBadRequest, "That sign-in is no longer waiting to be completed.")
	}
}
