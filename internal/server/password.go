package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/srpauth"
	"github.com/truvity/sluis/internal/srpweb"
	"github.com/truvity/sluis/storage/logattr"
)

// The recovery account signs in to the console with a password that is proved
// and never sent (docs/decisions/0076): two JSON requests the sign-in page's
// script makes, answered by the same exchange service the issuer's pages use.
// There is no route that takes a password, in a form, a body or a header.

// passwordRecovery is the console's password recovery, when it has one built to
// sign in.
func (s *ConsoleServer) passwordRecovery() (*PasswordRecovery, bool) {
	p, ok := s.recovery.(*PasswordRecovery)
	return p, ok && p != nil && p.Service != nil
}

// registerPassword mounts the password recovery's three routes.
func (s *ConsoleServer) registerPassword(mux *http.ServeMux) {
	mux.Handle("GET /login/srp.js", srpweb.Handler())
	mux.HandleFunc("POST /login/srp/start", s.passwordStart)
	mux.HandleFunc("POST /login/srp/finish", s.passwordFinish)
}

// passwordForm is the password block of the sign-in page.
func (s *ConsoleServer) passwordForm(state string) string {
	prompt := s.recovery.Prompt()
	return srpweb.Form(srpweb.FormOptions{
		State: state, Summary: "Recovery sign-in",
		Note:    "For the day the directory is what is broken. " + prompt.Intro,
		Caution: prompt.Caution,
		Start:   s.mount + "/login/srp/start", Finish: s.mount + "/login/srp/finish", Script: s.mount + "/login/srp.js",
	})
}

func srpReply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func srpFail(w http.ResponseWriter, status int, message string) {
	srpReply(w, status, map[string]string{"error": message})
}

// passwordRequest reads and checks the JSON a request of the door carries: the
// recovery is a password recovery that is on, the body is JSON of a bounded
// size, and its state is this console's, bound to recovery, and the same as the
// cookie of the browser the page was served to.
func (s *ConsoleServer) passwordRequest(w http.ResponseWriter, r *http.Request, into any, state func() string) (*PasswordRecovery, bool) {
	p, ok := s.passwordRecovery()
	if !ok {
		srpFail(w, http.StatusNotFound, "This console has no password sign-in.")
		return nil, false
	}
	if !s.recoveryAvailable(r.Context()) {
		s.recordPasswordDenied(r, reasonRecoveryDisabled, true)
		srpFail(w, http.StatusForbidden, "The recovery sign-in has been turned off.")
		return nil, false
	}
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
		srpFail(w, http.StatusUnsupportedMediaType, "That request was not understood.")
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, access.RecoveryFormLimit)
	// Unknown fields are refused: a body that carries a password, or anything
	// else this exchange does not take, is not one the page's script sends.
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		srpFail(w, http.StatusBadRequest, "That request was not understood.")
		return nil, false
	}
	if s.state == nil || s.sessions == nil {
		srpFail(w, http.StatusBadRequest, "This sign-in is not valid any more. Start again.")
		return nil, false
	}
	st := state()
	if !access.RecoveryStartedHere(r, st, s.sessions.Secure()) {
		srpFail(w, http.StatusBadRequest, "This sign-in did not start in this browser.")
		return nil, false
	}
	if bound, err := s.state.VerifyBinding(st); err != nil || bound != (access.Binding{Owner: access.RecoveryPurpose}) {
		srpFail(w, http.StatusBadRequest, "This sign-in is not valid any more. Start again.")
		return nil, false
	}
	return p, true
}

// recordPasswordDenied records a refused attempt: every one is, as the token
// form's always were. The ones that cost nothing are recorded once a window.
func (s *ConsoleServer) recordPasswordDenied(r *http.Request, reason string, cheap bool) {
	if cheap {
		n, ok := s.cheap.note(reason, time.Now())
		if !ok {
			return
		}
		if n > 1 {
			reason = fmt.Sprintf("%s (%d attempts in the last %s)", reason, n, cheapWindow)
		}
	}
	s.console.record(r.Context(), audit.RecoverySignedIn(audit.Anonymous(), "console", "password", audit.Denied(reason)))
}

