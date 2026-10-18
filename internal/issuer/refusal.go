package issuer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/internal/refusal"
	"github.com/truvity/sluis/storage/logattr"
)

// This file is how the issuer says no.
//
// A caller who is refused is told that, and given the request id. Why --
// which rule, which group, which client, whether a thing exists -- is
// written to the log, and to the audit record where an action exists for
// it, under that id. A reason in a response is an answer to whoever is
// probing: it separates a client that exists from one that does not, an
// audience that is declared from one that is not, and says which group
// would have been enough.
//
// Every refusal an OAuth endpoint gives is built by [deny] (or [malformed]
// and [fault], its two siblings), and nothing else sets an
// error_description: [TestNoDynamicErrorDescriptions] holds the package to
// it. [refusalFilter] is the net under that, for the sentences the OpenID
// library writes itself.

// deny is a refusal of kind, with the reason in the log and none in the
// answer.
//
// kind is the library's constructor for the error code the specifications
// ask for -- oidc.ErrInvalidGrant, oidc.ErrInvalidClient -- and decides
// the fixed words: "not authorised", or "server error" for a fault of
// ours. reason goes to the log under the request id and rides in the
// error's parent, which the library logs and never sends. fields are slog
// attributes for the same line; build them with [logattr] if they carry
// anything a caller chose.
func deny(ctx context.Context, kind func() *oidc.Error, reason error, fields ...any) *oidc.Error {
	e := kind()
	if reason == nil {
		reason = errors.New(string(e.ErrorType))
	}
	// The only place a description is set. Fixed words and the id: what a
	// caller can quote and nothing a caller can learn from.
	e.Description = refusal.Text(ctx, phraseOf(string(e.ErrorType)))
	e.Parent = reason

	level := slog.LevelWarn
	if e.ErrorType == oidc.ServerError {
		level = slog.LevelError
	}
	slog.Default().Log(ctx, level, "refused", append(withID(ctx,
		slog.String("error", string(e.ErrorType)),
		logattr.SafeError("reason", reason),
	), fields...)...)
	return e
}

// malformed is [deny] for a request that is not well-formed:
// invalid_request, and words that say only that.
func malformed(ctx context.Context, reason error, fields ...any) *oidc.Error {
	return deny(ctx, oidc.ErrInvalidRequest, reason, fields...)
}

// fault is [deny] for a failure of ours: server_error, and a reason that
// is ours alone to read.
func fault(ctx context.Context, reason error, fields ...any) *oidc.Error {
	return deny(ctx, oidc.ErrServerError, reason, fields...)
}

// phraseOf is the fixed words for an OAuth error code.
func phraseOf(code string) string {
	switch code {
	case string(oidc.ServerError):
		return refusal.ServerError
	case string(oidc.InvalidRequest), string(oidc.UnsupportedGrantType),
		string(oidc.RequestNotSupported), "unsupported_response_type",
		errRequestURINotSupported, errRegistrationNotSupported:
		return refusal.InvalidRequest
	}
	return refusal.NotAuthorised
}

// refusalPage is the page a browser is shown for a refusal: fixed words
// and the request id, with the reason in the log.
//
// No email, no domain, no client, no error text: whoever reads it is the
// person who was refused, or someone standing behind them, and neither is
// owed more than a number to quote.
func refusalPage(w http.ResponseWriter, r *http.Request, status int, why string, cause error, fields ...any) {
	logRefusal(r.Context(), status, why, cause, fields)
	phrase := refusal.NotAuthorised
	if status >= http.StatusInternalServerError {
		phrase = refusal.ServerError
	}
	_ = writePage(w, status, capital(phrase), `<p>`+html.EscapeString(pageText(r.Context(), phrase))+`</p>`)
}

// pageText is "Not authorised. Reference: <id>".
func pageText(ctx context.Context, phrase string) string {
	text := capital(phrase) + "."
	if id := refusal.ID(ctx); id != "" {
		text += " Reference: " + id
	}
	return text
}

func capital(phrase string) string {
	if phrase == "" {
		return phrase
	}
	return strings.ToUpper(phrase[:1]) + phrase[1:]
}

// logRefusal is the one line a refusal leaves for an operator.
func logRefusal(ctx context.Context, status int, why string, cause error, fields []any) {
	level := slog.LevelWarn
	if status >= http.StatusInternalServerError {
		level = slog.LevelError
	}
	attrs := withID(ctx, slog.Int("status", status), logattr.SafeString("reason", why))
	if cause != nil {
		attrs = append(attrs, logattr.SafeError("error", cause))
	}
	slog.Default().Log(ctx, level, "refused", append(attrs, fields...)...)
}

