package google

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"golang.org/x/oauth2"
	googleauth "golang.org/x/oauth2/google"
)

// revokeURL is where a refresh token is handed back.
const revokeURL = "https://oauth2.googleapis.com/revoke" //nolint:gosec // an endpoint, not a credential

// The two paths a Google flow returns to, appended to the console's own
// base URL. They are fixed because an operator types the whole redirect
// URI into a cloud console by hand, and the one thing that must not vary
// between installations is the part nobody can look up.
//
// Two, not one, because the two flows are answered by endpoints with
// opposite authorisation: the consent callback adopts a workspace and so
// demands an operator, while the sign-in callback is how a person becomes
// anyone at all and must be reachable by nobody. Sharing a path would
// mean one endpoint deciding which of those it was, from a parameter, at
// the moment it matters most.
const (
	CallbackPath       = "/connect/google/callback"
	SignInCallbackPath = "/login/google/callback"
)

// SignInScopes are what signing a person in asks for: who they are, and
// nothing else. The directory scopes belong to the consent flow, which an
// administrator grants once per company; a person signing in grants
// nothing on their company's behalf.
var SignInScopes = []string{"openid", "email", "profile"}

// ConsentScopes are what admin consent asks for: the four the hub reads
// with, plus the two that say who consented.
//
// The identity pair is not decoration. A refresh token acts as whoever
// granted it, and a console that cannot name that account can only show
// "someone at this company" beside a credential with directory-wide read
// — which is exactly the fact an operator needs when deciding whether it
// is still the right one. They are standard scopes: no consent-screen
// entry, no verification, nothing to justify.
var ConsentScopes = append([]string{"openid", "email"}, Scopes...)

// OAuthClient is the client an installation registered once with Google.
// One per installation, never one per company: the tenants a hub reads
// are the ones whose administrators consented to this client.
type OAuthClient struct {
	ID     string
	Secret string
	// BaseURL is where a browser reaches this console, with no trailing
	// slash. Both redirect URIs are derived from it rather than given
	// separately, so the two cannot disagree — and a mismatched redirect
	// is the failure that arrives late, in a cloud console's own words,
	// naming nothing useful.
	BaseURL string
	// HostedDomains are the Google Workspace domains a person may sign in
	// from. When set, a sign-in must carry an `hd` claim naming one of them
	// (compared without regard to case): an account of any other
	// organisation, and a consumer account, which has no `hd`, is refused
	// before the address is read. Empty asks nothing of `hd`, and the
	// directory the hub reads still decides which domains it serves.
	HostedDomains []string
}

// ConsentRedirect is where the consent flow returns; one of the two URIs
// to register with the client.
func (c OAuthClient) ConsentRedirect() string { return c.BaseURL + CallbackPath }

// SignInRedirect is where the sign-in flow returns; the other one.
func (c OAuthClient) SignInRedirect() string { return c.BaseURL + SignInCallbackPath }

func (c OAuthClient) config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.ID,
		ClientSecret: c.Secret,
		Endpoint:     googleauth.Endpoint,
		RedirectURL:  c.ConsentRedirect(),
		Scopes:       ConsentScopes,
	}
}

func (c OAuthClient) signInConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.ID,
		ClientSecret: c.Secret,
		Endpoint:     googleauth.Endpoint,
		RedirectURL:  c.SignInRedirect(),
		Scopes:       SignInScopes,
	}
}

// SignInURL is where the browser goes to prove who somebody is.
//
// No offline access and no forced screen: nothing here is stored, so
// there is no refresh token to want, and a person who signed in a minute
// ago should not be asked again. The account chooser is Google's, which
// is why this hub asks nobody for an address first.
//
// The request carries a `nonce` derived from the state ([Nonce]), which
// Google copies into the ID token: [IdentifyAccount] refuses a token whose nonce is
// not the one of the state the callback came back with, so that a code
// obtained for another sign-in (another browser's, or another person's)
// cannot complete this one.
func (c OAuthClient) SignInURL(state string) string {
	return c.signInConfig().AuthCodeURL(state, oauth2.SetAuthURLParam("nonce", Nonce(state)))
}

