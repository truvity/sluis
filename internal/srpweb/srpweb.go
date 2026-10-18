// Package srpweb serves the browser half of the password sign-in
// (docs/decisions/0076): one script, this origin's own, that runs SRP-6a in the
// page so the password is never sent. It is a file and not an inline script
// because the pages' policy is `default-src 'self'`.
package srpweb

import (
	"crypto/sha256"
	_ "embed" // the script
	"encoding/hex"
	"fmt"
	"html"
	"net/http"
)

//go:embed srp.js
var script []byte

// Script is the script's source.
func Script() []byte { return append([]byte(nil), script...) }

var etag = func() string {
	sum := sha256.Sum256(script)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}()

// Handler serves the script. It changes only with a release, so a browser may
// keep it for a few minutes and ask again with the tag.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "text/javascript; charset=utf-8")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "public, max-age=300")
		h.Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write(script)
	})
}

// FormOptions is what [Form] needs.
type FormOptions struct {
	// State is the signed state of the page's request.
	State string
	// Summary is the disclosure's title, Note the sentence under it and Caution
	// the warning after the form (empty for none).
	Summary, Note, Caution string
	// Start, Finish and Script are the paths of the two endpoints and of [Handler].
	Start, Finish, Script string
}

// Form is the password block of a sign-in page: a disclosure holding a form the
// script drives. The inputs carry no `name`, so a browser that does not run the
// script submits nothing and the password goes nowhere else.
func Form(o FormOptions) string {
	caution := ""
	if o.Caution != "" {
		caution = `<p class="warn">` + html.EscapeString(o.Caution) + `</p>`
	}
	return fmt.Sprintf(`<details><summary class="note">%s</summary>
	<p class="note">%s</p>
	<form data-srp data-state="%s" data-start="%s" data-finish="%s">
		<p><label>Username<br><input type="text" data-srp-user autocomplete="username" autocapitalize="none" spellcheck="false"></label></p>
		<p><label>Password<br><input type="password" data-srp-password autocomplete="current-password"></label></p>
		<p><button type="submit">Sign in</button></p>
		<p class="warn" data-srp-error hidden></p>
	</form>
	<noscript><p class="warn">This needs JavaScript.</p></noscript>%s
	<script src="%s" defer></script></details>`,
		html.EscapeString(o.Summary), html.EscapeString(o.Note), html.EscapeString(o.State),
		html.EscapeString(o.Start), html.EscapeString(o.Finish), caution, html.EscapeString(o.Script))
}