func (s *ConsoleServer) passwordError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, srpauth.ErrThrottled):
		s.log.WarnContext(r.Context(), "recovery refused: too many attempts", logattr.SafeString("remote", r.RemoteAddr))
		s.recordPasswordDenied(r, reasonRecoveryThrottled, true)
		srpFail(w, http.StatusTooManyRequests, "Too many attempts. Wait a minute and try again.")
	case errors.Is(err, srpauth.ErrRefused):
		s.log.WarnContext(r.Context(), "recovery refused", logattr.SafeString("remote", r.RemoteAddr))
		s.recordPasswordDenied(r, reasonRecoveryRefused, false)
		srpFail(w, http.StatusForbidden, "That sign-in was not accepted.")
	default:
		s.log.ErrorContext(r.Context(), "recovery could not be checked", logattr.SafeError("error", err))
		s.console.record(r.Context(), audit.RecoverySignedIn(audit.Anonymous(), "console", "password", audit.Failed(reasonRecoveryUnchecked)))
		srpFail(w, http.StatusServiceUnavailable, "The sign-in could not be checked. Try again.")
	}
}

// passwordStart is the first request: the username, answered with the salt, B
// and a handle.
func (s *ConsoleServer) passwordStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		State    string `json:"state"`
		Username string `json:"username"`
	}
	p, ok := s.passwordRequest(w, r, &in, func() string { return in.State })
	if !ok {
		return
	}
	source := clientAddress(r.Header, r.RemoteAddr, s.forwardedForTrustedHops)
	begun, err := p.Service.Start(r.Context(), source, in.State, in.Username, p.lookup())
	if err != nil {
		s.passwordError(w, r, err)
		return
	}
	srpReply(w, http.StatusOK, map[string]any{
		"handle": begun.Handle, "username": begun.Username, "salt": hex.EncodeToString(begun.Salt),
		"B": hex.EncodeToString(begun.B), "iterations": begun.Iterations,
	})
}

// passwordFinish is the second: A and the proof M1. A good proof is recorded
// durably, and refused if it cannot be, and then opens the console's session as
// the recovery account.
func (s *ConsoleServer) passwordFinish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		State  string `json:"state"`
		Handle string `json:"handle"`
		A      string `json:"A"`
		M1     string `json:"M1"`
	}
	p, ok := s.passwordRequest(w, r, &in, func() string { return in.State })
	if !ok {
		return
	}
	a, errA := hex.DecodeString(in.A)
	m1, errM := hex.DecodeString(in.M1)
	if errA != nil || errM != nil {
		srpFail(w, http.StatusBadRequest, "That request was not understood.")
		return
	}
	source := clientAddress(r.Header, r.RemoteAddr, s.forwardedForTrustedHops)
	proved, err := p.Service.Finish(r.Context(), source, in.State, in.Handle, a, m1, p.lookup())
	if err != nil {
		s.passwordError(w, r, err)
		return
	}
	subject := proved.User

	// Written down durably BEFORE there is a session, and refused when it cannot
	// be: the one event that does not fail open (see recoveryLogin).
	recovered := func(o audit.Outcome) *record.Record {
		return audit.RecoverySignedIn(audit.RecoveryIdentity(subject), "console", p.Kind(), o)
	}
	if err = s.console.recordDurable(r.Context(), recovered(audit.Succeeded())); err != nil {
		s.log.ErrorContext(r.Context(), "recovery refused: the audit trail could not be written",
			logattr.SafeString("subject", subject), logattr.SafeError("error", err))
		s.console.record(r.Context(), recovered(audit.Denied(reasonUnaudited)))
		srpFail(w, http.StatusServiceUnavailable,
			"Refused: the audit trail could not be written, and a recovery sign-in never happens without its record.")
		return
	}
	if err = s.sessions.Issue(w, access.Principal{Email: subject, Subject: subject, Source: access.SourceRecovery}); err != nil {
		s.console.record(r.Context(), recovered(audit.Failed("the session could not be issued")))
		srpFail(w, http.StatusInternalServerError, "The session could not be issued.")
		return
	}
	// Spent, as the provider callback spends its own.
	access.SetCookie(w, access.RecoveryCookie("", s.sessions.Secure(), 0))
	s.log.WarnContext(r.Context(), "recovery sign-in", logattr.SafeString("subject", subject), slog.String("kind", p.Kind()))
	srpReply(w, http.StatusOK, map[string]string{"M2": hex.EncodeToString(proved.M2), "next": s.at("/")})
}