// refusalFilter is the net under [deny]: it makes every error the OpenID
// library writes say what [deny] says, whatever the library put in it.
//
// The library writes its own sentences for the cases it decides itself --
// "client missing grant type ...", "grant_type %q not allowed", "invalid
// client_id / client_secret" -- and a few of them carry what the caller
// sent or what the storage refused with. Each is replaced here by the
// fixed words, and the sentence it replaced goes to the log under the
// request id. It also settles the one thing the library leaves to each
// caller: RFC 6749 5.2 asks for 401 and a challenge when the client
// authenticated in the Authorization header and 400 when it did not, so an
// unknown client and a wrong secret are the same answer, byte for byte.
//
// It wraps the library itself, so everything between it and the client --
// the page /end_session and /authorize turn an error into -- starts from
// the fixed words too.
func refusalFilter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := &filtered{ResponseWriter: w, r: r}
		next.ServeHTTP(f, r)
		f.finish()
	})
}

// filtered holds back only the responses that are refusals.
type filtered struct {
	http.ResponseWriter

	r       *http.Request
	decided bool
	held    bool
	status  int
	body    bytes.Buffer
}

func (f *filtered) Unwrap() http.ResponseWriter { return f.ResponseWriter }

func (f *filtered) WriteHeader(status int) {
	if f.decided {
		return
	}
	f.decided = true
	if f.holds(status) {
		f.held, f.status = true, status
		return
	}
	f.ResponseWriter.WriteHeader(status)
}

func (f *filtered) Write(p []byte) (int, error) {
	if !f.decided {
		f.WriteHeader(http.StatusOK)
	}
	if f.held {
		return f.body.Write(p)
	}
	return f.ResponseWriter.Write(p)
}

// holds says whether a response is one to rewrite: an error from the
// protocol, or a redirect that carries one to the client. A missing page
// is neither.
func (f *filtered) holds(status int) bool {
	switch {
	case status == http.StatusNotFound, status == http.StatusMethodNotAllowed:
		return false
	case status >= http.StatusBadRequest:
		return true
	case status >= 300 && status < 400:
		return redirectsAnError(f.Header().Get("Location"))
	}
	return false
}

// libraryGrantRefusals are the library's sentences for a token it could
// not verify, which it spells invalid_request and RFC 8693 spells
// invalid_grant.
var libraryGrantRefusals = map[string]bool{
	"subject_token is invalid": true,
	"actor_token is invalid":   true,
}

// keptAsIs are the codes whose words are the protocol's own, fixed and
// about nothing but timing.
func keptAsIs(code string) bool {
	switch code {
	case string(oidc.AuthorizationPending), string(oidc.SlowDown), string(oidc.ExpiredToken):
		return true
	}
	return false
}

func (f *filtered) finish() {
	if !f.held {
		return
	}
	ctx := f.r.Context()
	header := f.Header()
	header.Del("Content-Length")

	if f.status < http.StatusBadRequest {
		location := f.rewriteLocation(ctx, header.Get("Location"))
		header.Set("Location", location)
		f.ResponseWriter.WriteHeader(f.status)
		// The body http.Redirect wrote names the address it was given, which
		// is the one that has just been replaced.
		if f.body.Len() > 0 {
			_, _ = f.ResponseWriter.Write([]byte("<a href=\"" + html.EscapeString(location) + "\">Found</a>.\n\n"))
		}
		return
	}

	contentType := header.Get("Content-Type")
	switch {
	case strings.Contains(contentType, "json"):
		f.writeJSON(ctx, header)
	case strings.Contains(contentType, "html"):
		// A page this service wrote itself, in fixed words.
		f.ResponseWriter.WriteHeader(f.status)
		_, _ = f.ResponseWriter.Write(f.body.Bytes())
	default:
		// http.Error's plain sentence.
		phrase := phraseOf(codeForStatus(f.status))
		f.note(ctx, codeForStatus(f.status), strings.TrimSpace(f.body.String()))
		header.Set("Content-Type", "text/plain; charset=utf-8")
		f.ResponseWriter.WriteHeader(f.status)
		_, _ = f.ResponseWriter.Write([]byte(refusal.Text(ctx, phrase) + "\n"))
	}
}

func codeForStatus(status int) string {
	switch {
	case status >= http.StatusInternalServerError:
		return string(oidc.ServerError)
	case status == http.StatusBadRequest:
		return string(oidc.InvalidRequest)
	}
	return string(oidc.AccessDenied)
}

