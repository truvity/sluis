package op

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	httphelper "github.com/zitadel/oidc/v3/pkg/http"
	"github.com/zitadel/oidc/v3/pkg/oidc"
)

type ErrAuthRequest interface {
	GetRedirectURI() string
	GetResponseType() oidc.ResponseType
	GetState() string
}

// LogAuthRequest is an optional interface,
// that allows logging AuthRequest fields.
// If the AuthRequest does not implement this interface,
// no details shall be printed to the logs.
type LogAuthRequest interface {
	ErrAuthRequest
	slog.LogValuer
}

func AuthRequestError(w http.ResponseWriter, r *http.Request, authReq ErrAuthRequest, err error, authorizer Authorizer) {
	e := oidc.DefaultToServerError(err, err.Error())
	level := authRequestLogLevel(e, err)
	args := authRequestLogArgs(r, authReq, e)

	if authReq == nil {
		slog.Log(r.Context(), level, "auth request", args...)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if logAuthReq, ok := authReq.(LogAuthRequest); ok {
		args = append(args, slog.Any("auth_request", logAuthReq))
	}

	if authReq.GetRedirectURI() == "" || e.IsRedirectDisabled() {
		slog.Log(r.Context(), level, "auth request: not redirecting", args...)
		http.Error(w, e.Description, http.StatusBadRequest)
		return
	}
	e.State = authReq.GetState()
	var sessionState string
	authRequestSessionState, ok := authReq.(AuthRequestSessionState)
	if ok {
		sessionState = authRequestSessionState.GetSessionState()
	}
	e.SessionState = sessionState
	var responseMode oidc.ResponseMode
	if rm, ok := authReq.(interface{ GetResponseMode() oidc.ResponseMode }); ok {
		responseMode = rm.GetResponseMode()
	}
	url, err := AuthResponseURL(authReq.GetRedirectURI(), authReq.GetResponseType(), responseMode, e, authorizer.Encoder())
	if err != nil {
		args = append(args, slog.Any("error", err))
		slog.ErrorContext(r.Context(), "auth response URL", args...)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	slog.Log(r.Context(), level, "auth request", args...)
	http.Redirect(w, r, url, http.StatusFound)
}

// maxAuthRequestLogValue bounds a request value copied into a log line, so a
// caller cannot make a line as long as its query string.
const maxAuthRequestLogValue = 256

// authRequestLogLevel is the level an auth request error is logged at. The
// error's own level stands, except that an error which wraps a server error
// anywhere in its chain is logged as one: the authorization endpoint words a
// failed client lookup as an invalid request (so the error is never redirected
// to an unchecked address), and that must not hide a fault of the server's own
// behind a warning.
func authRequestLogLevel(e *oidc.Error, err error) slog.Level {
	for cur := err; cur != nil; cur = errors.Unwrap(cur) {
		if inner, ok := cur.(*oidc.Error); ok && inner.ErrorType == oidc.ServerError {
			return slog.LevelError
		}
	}
	return e.LogLevel()
}

// authRequestLogArgs are the fields of an auth request error's log line:
// the error code and why, the client the request names and, when there is a
// request, the path it was made to. Only these: never the query, the state, a
// PKCE challenge or any other parameter.
func authRequestLogArgs(r *http.Request, authReq ErrAuthRequest, e *oidc.Error) []any {
	args := []any{slog.Any("oidc_error", e), slog.String("error", string(e.ErrorType))}
	if e.Description != "" {
		args = append(args, slog.String("reason", truncateLogValue(e.Description)))
	}
	if e.Parent != nil {
		args = append(args, slog.String("cause", truncateLogValue(e.Parent.Error())))
	}
	var clientID string
	if named, ok := authReq.(interface{ GetClientID() string }); ok {
		clientID = named.GetClientID()
	}
	if clientID == "" && r != nil {
		if r.Form != nil {
			clientID = r.Form.Get("client_id")
		} else {
			clientID = r.URL.Query().Get("client_id")
		}
	}
	if clientID != "" {
		args = append(args, slog.String("client_id", truncateLogValue(clientID)))
	}
	if r != nil {
		args = append(args, slog.String("path", truncateLogValue(r.URL.Path)))
	}
	return args
}

func truncateLogValue(s string) string {
	if len(s) <= maxAuthRequestLogValue {
		return s
	}
	return strings.ToValidUTF8(s[:maxAuthRequestLogValue], "") + "..."
}

func RequestError(w http.ResponseWriter, r *http.Request, err error, _ *slog.Logger) {
	e := oidc.DefaultToServerError(err, err.Error())
	status := http.StatusBadRequest
	if e.ErrorType == oidc.InvalidClient {
		status = http.StatusUnauthorized
	}
	if e.ErrorType == oidc.ServerError {
		status = http.StatusInternalServerError
	}
	slog.Log(r.Context(), e.LogLevel(), "request error", slog.Any("oidc_error", e))
	httphelper.MarshalJSONWithStatus(w, e, status)
}

// TryErrorRedirect tries to handle an error by redirecting a client.
// If this attempt fails, an error is returned that must be returned
// to the client instead.
func TryErrorRedirect(ctx context.Context, authReq ErrAuthRequest, parent error, encoder httphelper.Encoder, _ *slog.Logger) (*Redirect, error) {
	e := oidc.DefaultToServerError(parent, parent.Error())
	level := authRequestLogLevel(e, parent)
	args := authRequestLogArgs(nil, authReq, e)

	if authReq == nil {
		slog.Log(ctx, level, "auth request", args...)
		return nil, AsStatusError(e, http.StatusBadRequest)
	}

	if logAuthReq, ok := authReq.(LogAuthRequest); ok {
		args = append(args, slog.Any("auth_request", logAuthReq))
	}

	if authReq.GetRedirectURI() == "" || e.IsRedirectDisabled() {
		slog.Log(ctx, level, "auth request: not redirecting", args...)
		return nil, AsStatusError(e, http.StatusBadRequest)
	}

	e.State = authReq.GetState()
	var sessionState string
	authRequestSessionState, ok := authReq.(AuthRequestSessionState)
	if ok {
		sessionState = authRequestSessionState.GetSessionState()
	}
	e.SessionState = sessionState
	var responseMode oidc.ResponseMode
	if rm, ok := authReq.(interface{ GetResponseMode() oidc.ResponseMode }); ok {
		responseMode = rm.GetResponseMode()
	}
	url, err := AuthResponseURL(authReq.GetRedirectURI(), authReq.GetResponseType(), responseMode, e, encoder)
	if err != nil {
		args = append(args, slog.Any("error", err))
		slog.ErrorContext(ctx, "auth response URL", args...)
		return nil, AsStatusError(err, http.StatusBadRequest)
	}
	args = append(args, slog.String("url", url))
	slog.Log(ctx, level, "auth request redirect", args...)
	return NewRedirect(url), nil
}

// StatusError wraps an error with an HTTP status code.
// The status code is passed to the handler's writer.
type StatusError struct {
	parent     error
	statusCode int
}

// NewStatusError sets the parent and statusCode to a new StatusError.
// It is recommended for parent to be an [oidc.Error].
//
// Typically, implementations should only use this to signal something
// very specific, like an internal server error.
// If a returned error is not a StatusError, the framework
// will set a statusCode based on what the standard specifies,
// which is [http.StatusBadRequest] for most of the time.
// If the encountered error can be clearly described by an [oidc.Error],
// do not use this function, as it might break standard rules!
func NewStatusError(parent error, statusCode int) StatusError {
	return StatusError{
		parent:     parent,
		statusCode: statusCode,
	}
}

// AsStatusError unwraps a StatusError from err
// and returns it unmodified if found.
// If no StatusError was found, a new one is returned
// with statusCode set to it as a default.
func AsStatusError(err error, statusCode int) (target StatusError) {
	if errors.As(err, &target) {
		return target
	}
	return NewStatusError(err, statusCode)
}

func (e StatusError) Error() string {
	return fmt.Sprintf("%s: %s", http.StatusText(e.statusCode), e.parent.Error())
}

func (e StatusError) Unwrap() error {
	return e.parent
}

func (e StatusError) Is(err error) bool {
	var target StatusError
	if !errors.As(err, &target) {
		return false
	}
	return errors.Is(e.parent, target.parent) &&
		e.statusCode == target.statusCode
}

// WriteError asserts for a [StatusError] containing an [oidc.Error].
// If no `StatusError` is found, the status code will default to [http.StatusBadRequest].
// If no `oidc.Error` was found in the parent, the error type defaults to [oidc.ServerError].
// When there was no `StatusError` and the `oidc.Error` is of type `oidc.ServerError`,
// the status code will be set to [http.StatusInternalServerError]
func WriteError(w http.ResponseWriter, r *http.Request, err error, _ *slog.Logger) {
	var statusError StatusError
	if errors.As(err, &statusError) {
		writeError(w, r,
			oidc.DefaultToServerError(statusError.parent, statusError.parent.Error()),
			statusError.statusCode,
		)
		return
	}
	statusCode := http.StatusBadRequest
	e := oidc.DefaultToServerError(err, err.Error())
	if e.ErrorType == oidc.ServerError {
		statusCode = http.StatusInternalServerError
	}
	writeError(w, r, e, statusCode)
}

func writeError(w http.ResponseWriter, r *http.Request, err *oidc.Error, statusCode int) {
	slog.Log(r.Context(), err.LogLevel(), "request error", slog.Any("oidc_error", err), slog.Int("status_code", statusCode))
	httphelper.MarshalJSONWithStatus(w, err, statusCode)
}