// Nonce is the OpenID Connect `nonce` of the sign-in started with state: a
// hash of it, so that the callback recomputes it from the state it was handed,
// which the caller has already held to the browser that started the sign-in.
// It needs no storage of its own, and it is not secret: it travels in the same
// URL as the state.
func Nonce(state string) string {
	sum := sha256.Sum256([]byte("sluis google sign-in nonce\x00" + state))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Account is who a sign-in proved: the address, and Google's stable account
// id (`sub`), which stays with the account when the address is renamed and is
// never given to another account when the address is.
type Account struct {
	Email   string
	Subject string
}

// Identify turns a sign-in callback's code into the address that
// authenticated, without checking the ID token's nonce.
//
// Deprecated: use [IdentifyAccount], which checks the nonce [OAuthClient.SignInURL]
// sends against the callback's state and returns the account's `sub`.
func Identify(ctx context.Context, client OAuthClient, code string) (string, error) {
	if code == "" {
		return "", errors.New("google: the sign-in returned no code")
	}
	token, err := client.signInConfig().Exchange(ctx, code)
	if err != nil {
		return "", fmt.Errorf("google: exchange the sign-in code: %w", err)
	}
	account, err := signedInAccount(token, client.HostedDomains, "")
	return account.Email, err
}

// IdentifyAccount turns a sign-in callback's code into the account that
// authenticated. state is the callback's state, which the caller has already
// held to this browser: the ID token must carry its [Nonce]. The hub decides
// everything else from the address, through the directory it already reads;
// the subject is for the caller to pin the address to.
func IdentifyAccount(ctx context.Context, client OAuthClient, code, state string) (Account, error) {
	if code == "" {
		return Account{}, errors.New("google: the sign-in returned no code")
	}
	if state == "" {
		return Account{}, errors.New("google: the sign-in has no state to check its nonce against")
	}
	token, err := client.signInConfig().Exchange(ctx, code)
	if err != nil {
		return Account{}, fmt.Errorf("google: exchange the sign-in code: %w", err)
	}
	return signedInAccount(token, client.HostedDomains, Nonce(state))
}

// signedInAccount is the address a sign-in's ID token vouches for, when Google
// vouches for it.
//
// Four things are asked of the token beyond an address. Its `nonce` must be
// the one this sign-in sent ([Nonce]). `email_verified` must
// be true: Google says the account has proved it owns the address, and an
// address it has not verified is a string somebody typed. When hosted domains
// are configured, `hd` must be one of them, so that an address of the
// right shape from another organisation or from a consumer account is not
// taken for one of ours. And the token must carry its `sub`, Google's stable
// account id, which a token without is not one this client was handed.
//
// The identity is still the address, as every link and every grant here is
// keyed by it. `sub` is returned beside it, for the caller to pin the address
// to the account that first signed in with it, so that an address deleted and
// given to a new account is not taken for the old one.
func signedInAccount(token *oauth2.Token, hostedDomains []string, nonce string) (Account, error) {
	claims, ok := idTokenClaims(token)
	if !ok {
		return Account{}, errors.New("google: the sign-in did not say which account it was; " +
			"the OAuth client must be allowed the openid and email scopes")
	}
	// nonce is empty only for the deprecated [Identify], which checks none.
	if nonce != "" && (claims.Nonce == "" || subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(nonce)) != 1) {
		return Account{}, errors.New("google: the sign-in's ID token is not for this sign-in (its nonce is missing or another one)")
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email == "" {
		return Account{}, errors.New("google: the sign-in did not say which account it was; " +
			"the OAuth client must be allowed the openid and email scopes")
	}
	if claims.Subject == "" {
		return Account{}, errors.New("google: the sign-in carried no subject")
	}
	if !claims.EmailVerified.true() {
		return Account{}, errors.New("google: the account's address is not verified")
	}
	if len(hostedDomains) > 0 {
		hosted := strings.TrimSpace(claims.HostedDomain)
		if !slices.ContainsFunc(hostedDomains, func(d string) bool { return strings.EqualFold(strings.TrimSpace(d), hosted) }) || hosted == "" {
			return Account{}, errors.New("google: the account is not in a hosted domain this installation signs in")
		}
	}
	return Account{Email: email, Subject: claims.Subject}, nil
}

// verifiedFlag is Google's `email_verified`, a boolean in an ID token and a
// string in some of its older responses. Absent or anything but true is not
// verified.
type verifiedFlag struct{ value bool }

func (f *verifiedFlag) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil //nolint:nilerr // unreadable is not verified
	}
	switch v := raw.(type) {
	case bool:
		f.value = v
	case string:
		f.value = v == "true"
	}
	return nil
}

func (f verifiedFlag) true() bool { return f.value }

// idTokenClaim are the claims of an ID token a sign-in reads.
type idTokenClaim struct {
	Subject       string       `json:"sub"`
	Email         string       `json:"email"`
	EmailVerified verifiedFlag `json:"email_verified"`
	HostedDomain  string       `json:"hd"`
	Nonce         string       `json:"nonce"`
}

