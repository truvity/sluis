package issuer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/truvity/sluis/internal/access"
)

// SSOCookieName is the browser's session with the ISSUER, which is a
// different thing from the session a proxy holds for one console.
//
// The proxy's cookie says "this browser is signed in to *this* console".
// This one says "this browser has proved who it is to the issuer", and it
// is what makes the second console cost no login: the authorization
// request it sends completes against this session instead of a round trip
// to the corporate directory.
const SSOCookieName = "access_issuer_sso"

// SSOSession is that session, as a record.
//
// Its ID is the sign-in's identity everywhere -- the sessions opened under
// it, the operators' listing, revoking one browser -- and it is NOT a
// credential: the browser's cookie carries a separate random secret (see
// [SSO.Begin]), so an id seen on a console page or in a log signs nobody
// in.
//
// It holds when the person actually authenticated, which is the whole
// reason it is a record and not just a cookie: `auth_time` is a claim
// every token minted from this session carries, and a refresh an hour
// later must carry the same one. A session that only existed as a cookie
// could not answer "when", and could not be ended from anywhere but the
// browser holding it.
type SSOSession struct {
	ID       string `json:"id"`
	Identity string `json:"identity"`
	// How says what proved it: a provider kind ("google"), or "recovery".
	How string `json:"how"`
	// AuthTime is when the person authenticated, never when a token was
	// minted from it.
	AuthTime  time.Time `json:"auth_time"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Live reports whether the session is still usable at now.
func (s SSOSession) Live(now time.Time) bool { return now.Before(s.ExpiresAt) }

// Fresh reports whether the authentication is younger than `within`,
// which is what `max_age` on an authorization request asks. A zero or
// negative window is no requirement at all — `max_age=0` is turned into
// "authenticate again" where the request is read, not here, because a
// zero duration cannot mean both.
func (s SSOSession) Fresh(now time.Time, within time.Duration) bool {
	return within <= 0 || now.Sub(s.AuthTime) <= within
}

// SSO is the store of browser sessions the issuer holds.
//
// Shared, for the same reason the per-client index is: a browser signs in
// at one replica and comes back at another, and an SSO session that lived
// in one process would be silently absent half the time — which reads to
// a person as "it asked me to log in again, sometimes".
type SSO struct {
	state     State
	now       func() time.Time
	newID     func() string
	newSecret func() string
	lifetime  time.Duration
}

// NewSSO returns the store. lifetime is how long a browser stays signed
// in to the issuer without authenticating again.
func NewSSO(state State, lifetime time.Duration) *SSO {
	if lifetime <= 0 {
		lifetime = 12 * time.Hour
	}

	return &SSO{state: state, now: time.Now, newID: uuid.NewString, newSecret: newSSOSecret, lifetime: lifetime}
}

// SetClock replaces the clock, for tests.
func (s *SSO) SetClock(now func() time.Time) { s.now = now }

// SetIDs replaces the id source, for tests.
func (s *SSO) SetIDs(newID func() string) { s.newID = newID }

// SetSecrets replaces the source of cookie secrets, for tests.
func (s *SSO) SetSecrets(newSecret func() string) { s.newSecret = newSecret }

// The keys: the record carries the expiry, the set carries ids only, in
// the same shape the per-client index uses.
func ssoKey(id string) string { return "issuer:sso:" + id }

// ssoCookieKey is the pointer from a browser's cookie to the sign-in it
// proves: the key is a hash of the cookie's secret and the value is the
// sign-in's id, in the shape the refresh-token pointer has. The cookie is
// a bearer secret, so it is hashed into the key rather than written into
// the keyspace -- a store that can be read must not be a store whose keys
// can be replayed as cookies.
func ssoCookieKey(hash string) string { return "issuer:sso-cookie:" + hash }

// ssoCookieLabel separates this hash from every other the issuer takes of
// a secret: the same bytes presented as a refresh token hash to a
// different key.
const ssoCookieLabel = "sluis issuer sso-cookie v1"

// ssoCookieHash is the hex SHA-256 of the label and the secret.
func ssoCookieHash(secret string) string {
	sum := sha256.Sum256([]byte(ssoCookieLabel + "\x00" + secret))
	return hex.EncodeToString(sum[:])
}

// ssoSecretBytes is the cookie's entropy: 256 bits, 43 characters as
// base64url.
const ssoSecretBytes = 32

// newSSOSecret is a fresh cookie value, base64url without padding so it
// needs no quoting in a cookie.
func newSSOSecret() string {
	b := make([]byte, ssoSecretBytes)
	// crypto/rand.Read never returns an error: it fills b or crashes
	// the program, which is the right answer to a system with no
	// randomness to give.
	_, _ = rand.Read(b)

	return base64.RawURLEncoding.EncodeToString(b)
}

// ssoRecord is the stored form of a sign-in: the session and the hash of
// its cookie, which is what lets the sign-in's end remove the pointer
// with it. It is not [SSOSession] because nothing outside this file has
// any business with the hash -- the listing, the logs and the console see
// the session alone. A record written before the cookie had a secret of
// its own carries no hash, and no pointer leads to it: it is listed and
// can be ended, and no browser can use it.
type ssoRecord struct {
	SSOSession

	Cookie string `json:"cookie_hash,omitempty"`
}

// ssoClientsKey is the clients that were issued an ID token under one
// sign-in: the relying parties that hold a session with THIS issuer for
// that browser. It exists because the session index does not answer
// that question -- a session there is a refresh token, and a client that
// asked for `openid` alone holds none, yet it signed somebody in and has
// to be told when that ends (OIDC Back-Channel Logout 1.0). Found by the
// suite: its logout module signs in with no `offline_access`, the issuer
// recorded nothing, and so announced nothing.
func ssoClientsKey(id string) string  { return "issuer:sso-clients:" + id }
func ssoOfKey(identity string) string { return "issuer:sso-of:" + strings.ToLower(identity) }

// ssoAllKey is every sign-in, for the operator's view of what is open —
// the same shape sessions keep, and for the same reason: the incident
// case is the one where you do not know WHOSE to look for.
const ssoAllKey = "issuer:sso"

// List returns the live sign-ins, for one identity or for all of them.
//
// It exists because the console listed per-client sessions and not the
// SIGN-IN behind them, so revoking every row emptied the page and
// changed nothing about who could walk back in. What keeps letting you
// in has to be on the page that says what is open.
func (s *SSO) List(ctx context.Context, identity string) ([]SSOSession, error) {
	return s.list(ctx, identity, false)
}

// Like lists the sign-ins whose identity CONTAINS a substring -- prefix,
// suffix and middle, the same rule the session listing follows. There is
// no per-identity index that answers this, so it walks all of them, and
// that is why it is an operator's question at the service above.
func (s *SSO) Like(ctx context.Context, part string) ([]SSOSession, error) {
	return s.list(ctx, part, true)
}

func (s *SSO) list(ctx context.Context, identity string, contains bool) ([]SSOSession, error) {
	key := ssoAllKey
	if identity = strings.ToLower(strings.TrimSpace(identity)); identity != "" && !contains {
		key = ssoOfKey(identity)
	}

	ids, err := s.state.Members(ctx, key)
	if err != nil {
		return nil, err
	}

	out := make([]SSOSession, 0, len(ids))

	for _, id := range ids {
		session, live, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}

		if !live {
			// Self-repair, as the session index does: the record expired,
			// so the id is not a sign-in any more and the set should stop
			// claiming it is.
			if err = s.state.Remove(ctx, key, id); err != nil {
				return nil, err
			}

			continue
		}

		if contains && !strings.Contains(strings.ToLower(session.Identity), identity) {
			continue
		}

		out = append(out, session)
	}

	slices.SortFunc(out, func(a, b SSOSession) int { return b.AuthTime.Compare(a.AuthTime) })

	return out, nil
}

// Involve records that a client was issued an ID token under a sign-in.
// Idempotent, and it lives exactly as long as the sign-in.
func (s *SSO) Involve(ctx context.Context, id, clientID string) error {
	if id == "" || clientID == "" {
		return nil
	}

	return s.state.Add(ctx, ssoClientsKey(id), clientID, s.lifetime)
}

// Involved lists the clients a sign-in was used at, in no order.
func (s *SSO) Involved(ctx context.Context, id string) ([]string, error) {
	return s.state.Members(ctx, ssoClientsKey(id))
}

// Begin records a fresh authentication and returns the session and the
// secret the browser's cookie carries.
//
// The secret is returned once and stored nowhere: the store keeps its
// hash, as the pointer's key and in the record (so the end of the sign-in
// can remove the pointer). It is never logged.
func (s *SSO) Begin(ctx context.Context, identity, how string) (SSOSession, string, error) {
	now := s.now()
	session := SSOSession{
		ID:        s.newID(),
		Identity:  strings.ToLower(strings.TrimSpace(identity)),
		How:       how,
		AuthTime:  now,
		ExpiresAt: now.Add(s.lifetime),
	}
	secret := s.newSecret()
	hash := ssoCookieHash(secret)

	// The record before the pointer: a pointer is never left naming a
	// sign-in that is not there, and a record without its pointer is one
	// no browser holds, which ends at its lifetime like any other.
	if err := setJSON(ctx, s.state, ssoKey(session.ID), ssoRecord{SSOSession: session, Cookie: hash}, s.lifetime); err != nil {
		return SSOSession{}, "", err
	}

	if err := s.state.Set(ctx, ssoCookieKey(hash), []byte(session.ID), s.lifetime); err != nil {
		return SSOSession{}, "", err
	}

	for _, key := range []string{ssoAllKey, ssoOfKey(session.Identity)} {
		if err := s.state.Add(ctx, key, session.ID, s.lifetime); err != nil {
			return SSOSession{}, "", err
		}
	}

	return session, secret, nil
}

// Resolve returns the session a browser's cookie proves, and whether it
// is there and live: the cookie's hash names the pointer, the pointer
// names the record. An id presented as a cookie -- what the cookie held
// before it had a secret of its own -- hashes to no pointer and is
// answered as absent.
func (s *SSO) Resolve(ctx context.Context, cookie string) (SSOSession, bool, error) {
	if cookie == "" {
		return SSOSession{}, false, nil
	}

	hash := ssoCookieHash(cookie)

	raw, found, err := s.state.Get(ctx, ssoCookieKey(hash))
	if err != nil || !found {
		return SSOSession{}, false, err
	}

	record, live, err := s.get(ctx, string(raw))
	if err != nil || !live {
		return SSOSession{}, false, err
	}

	// The record must name this cookie too. A pointer outliving its record
	// cannot happen by design (they share a lifetime and end together),
	// and this is what makes it harmless if it ever does.
	if subtle.ConstantTimeCompare([]byte(record.Cookie), []byte(hash)) != 1 {
		return SSOSession{}, false, nil
	}

	return record.SSOSession, true, nil
}

// preUpgrade reads a sign-in by a cookie set before the cookie had a
// secret of its own, when the cookie held the sign-in's id. It answers
// only for a record with no cookie hash -- one written before then -- so
// a current sign-in is never reached by its id, and only sign-out calls
// it: it ends a sign-in, it never signs anybody in. Remove one release
// after the one that introduced the cookie secret, when every such
// record has expired.
func (s *SSO) preUpgrade(ctx context.Context, cookie string) (SSOSession, bool, error) {
	record, live, err := s.get(ctx, cookie)
	if err != nil || !live || record.Cookie != "" {
		return SSOSession{}, false, err
	}

	return record.SSOSession, true, nil
}

// Get returns a session by id, and whether it is there and live. An
// expired record is absent: "it ended" and "it never was" are the same
// answer.
//
// The id is not a credential: this is for what names a sign-in -- the
// operators' listing, revoking one browser -- never for a browser's
// cookie, which is [SSO.Resolve].
func (s *SSO) Get(ctx context.Context, id string) (SSOSession, bool, error) {
	record, live, err := s.get(ctx, id)

	return record.SSOSession, live, err
}

// get is [SSO.Get] with the cookie's hash.
func (s *SSO) get(ctx context.Context, id string) (ssoRecord, bool, error) {
	if id == "" {
		return ssoRecord{}, false, nil
	}

	record, err := getJSON[ssoRecord](ctx, s.state, ssoKey(id))
	if err != nil || record == nil {
		return ssoRecord{}, false, err
	}

	if !record.Live(s.now()) {
		return ssoRecord{}, false, nil
	}

	return *record, true, nil
}

// End removes one session. Ending what is not there is not an error:
// signing out twice is a person clicking twice.
//
// The cookie's pointer goes first: from that write on no browser is
// signed in by it, whatever happens to the rest.
func (s *SSO) End(ctx context.Context, id string) error {
	record, found, err := s.get(ctx, id)
	if err != nil {
		return err
	}

	if found {
		if record.Cookie != "" {
			if err = s.state.Delete(ctx, ssoCookieKey(record.Cookie)); err != nil {
				return err
			}
		}

		for _, key := range []string{ssoAllKey, ssoOfKey(record.Identity)} {
			if err = s.state.Remove(ctx, key, id); err != nil {
				return err
			}
		}
	}

	if err := s.state.Delete(ctx, ssoClientsKey(id)); err != nil {
		return err
	}

	return s.state.Delete(ctx, ssoKey(id))
}

// EndFor removes every session an identity has, and reports how many.
// This is the half of "sign out everywhere" that stops the NEXT silent
// sign-in; revoking the per-client sessions is the half that ends the
// ones already running.
func (s *SSO) EndFor(ctx context.Context, identity string) (int, error) {
	key := ssoOfKey(identity)

	ids, err := s.state.Members(ctx, key)
	if err != nil {
		return 0, err
	}

	ended := 0

	for _, id := range ids {
		// The record is read for the one thing the set does not say: the
		// hash that names its cookie's pointer.
		var record *ssoRecord
		if record, err = getJSON[ssoRecord](ctx, s.state, ssoKey(id)); err != nil {
			return ended, err
		}

		if record != nil && record.Cookie != "" {
			if err = s.state.Delete(ctx, ssoCookieKey(record.Cookie)); err != nil {
				return ended, err
			}
		}

		if err = s.state.Delete(ctx, ssoClientsKey(id)); err != nil {
			return ended, err
		}

		if err = s.state.Delete(ctx, ssoKey(id)); err != nil {
			return ended, err
		}

		if err = s.state.Remove(ctx, key, id); err != nil {
			return ended, err
		}

		ended++
	}

	return ended, nil
}

// Cookie is the browser's half. SameSite=Lax is load-bearing twice over:
// the cookie must travel on the top-level redirect from a console into
// /authorize, which Lax allows, and it must NOT travel on a cross-site
// POST, which is what makes the account page's buttons safe without a
// token of their own.
//
// value is the secret [SSO.Begin] returned, or "" to clear the cookie.
func (s *SSO) Cookie(value string, secure bool) *http.Cookie {
	cookie := &http.Cookie{
		Name:     access.CookieNameFor(SSOCookieName, secure),
		Value:    value,
		Path:     "/",
		MaxAge:   int(s.lifetime.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	if value == "" {
		cookie.MaxAge = -1
	}

	return cookie
}

// SSOFromRequest reads the cookie the browser is presenting: the secret
// [SSO.Resolve] turns into a session, never a session id.
//
// secure must be the flag the cookie was set with: it decides the name.
func SSOFromRequest(r *http.Request, secure bool) string {
	cookie, err := r.Cookie(access.CookieNameFor(SSOCookieName, secure))
	if err != nil {
		return ""
	}

	return cookie.Value
}
