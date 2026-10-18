package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// login runs the browser flow once and caches what came back.
//
// Authorization code with PKCE on a LOOPBACK port, which is the only
// browser flow left: the device flow was withdrawn because
// nobody signs in from a machine with no browser here. A loopback
// redirect is what makes a public client safe without a secret — the
// code comes back to a port only this process is listening on, and PKCE
// means a code seen by anything else cannot be redeemed.
func login(args []string) error {
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	issuer := flags.String("issuer", "", "the sluis issuer, e.g. https://access.example")
	clientID := flags.String("client", "", "the client to sign in as (default "+DefaultClientID+", else "+LegacyClientID+" if the issuer does not know it)")
	if err := flags.Parse(args); err != nil {
		return usageError{err}
	}

	cfg, err := loadLoginConfig(*issuer, *clientID)
	if err != nil {
		return err
	}

	if cfg.ClientDefaulted {
		cfg.ClientID = chooseDefaultClient(context.Background(), cfg.Issuer, os.Stderr)
	}

	// Port zero: the kernel picks, so two laptops and two shells do not
	// fight over one number, and the redirect is registered as a pattern
	// rather than an exact port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen on loopback: %w", err)
	}
	defer func() { _ = listener.Close() }()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)

	verifier := random()
	state := random()
	query := url.Values{
		"client_id":             {cfg.ClientID},
		"response_type":         {"code"},
		"scope":                 {"openid offline_access"},
		"redirect_uri":          {redirect},
		"state":                 {state},
		"code_challenge":        {challenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	where := cfg.Issuer + "/authorize?" + query.Encode()

	_, _ = fmt.Fprintln(stdout, "Opening your browser to sign in.")
	_, _ = fmt.Fprintln(stdout, "If it does not open, go to:")
	_, _ = fmt.Fprintln(stdout, "  "+where)
	openBrowser(where)

	code, err := waitForCode(listener, state)
	if err != nil {
		return err
	}

	session, err := redeem(context.Background(), cfg, code, verifier, redirect)
	if err != nil {
		return err
	}
	// Remember the sign-in, but never as a pin: `issuer:` stays whatever
	// the person wrote there.
	saved, err := readConfigFile()
	if err != nil {
		return err
	}
	saved.LastIssuer = cfg.Issuer
	if !cfg.ClientDefaulted {
		saved.ClientID = cfg.ClientID
	} else {
		session.Client = cfg.ClientID
	}
	if err = saveConfig(saved); err != nil {
		return err
	}
	if err = saveSession(cfg.Issuer, session); err != nil {
		return err
	}

	// Best-effort, and silent unless there is something configured: see
	// ssh_known_hosts.go. A sign-in must never fail, or print anything,
	// over a feature it never opted into.
	refreshKnownHostsAfterLogin(cfg)

	// Best-effort as well: where the installation's backup and restore
	// functions are, saved for `sluisctl backup` and `restore`.
	loginAfterBackup(context.Background(), cfg)

	who := session.Email
	if who == "" {
		who = session.Subject
	}
	_, _ = fmt.Fprintf(stdout, "Signed in as %s.\n", who)
	return nil
}

// waitForCode serves the one request the browser is about to make.
//
// It answers with a page rather than closing the connection, because a
// browser left on a failed request is how somebody concludes the sign-in
// did not work when it did.
func waitForCode(listener net.Listener, state string) (string, error) {
	type result struct {
		code string
		err  error
	}
	answers := make(chan result, 1)
	// answer keeps the first answer; a second one (a browser that asks twice)
	// is dropped rather than left blocking a handler.
	answer := func(r result) {
		select {
		case answers <- r:
		default:
		}
	}

	server := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			query := r.URL.Query()
			// The state is checked first, before anything else in the request
			// is believed. This listener is on the loopback interface for as
			// long as the browser takes, and anything that can reach it -- a
			// web page the person has open, a process on the machine -- can
			// send it an `?error=` or a code. Such a request is not the
			// issuer's answer to this sign-in and does not end it; the real
			// callback, which carries the state, can still arrive.
			if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(state)) != 1 {
				pageStatus(w, http.StatusBadRequest, "Not this sign-in", "That request does not belong to the sign-in in progress. It was ignored.")
				return
			}
			if failure := query.Get("error"); failure != "" {
				detail := query.Get("error_description")
				if detail == "" {
					detail = failure
				}
				page(w, "Sign-in refused", detail)
				answer(result{err: fmt.Errorf("the issuer refused the sign-in: %s", detail)})
				return
			}
			code := query.Get("code")
			if code == "" {
				page(w, "Sign-in refused", "No authorization code came back.")
				answer(result{err: errors.New("no authorization code came back")})
				return
			}
			page(w, "Signed in", "You can close this tab and go back to the terminal.")
			answer(result{code: code})
		}),
	}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		// Shut down, do not close: the handler hands over the answer before it
		// returns, and closing would cut the page off the browser (and make a
		// client retry the request against a listener that is gone).
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if server.Shutdown(ctx) != nil {
			_ = server.Close()
		}
	}()

	select {
	case answer := <-answers:
		return answer.code, answer.err
	case <-time.After(3 * time.Minute):
		// Long enough for a password manager, a second factor and a
		// consent screen; short enough that a forgotten terminal does
		// not hold a port all afternoon.
		return "", errors.New("timed out waiting for the browser")
	}
}