// idTokenClaims reads the claims out of the ID token that came back with a
// grant. The token is not verified here for the reason [consentingAccount]
// gives: it arrived from Google's token endpoint over TLS, in the response to
// a request carrying this client's secret.
func idTokenClaims(token *oauth2.Token) (idTokenClaim, bool) {
	raw, _ := token.Extra("id_token").(string)
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return idTokenClaim{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return idTokenClaim{}, false
	}
	var claims idTokenClaim
	if err = json.Unmarshal(payload, &claims); err != nil {
		return idTokenClaim{}, false
	}
	return claims, true
}

// AuthURL is where the browser goes to consent.
//
// Offline access is what makes a refresh token arrive at all, and forcing
// the screen is what makes one arrive *every* time: Google returns a
// refresh token only on a grant the administrator actually saw, so a
// second consent that silently reuses the first would hand back an access
// token good for an hour and nothing to renew it with. Reconnecting a
// workspace is precisely the case where that would be a silent failure.
func (c OAuthClient) AuthURL(state string) string {
	return c.config().AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
}

// Consent turns a callback code into an open backend.
//
// The returned backend acts as the administrator who consented, and
// carries the refresh token as its credential so that a store can write
// it down.
func Consent(ctx context.Context, client OAuthClient, code string) (*Backend, error) {
	if code == "" {
		return nil, errors.New("google: the consent returned no code")
	}
	token, err := client.config().Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("google: exchange the consent code: %w", err)
	}
	if token.RefreshToken == "" {
		// Without one, the connection would work for an hour and then
		// quietly stop. Refusing now, while an operator is watching the
		// screen that caused it, is the only honest moment to say so.
		return nil, errors.New("google: the consent returned no refresh token; " +
			"the grant must be offline and freshly approved, so press Connect again and complete the consent screen")
	}
	admin := consentingAccount(token)
	if admin == "" {
		return nil, errors.New("google: the consent did not say which account granted it; " +
			"the OAuth client must be allowed the openid and email scopes")
	}
	return OpenWithToken(ctx, client, token.RefreshToken, admin)
}

// consentingAccount reads the address out of the id token that came back
// with the grant.
//
// The token is not verified here, and does not need to be: it did not
// arrive from a browser but from Google's own token endpoint over TLS, in
// the response to a request carrying this client's secret. Nothing but
// Google could have produced it, and nothing about the grant is decided
// from it — it names the account for a human to read.
func consentingAccount(token *oauth2.Token) string {
	raw, _ := token.Extra("id_token").(string)
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err = json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(claims.Email))
}

// VerifyClient reports whether this OAuth client's id and secret are the
// ones Google holds, without needing a person.
//
// It exists because the alternative is finding out after an administrator
// has already consented: a wrong secret fails at the exchange, which is
// the step AFTER the consent screen, so a real Super Admin has granted a
// real credential to an installation that cannot collect it.
//
// The method is the one OAuth actually offers. There is no "check my
// client" endpoint, so this presents a code that cannot be valid and
// reads which complaint comes back: `invalid_client` is about the client,
// and anything else — `invalid_grant`, for the code — means the client
// itself was accepted. Nothing is minted either way.
//
// Everything inconclusive passes. A refusal here stops an operator from
// connecting, so it is made only when Google has named the client as the
// problem; a timeout, a proxy, an unexpected shape are all "proceed".
func VerifyClient(ctx context.Context, client OAuthClient) error {
	if client.ID == "" || client.Secret == "" {
		return errors.New("google: the OAuth client id and secret are both required")
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"sluis-client-check"},
		"client_id":     {client.ID},
		"client_secret": {client.Secret},
		"redirect_uri":  {client.ConsentRedirect()},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		googleauth.Endpoint.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil //nolint:nilerr // inconclusive is not a refusal
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil //nolint:nilerr // inconclusive is not a refusal
	}
	defer func() { _ = response.Body.Close() }()

	var body struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&body); err != nil {
		return nil //nolint:nilerr // inconclusive is not a refusal
	}
	if body.Error != "invalid_client" {
		return nil
	}
	detail := body.Description
	if detail == "" {
		detail = "Google did not recognise this client id and secret"
	}
	return fmt.Errorf("google: the OAuth client was refused (%s). "+
		"Check the client id and secret against the Google Cloud project that owns them, "+
		"and that the client is a Web application with %s among its authorised redirect URIs",
		detail, client.ConsentRedirect())
}
