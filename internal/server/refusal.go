package server

import (
	"html"
	"log/slog"
	"net/http"
	"strings"

	"github.com/truvity/sluis/internal/refusal"
	"github.com/truvity/sluis/storage/logattr"
)

// refusePage answers a sign-in that is refused in fixed words and a
// reference, and writes the reason to the log under the same one.
//
// Nothing about the person goes on the page -- not the address they signed
// in with, not its domain, not what the directory or the provider said, not
// which setting turned a door off. Whoever is looking at the screen is not
// necessarily who signed in, and none of it is theirs to act on; the
// operator reads the reference back in the log.
//
// A browser gets a page with the way back; anything else (a runbook's curl)
// gets the same words as text.
func (s *ConsoleServer) refusePage(w http.ResponseWriter, r *http.Request, status int, why string, cause error, fields ...any) {
	phrase := refusal.NotAuthorised
	level := slog.LevelWarn
	if status >= http.StatusInternalServerError {
		phrase, level = refusal.ServerError, slog.LevelError
	}
	attrs := []any{slog.Int("status", status), logattr.SafeString("reason", why)}
	if id := refusal.ID(r.Context()); id != "" {
		attrs = append([]any{slog.String(refusal.LogKey, id)}, attrs...)
	}
	if cause != nil {
		attrs = append(attrs, logattr.SafeError("error", cause))
	}
	s.log.Log(r.Context(), level, "refused", append(attrs, fields...)...)

	text := capital(phrase) + "."
	if id := refusal.ID(r.Context()); id != "" {
		text += " Reference: " + id
	}
	if !strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Error(w, text, status)
		return
	}
	s.writePage(w, r, status, capital(phrase), `<h1>`+html.EscapeString(capital(phrase))+`</h1><p>`+html.EscapeString(text)+
		`</p><p><a class="btn" href="`+html.EscapeString(s.at("/"))+`">Back</a></p>`)
}

func capital(phrase string) string {
	if phrase == "" {
		return phrase
	}
	return strings.ToUpper(phrase[:1]) + phrase[1:]
}