// redeem trades the code for tokens.
func redeem(ctx context.Context, cfg Config, code, verifier, redirect string) (Session, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirect},
		"client_id":     {cfg.ClientID},
		"code_verifier": {verifier},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Issuer+"/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return Session{}, fmt.Errorf("build the token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return Session{}, fmt.Errorf("%w: %w", errUnreachable, err)
	}
	defer func() { _ = response.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return Session{}, fmt.Errorf("the issuer refused the code: %s", strings.TrimSpace(string(body)))
	}

	var granted struct {
		RefreshToken string `json:"refresh_token"`
		AccessToken  string `json:"access_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err = json.Unmarshal(body, &granted); err != nil {
		return Session{}, fmt.Errorf("parse the token response: %w", err)
	}
	if granted.RefreshToken == "" {
		// Without one, every later command would open a browser. The
		// scope asked for offline_access, so this means the client is
		// declared without it.
		return Session{}, errors.New("the issuer returned no refresh token: this client cannot stay signed in")
	}

	session := Session{RefreshToken: granted.RefreshToken}
	// The access token that came with the code is kept, as a refresh would keep
	// it: the first command then presents it instead of spending the refresh
	// token for one just like it.
	if granted.AccessToken != "" && granted.ExpiresIn > 0 {
		session.AccessToken = granted.AccessToken
		session.AccessExpires = time.Now().Add(time.Duration(granted.ExpiresIn) * time.Second)
	}
	// The ID token is read for the person's own name only, and is not
	// verified here: it was just received over TLS from the issuer this
	// process chose, and nothing is authorized on it.
	if subject, email, ok := readIDToken(granted.IDToken); ok {
		session.Subject, session.Email = subject, email
	}
	return session, nil
}

// readIDToken reads `sub` and `email` for display. Unverified, and used
// for nothing else.
func readIDToken(token string) (subject, email string, ok bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", false
	}
	var claims struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
	}
	if err = json.Unmarshal(payload, &claims); err != nil {
		return "", "", false
	}
	return claims.Subject, claims.Email, true
}

// page is the one thing the browser sees from this process.
//
// Everything is escaped, and the message especially: it carries the
// issuer's `error_description` straight off the query string, so it is
// attacker-controlled by anyone who can make a browser visit this
// loopback port while a sign-in is running. Reflecting it unescaped was
// a reflected XSS, and CodeQL was right to say so.
func page(w http.ResponseWriter, title, message string) { pageStatus(w, http.StatusOK, title, message) }

// pageStatus is [page] with a status other than 200.
func pageStatus(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Nothing on this page loads a script, so the policy says exactly
	// that. It costs one header and closes the whole class rather than
	// this one instance of it.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	safeTitle, safeMessage := html.EscapeString(title), html.EscapeString(message)
	_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>%s</title>
<style>body{font:16px/1.5 system-ui,sans-serif;margin:4rem auto;max-width:32rem;padding:0 1rem}</style>
<h1>%s</h1><p>%s</p>`, safeTitle, safeTitle, safeMessage)
}

// openBrowser asks the desktop to open a URL, and does not care whether
// it worked: the address is printed above either way, which is the only
// thing that has to be true on a machine with no desktop at all.
func openBrowser(where string) {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command = "open"
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		command = "xdg-open"
	}
	_ = exec.Command(command, append(args, where)...).Start() //nolint:gosec // a URL this process built
}

func random() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// chooseDefaultClient picks the client id for a login that named none. It asks
// the issuer's token endpoint whether DefaultClientID is registered: an
// unknown client is answered `invalid_client` before the code is looked at,
// and a known one `invalid_grant`. On `invalid_client` it says on w that the
// legacy id is in use and returns LegacyClientID, once; any other answer, or
// none, keeps DefaultClientID, and the sign-in itself reports what is wrong.
// A client named by --client or config.yaml never comes here.
func chooseDefaultClient(ctx context.Context, issuer string, w io.Writer) string {
	if !clientRejected(ctx, issuer, DefaultClientID) {
		return DefaultClientID
	}
	_, _ = fmt.Fprintf(w, "sluisctl: %s does not know the client %q; signing in as the deprecated %q instead. "+
		"Ask whoever runs it to register a %q client beside it; %q will stop working in a later release.\n",
		issuer, DefaultClientID, LegacyClientID, DefaultClientID, LegacyClientID)
	return LegacyClientID
}

// clientRejected reports whether the issuer answers `invalid_client` to a
// token request made as client. The request carries a code that cannot be
// valid, so nothing is ever redeemed.
func clientRejected(ctx context.Context, issuer, client string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"probe"},
		"redirect_uri":  {"http://127.0.0.1/callback"},
		"client_id":     {client},
		"code_verifier": {random()},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	var refusal struct {
		Error string `json:"error"`
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
	if json.Unmarshal(body, &refusal) != nil {
		return false
	}
	return refusal.Error == "invalid_client"
}
