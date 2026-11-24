package mimic

import (
	"crypto/hmac"
	"encoding/base64"
	"html/template"
	"net/http"
	"net/url"
)

// The identity chooser: a person who signed in at the stable sluis, in a tenant of two or more identities, with no
// login_hint, picks one of the tenant's fixed identities. The choice resumes the same authorization request with that
// identity as its login_hint, through handleAuthorize, so every check of the request runs again on it.
//
// A cross-site form must not pick an identity for the person. The form is a POST that carries a token: an HMAC, under
// a key derived from mimic's secret for this purpose alone, of the session cookie, the tenant and the exact query of
// the authorization request. Only a page that can read the chooser, which is mimic's own origin, can learn the token,
// and the token is good for that session, that tenant and that request only. The session cookie is SameSite=Lax as
// well, so a browser does not send it with a cross-site POST in the first place.

var chooserPage = template.Must(template.New("chooser").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>mimic: choose an identity</title>
</head>
<body>
<h1>Choose an identity</h1>
<p>Sign in to {{.Client}} as one of the test identities of {{.Issuer}}.</p>
<ul>
{{- range .Identities}}
<li><form method="post" action="{{$.Action}}">
<input type="hidden" name="request" value="{{$.Request}}">
<input type="hidden" name="token" value="{{$.Token}}">
<input type="hidden" name="login_hint" value="{{.Username}}">
<button type="submit">{{.Name}} ({{.Username}})</button>
</form></li>
{{- end}}
</ul>
<p>mimic is a test identity provider of development. The tokens it issues name you in <code>act</code>.</p>
</body>
</html>
`))

// choiceToken is the token of the chooser's form for one session cookie, one tenant and one authorization request.
func (k *keys) choiceToken(sessionCookie, tenant, request string) string {
	return base64.RawURLEncoding.EncodeToString(mac(k.choice, "choice", sessionCookie, tenant, request))
}

// showChooser answers an authorization request that names no identity with the page that lists the tenant's
// identities. The caller has checked the client, the redirect URI, PKCE, the session and the act-as group.
func (t *tenant) showChooser(w http.ResponseWriter, r *http.Request, ses *session) {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		http.Error(w, "no session at mimic", http.StatusBadRequest)
		return
	}
	data := struct {
		Client, Issuer, Action, Request, Token string
		Identities                             []Identity
	}{
		Client: r.URL.Query().Get("client_id"), Issuer: t.issuer, Action: t.cfg.path() + "/authorize/choose",
		Request: r.URL.RawQuery, Token: t.s.keys.choiceToken(c.Value, t.cfg.path(), r.URL.RawQuery),
		Identities: t.cfg.Identities,
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	t.s.log.Info("mimic: identity chooser shown", "tenant", t.cfg.path(), "sluis_sub", ses.Sub)
	_ = chooserPage.Execute(w, data)
}

// handleChoose takes a choice of the chooser. It checks the token against the request's own session cookie, then
// resumes the authorization request with the chosen identity as its login_hint, through handleAuthorize.
func (t *tenant) handleChoose(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form does not parse", http.StatusBadRequest)
		return
	}
	f := r.PostForm
	c, err := r.Cookie(SessionCookie)
	if err != nil || t.s.sessionOf(r) == nil {
		http.Error(w, "no session at mimic; start the sign-in again", http.StatusForbidden)
		return
	}
	got, err := base64.RawURLEncoding.DecodeString(f.Get("token"))
	want, _ := base64.RawURLEncoding.DecodeString(t.s.keys.choiceToken(c.Value, t.cfg.path(), f.Get("request")))
	if err != nil || !hmac.Equal(got, want) {
		t.s.log.Info("mimic: refused, a choice without this session's token", "tenant", t.cfg.path())
		http.Error(w, "token: the choice does not belong to this session; start the sign-in again", http.StatusForbidden)
		return
	}
	q, err := url.ParseQuery(f.Get("request"))
	hint := f.Get("login_hint")
	if err != nil || hint == "" {
		http.Error(w, "request and login_hint: required", http.StatusBadRequest)
		return
	}
	q.Set("login_hint", hint)
	u := *r.URL
	u.Path, u.RawPath, u.RawQuery = t.cfg.path()+"/authorize", "", q.Encode()
	resumed := r.Clone(r.Context())
	resumed.Method, resumed.URL, resumed.RequestURI = http.MethodGet, &u, u.RequestURI()
	resumed.Body, resumed.ContentLength, resumed.Form, resumed.PostForm = http.NoBody, 0, nil, nil
	resumed.Header.Del("Content-Type")
	t.handleAuthorize(w, resumed)
}