// writeJSON answers an OAuth error body in the fixed words.
func (f *filtered) writeJSON(ctx context.Context, header http.Header) {
	var body map[string]any
	if err := json.Unmarshal(f.body.Bytes(), &body); err != nil || body == nil {
		body = map[string]any{"error": codeForStatus(f.status)}
		f.note(ctx, codeForStatus(f.status), strings.TrimSpace(f.body.String()))
	}
	code, _ := body["error"].(string)
	if code == "" {
		code = codeForStatus(f.status)
		body["error"] = code
	}
	status := f.status
	if !keptAsIs(code) {
		description, _ := body["error_description"].(string)
		// A subject or actor token the library could not verify is a bad
		// grant (RFC 8693 2.2.2), and the library says invalid_request.
		if code == string(oidc.InvalidRequest) && libraryGrantRefusals[description] {
			code = string(oidc.InvalidGrant)
			body["error"] = code
		}
		if !isFixed(ctx, description) {
			f.note(ctx, code, description)
			body["error_description"] = refusal.Text(ctx, phraseOf(code))
		}
		// Whatever else a library version adds to the body is not for
		// the caller either: a parent error, for one.
		delete(body, "parent")
	}
	if code == string(oidc.InvalidClient) {
		// RFC 6749 5.2.
		if f.r.Header.Get("Authorization") != "" {
			status = http.StatusUnauthorized
			header.Set("WWW-Authenticate", `Basic realm="sluis", charset="UTF-8"`)
		} else {
			status = http.StatusBadRequest
			header.Del("WWW-Authenticate")
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		encoded = []byte(`{"error":"server_error"}`)
		status = http.StatusInternalServerError
	}
	header.Set("Content-Type", "application/json")
	f.ResponseWriter.WriteHeader(status)
	_, _ = f.ResponseWriter.Write(append(encoded, '\n'))
}

// note logs a sentence the filter is about to replace.
func (f *filtered) note(ctx context.Context, code, was string) {
	if was == "" {
		return
	}
	level := slog.LevelWarn
	if code == string(oidc.ServerError) {
		level = slog.LevelError
	}
	slog.Default().Log(ctx, level, "refused", withID(ctx,
		slog.String("error", code), slog.Int("status", f.status), slog.String("path", logattr.Safe(f.r.URL.Path)),
		logattr.SafeString("reason", was))...)
}

// isFixed reports whether a description is already the fixed words with
// this request's id.
func isFixed(ctx context.Context, description string) bool {
	id := refusal.ID(ctx)
	if id == "" {
		return false
	}
	for _, phrase := range []string{refusal.NotAuthorised, refusal.ServerError, refusal.InvalidRequest} {
		if description == phrase+" (request "+id+")" {
			return true
		}
	}
	return false
}

// redirectsAnError reports whether a redirect carries an OAuth error to
// the client, in its query or its fragment.
func redirectsAnError(location string) bool {
	to, err := url.Parse(location)
	if err != nil {
		return false
	}
	return to.Query().Has("error") || fragmentValues(to).Has("error")
}

func fragmentValues(to *url.URL) url.Values {
	values, _ := url.ParseQuery(to.Fragment)
	return values
}

// rewriteLocation puts the fixed words in the error a redirect carries.
func (f *filtered) rewriteLocation(ctx context.Context, location string) string {
	to, err := url.Parse(location)
	if err != nil {
		return location
	}
	fix := func(values url.Values) url.Values {
		code := values.Get("error")
		if code == "" || keptAsIs(code) {
			return values
		}
		if was := values.Get("error_description"); !isFixed(ctx, was) {
			f.note(ctx, code, was)
			values.Set("error_description", refusal.Text(ctx, phraseOf(code)))
		}
		return values
	}
	if query := to.Query(); query.Has("error") {
		to.RawQuery = fix(query).Encode()
	}
	if fragment := fragmentValues(to); fragment.Has("error") {
		to.Fragment = fix(fragment).Encode()
	}
	return to.String()
}

// reasonOfOIDC is the reason an error from [deny] carries, as text, or the
// error's own text for any other.
func reasonOfOIDC(err error) string {
	var spelled *oidc.Error
	if errors.As(err, &spelled) {
		return reasonOf(spelled)
	}
	return err.Error()
}

// reasonOf is why an error from [deny] was given: the reason it carries
// for the log, and for the audit record that names it.
func reasonOf(e *oidc.Error) string {
	if e.Parent != nil {
		return logattr.Error(e.Parent)
	}
	return string(e.ErrorType)
}

// writeOAuthError answers a token request this package serves itself, in
// the same words and status the library's answers have after
// [refusalFilter]: it is the same function, applied to a response written
// here.
func writeOAuthError(w http.ResponseWriter, r *http.Request, e *oidc.Error) {
	f := &filtered{ResponseWriter: w, r: r}
	op.RequestError(f, r, e, nil)
	f.finish()
}

// phraseIn is the fixed words a text starts with, for turning a response
// [refusalFilter] has already worded into a page.
func phraseIn(text string) string {
	text = strings.TrimSpace(text)
	for _, phrase := range []string{refusal.ServerError, refusal.InvalidRequest} {
		if strings.HasPrefix(text, phrase) {
			return phrase
		}
	}
	return refusal.NotAuthorised
}

// withID puts the request id first among a refusal's log attributes, when
// there is one. The log handler adds it to any line that lacks it, but a
// refusal names it itself so that it is there under any handler.
func withID(ctx context.Context, attrs ...any) []any {
	if id := refusal.ID(ctx); id != "" {
		return append([]any{slog.String(refusal.LogKey, id)}, attrs...)
	}
	return attrs
}

// refusePlain is the refusal of a plain-text endpoint, such as the
// operator's: the fixed words and the request id, the reason in the log.
func refusePlain(w http.ResponseWriter, r *http.Request, status int, why string, fields ...any) {
	logRefusal(r.Context(), status, why, nil, fields)
	phrase := refusal.NotAuthorised
	if status >= http.StatusInternalServerError {
		phrase = refusal.ServerError
	}
	http.Error(w, refusal.Text(r.Context(), phrase), status)
}
