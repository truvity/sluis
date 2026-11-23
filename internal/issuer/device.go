package issuer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/httpsec"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/refusal"
	"github.com/truvity/sluis/internal/throttle"
	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/storage/logattr"
)

// The OAuth 2.0 device authorization grant (RFC 8628), for the one kind of
// client that needs it: a CLI a person runs on a machine they are not sitting
// at, over SSH, in a container, on a host with no browser. The CLI asks
// `/device_authorization` for a device code and a short user code, shows the
// person the code and where to type it, and polls `/token` with the device
// code. The person opens `/device` in any browser, signs in the normal way,
// sees which client asked, from where and when, and approves or denies.
//
// It is served only for a client that declares `device_authorization: true`
// ([policy.Client.DeviceAuthorization]). An installation that declares none
// serves none of it: the routes are 404, discovery does not advertise it, and
// the grant at `/token` is `unsupported_grant_type`.
//
// The tokens are not minted here. Approving completes an ordinary
// authorization request for the client, exactly as a loopback sign-in does
// ([Storage.Complete]: the entitlement check, the class, the sign-in record),
// and saves a code for it whose PKCE verifier only this issuer holds. The
// poll that finds the approval redeems that code through the same token
// endpoint a loopback CLI calls, so the lifetimes, scopes, claims, refresh
// rotation and session bookkeeping are the loopback sign-in's by
// construction, not by a second copy of them.
//
// What is kept, under `issuer:device:<sha256 of the device code>` and
// `issuer:device-user:<sha256 of the user code>` in the issuer's State, is
// written and read through the same interface every login in progress is, so
// every backend that keeps an authorization code keeps a device code. Neither
// code is ever a key or a log value as itself.

const (
	// deviceAuthorizationPath is RFC 8628 3.1's endpoint, and devicePath the
	// verification URI a person types (3.2).
	deviceAuthorizationPath = "/device_authorization"
	devicePath              = "/device"

	// deviceLifetime is how long a code may be answered: `expires_in`.
	deviceLifetime = 10 * time.Minute
	// deviceInterval is the poll interval a device is told, and
	// deviceSlowDown what each `slow_down` adds to it (RFC 8628 3.5).
	deviceInterval = 5 * time.Second
	deviceSlowDown = 5 * time.Second
	// deviceKept is how long the record outlives the code: long enough to
	// answer `expired_token` rather than `invalid_grant` to a device that
	// polls late, and to refuse a code a second time once it is spent.
	deviceKept = deviceLifetime + 10*time.Minute

	// deviceCodeBytes is the device code's entropy: 256 bits, base64url.
	deviceCodeBytes = 32
	// deviceUserCodeLength is the user code without its dash: 8 characters
	// of [op.CharSetBase20], about 34.6 bits, which a person types and the
	// verification page holds to a few guesses a minute per source.
	deviceUserCodeLength = 8
	// deviceUserAgentLimit is how much of a device's User-Agent is kept and
	// shown.
	deviceUserAgentLimit = 128
	// deviceFormLimit is the largest form either endpoint reads.
	deviceFormLimit = 4 << 10

	// devicePurpose is what the approval form's token is for.
	devicePurpose = "device"

	grantDeviceCode = string(oidc.GrantTypeDeviceCode)

	errSlowDown             = "slow_down"
	errAuthorizationPending = "authorization_pending"
	errExpiredToken         = "expired_token"
	errAccessDenied         = "access_denied"
)

// deviceStatus is where a device code stands.
type deviceStatus string

const (
	devicePending  deviceStatus = "pending"
	deviceApproved deviceStatus = "approved"
	deviceDenied   deviceStatus = "denied"
	// deviceUsed is a code whose tokens have been handed out: never again.
	deviceUsed deviceStatus = "used"
)

// deviceGrant is one device code, from issue to redemption.
type deviceGrant struct {
	ClientID string   `json:"client"`
	Scopes   []string `json:"scopes"`
	// UserCode is the normalised user code (no dash), for the page and for
	// the sign-in round trip to return to it.
	UserCode string    `json:"userCode"`
	Issued   time.Time `json:"issued"`
	Expires  time.Time `json:"expires"`
	// From and UserAgent are what the device's request said about itself,
	// shown on the approval page.
	From      string `json:"from"`
	UserAgent string `json:"userAgent,omitempty"`

	// Ref is a short, stable reference to this code for the audit trail
	// (the first 16 hex digits of the SHA-256 of the device code's key), so
	// that its issue, approval or denial and expiry join up. It opens nothing.
	Ref string `json:"ref"`

	Status deviceStatus `json:"status"`
	// Interval is the poll interval the code was issued with, in seconds.
	// What polls do to it is kept apart, in [devicePoll], so that a poll
	// never moves the revision an approval or a denial writes over.
	Interval int `json:"interval"`
	// ExpiryRecorded says the expiry has been audited, which happens once.
	ExpiryRecorded bool `json:"expiryRecorded,omitempty"`

	// Verifier is the PKCE verifier of the authorization request the
	// approval completes, and RedirectURI that request's redirect: neither
	// leaves this record.
	Verifier    string `json:"verifier"`
	RedirectURI string `json:"redirectUri"`
	// Request is the authorization request a sign-in from the verification
	// page completes, or the approval made; Code is the code saved for it on
	// approval, and Subject who approved.
	Request string `json:"request,omitempty"`
	Code    string `json:"code,omitempty"`
	Subject string `json:"subject,omitempty"`
}

func (g *deviceGrant) audit() audit.Device {
	return audit.Device{Ref: g.Ref, Client: g.ClientID, UserAgent: g.UserAgent, From: g.From, Expires: g.Expires}
}

// devicePoll is a device code's poll bookkeeping: when it was last polled
// and the interval it is held to, raised by each slow_down. It lives under a
// key of its own ([devicePollKey]) and is written with plain writes: two
// polls racing each other lose nothing that matters, and the grant's own
// revision belongs to the person deciding.
type devicePoll struct {
	LastPoll time.Time `json:"lastPoll,omitzero"`
	Interval int       `json:"interval"`
}

func deviceKey(hash string) string { return "issuer:device:" + hash }

func devicePollKey(hash string) string { return "issuer:device-poll:" + hash }

// deviceRef is the audit reference of a device code's key hash.
func deviceRef(hash string) string { return hashHex(hash)[:16] }

func deviceUserKey(userCode string) string { return "issuer:device-user:" + hashHex(userCode) }

func hashHex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// normalUserCode is a user code as a person may type it -- any case, with or
// without the dash or spaces -- reduced to its eight characters, or "" when
// it cannot be one.
func normalUserCode(typed string) string {
	var out strings.Builder
	for _, r := range strings.ToUpper(typed) {
		switch {
		case r == '-' || r == ' ':
			continue
		case !strings.ContainsRune(op.CharSetBase20, r):
			return ""
		}
		out.WriteRune(r)
		if out.Len() > deviceUserCodeLength {
			return ""
		}
	}
	if out.Len() != deviceUserCodeLength {
		return ""
	}
	return out.String()
}

// shownUserCode is the normalised code as it is shown: XXXX-XXXX.
func shownUserCode(code string) string {
	if len(code) != deviceUserCodeLength {
		return code
	}
	return code[:4] + "-" + code[4:]
}

// newUserCode draws one, normalised.
func newUserCode() (string, error) {
	code, err := op.NewUserCode([]rune(op.CharSetBase20), deviceUserCodeLength, 0)
	if err != nil {
		return "", fmt.Errorf("draw a user code: %w", err)
	}
	return code, nil
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("draw a random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// deviceUserAgent is the User-Agent kept for a device: valid UTF-8 with every
// control and format character dropped -- C0 and C1 controls (Cc), and the
// bidirectional overrides and isolates and the zero-width characters (Cf),
// which would let a device make the text it shows read as something else --
// and at most [deviceUserAgentLimit] runes. It is shown on a page (escaped
// there) and written in an audit record, both as what the device says about
// itself and nothing more.
func deviceUserAgent(raw string) string {
	var out strings.Builder
	n := 0
	for _, r := range raw {
		if n == deviceUserAgentLimit {
			break
		}
		if r == utf8.RuneError || unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		out.WriteRune(r)
		n++
	}
	return strings.TrimSpace(out.String())
}

// deviceFlow serves the grant over one issuer's storage and sign-in.
type deviceFlow struct {
	iss     *Issuer
	storage *Storage
	signIn  SignInDeps
	now     func() time.Time
	// issuing counts device codes issued per source, and refuses a source
	// that asks for more than its share a minute: every code is a write, and
	// the endpoint answers anyone. Only a request that would be issued counts
	// (a declared client with the grant, a well-formed form), so a stream of
	// junk cannot spend anybody's share.
	issuing *throttle.Limiter
	// guesses counts the user codes a signed-in person typed that name
	// nothing, per account and per source and never across all of them, and
	// stops looking codes up for either once it has spent its share: a user
	// code is short, and this is what keeps guessing one slower than its
	// lifetime. Nobody who is not signed in gets a code looked up at all.
	guesses *throttle.Limiter

	// decided, when set, runs between an approval's or a denial's read of
	// the grant and its write: for a test to land a poll in between.
	decided func()
}

// newDeviceFlow returns the flow over a concrete storage, or nil over a
// stand-in, which serves none of it.
func newDeviceFlow(iss *Issuer, storage op.Storage, signIn SignInDeps) *deviceFlow {
	own, ok := storage.(*Storage)
	if !ok {
		return nil
	}
	return &deviceFlow{
		iss: iss, storage: own, signIn: signIn,
		now:     func() time.Time { return own.now() },
		issuing: throttle.New(10, 600, time.Minute, 0, func() time.Time { return own.now() }),
		guesses: throttle.New(5, 0, time.Minute, 0, func() time.Time { return own.now() }),
		decided: func() {
			if own.deviceDecided != nil {
				own.deviceDecided()
			}
		},
	}
}

// served reports whether any client declares the grant.
func (d *deviceFlow) served() bool {
	return d != nil && len(d.iss.Policy().DeviceClients()) > 0
}

// --------------------------------------------------------------- protocol

// deviceProtocol answers, in front of the library, the device authorization
// endpoint and the device code grant at the token endpoint, and passes
// everything else through.
//
// It sits behind [singleValued], so a repeated `client_id`, `scope` or
// `device_code` has been refused already, and in front of every layer the
// code exchange it makes passes through.
func deviceProtocol(d *deviceFlow, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == deviceAuthorizationPath:
			if !d.served() {
				http.NotFound(w, r)
				return
			}
			d.authorize(w, r)
		case tokenPaths[r.URL.Path] && r.Method == http.MethodPost && r.PostFormValue("grant_type") == grantDeviceCode:
			d.token(w, r, next)
		case r.URL.Path == authorizeCallbackPath && d != nil && d.deviceRequest(r.Context(), callbackID(r)):
			// A request made for a device is approved at /device, never
			// handed to its redirect by the library's callback.
			_ = writePage(w, http.StatusBadRequest, "Use the device page",
				`<p>This sign-in is for a device. Approve it on the page that asked.</p>`)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// callbackID is the authorization request the library's callback acts on,
// read the way the library reads it ([op.ParseAuthorizeCallbackRequest]):
// from the parsed form, where a value in a POST body wins over the query. A
// guard that read the query alone would judge one request while the library
// completed another.
func callbackID(r *http.Request) string {
	if err := r.ParseForm(); err != nil {
		return ""
	}
	return r.Form.Get("id")
}

// deviceRequest reports whether an authorization request was made for a
// device code.
func (d *deviceFlow) deviceRequest(ctx context.Context, id string) bool {
	if id == "" {
		return false
	}
	req, err := d.storage.request(ctx, id)
	return err == nil && req.Device != ""
}

// deviceError answers the device with an OAuth error (RFC 6749 5.2, RFC 8628
// 3.5). The polling answers carry no description: they are a state, not a
// refusal.
func deviceError(w http.ResponseWriter, r *http.Request, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Basic realm="token"`)
	}
	w.WriteHeader(status)
	body := map[string]string{"error": code}
	switch code {
	case errAuthorizationPending, errSlowDown:
	default:
		body["error_description"] = refusal.Text(r.Context(), phraseOf(code))
	}
	_ = json.NewEncoder(w).Encode(body)
}

// presentedClient is the client id a device request names: in the form, or
// as the user of HTTP Basic (RFC 6749 2.3.1, form-encoded).
func presentedClient(r *http.Request) string {
	if id := r.PostForm.Get("client_id"); id != "" {
		return id
	}
	if user, _, ok := r.BasicAuth(); ok {
		if unescaped, err := url.QueryUnescape(user); err == nil {
			return unescaped
		}
	}
	return ""
}

// deviceClient is the declared client a device request names, and the OAuth
// error to answer when it may not use the grant: `invalid_client` for a
// client this installation does not declare, `unauthorized_client` for one
// that does not declare `device_authorization`.
func (d *deviceFlow) deviceClient(r *http.Request) (string, policy.Client, string) {
	id := presentedClient(r)
	declared, ok := d.iss.Policy().Client(id)
	switch {
	case id == "" || !ok:
		return id, policy.Client{}, string(oidc.InvalidClient)
	case !declared.DeviceAuthorization:
		return id, declared, string(oidc.UnauthorizedClient)
	}
	return id, declared, ""
}

// authorize is RFC 8628 3.1 and 3.2: a device asks for a code.
func (d *deviceFlow) authorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		deviceError(w, r, http.StatusMethodNotAllowed, string(oidc.InvalidRequest))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, deviceFormLimit)
	if err := r.ParseForm(); err != nil {
		logRefusal(r, http.StatusBadRequest, "the device authorization request is not a form", err, nil)
		deviceError(w, r, http.StatusBadRequest, string(oidc.InvalidRequest))
		return
	}
	for _, values := range r.PostForm {
		if len(values) > 1 {
			logRefusal(r, http.StatusBadRequest, "a device authorization parameter is repeated", nil, nil)
			deviceError(w, r, http.StatusBadRequest, string(oidc.InvalidRequest))
			return
		}
	}

	source := requestSource(r)
	id, declared, refused := d.deviceClient(r)
	noteClient(r.Context(), id)
	from, agent := source, deviceUserAgent(r.UserAgent())
	if refused != "" {
		why := "the client is not one this installation declares"
		status := http.StatusUnauthorized
		if refused == string(oidc.UnauthorizedClient) {
			why, status = "the client does not allow device_authorization", http.StatusBadRequest
			d.iss.record(r.Context(), audit.DeviceIssued(audit.Device{Client: id, UserAgent: agent, From: from}, audit.Denied(why)))
		}
		logRefusal(r, status, why, nil, []any{logattr.SafeString("client_id", id)})
		deviceError(w, r, status, refused)
		return
	}

	scopes, ok := deviceScopes(r.PostForm.Get("scope"))
	if !ok {
		logRefusal(r, http.StatusBadRequest, "the device asked for a scope this issuer does not grant", nil,
			[]any{logattr.SafeString("client_id", id)})
		deviceError(w, r, http.StatusBadRequest, string(oidc.InvalidScope))
		return
	}

	// Counted only now, for a request that would be issued.
	if d.issuing.Blocked(source) {
		logRefusal(r, http.StatusTooManyRequests, "this source has asked for too many device codes", nil, nil)
		w.Header().Set("Retry-After", "60")
		deviceError(w, r, http.StatusTooManyRequests, errSlowDown)
		return
	}
	d.issuing.Refused(source)

	now := d.now()
	grant := &deviceGrant{
		ClientID: id, Scopes: scopes, Issued: now, Expires: now.Add(deviceLifetime),
		From: from, UserAgent: agent, Status: devicePending, Interval: int(deviceInterval / time.Second),
		RedirectURI: declared.Redirects[0],
	}
	deviceCode, err := d.issue(r.Context(), grant)
	if err != nil {
		if errors.Is(err, errFrozenForMigration) {
			deviceError(w, r, http.StatusServiceUnavailable, string(oidc.ServerError))
			return
		}
		logRefusal(r, http.StatusInternalServerError, "the device code could not be stored", err, nil)
		d.iss.record(r.Context(), audit.DeviceIssued(grant.audit(), audit.Failed("the device code could not be stored")))
		deviceError(w, r, http.StatusInternalServerError, string(oidc.ServerError))
		return
	}
	d.iss.record(r.Context(), audit.DeviceIssued(grant.audit(), audit.Succeeded()))
	slog.Default().InfoContext(r.Context(), "issued a device code", logattr.SafeString("client", id),
		logattr.SafeString("from", from))

	verification := strings.TrimSuffix(d.iss.Config().URL, "/") + devicePath
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	_ = json.NewEncoder(w).Encode(oidc.DeviceAuthorizationResponse{
		DeviceCode:              deviceCode,
		UserCode:                shownUserCode(grant.UserCode),
		VerificationURI:         verification,
		VerificationURIComplete: verification + "?user_code=" + url.QueryEscape(shownUserCode(grant.UserCode)),
		ExpiresIn:               int(deviceLifetime / time.Second),
		Interval:                grant.Interval,
	})
}

// deviceScopes is the scope a device asked for, or `openid` when it named
// none. Only the scopes this issuer grants any client are accepted.
func deviceScopes(asked string) ([]string, bool) {
	scopes := strings.Fields(asked)
	if len(scopes) == 0 {
		return []string{oidc.ScopeOpenID}, true
	}
	var out []string
	for _, scope := range scopes {
		switch scope {
		case oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail, oidc.ScopeOfflineAccess:
		default:
			return nil, false
		}
		if !slices.Contains(out, scope) {
			out = append(out, scope)
		}
	}
	return out, true
}

// issue draws the codes and writes the grant. The user code is claimed with
// a conditional write, so two replicas drawing the same one cannot both
// believe they own it; a clash draws again.
func (d *deviceFlow) issue(ctx context.Context, grant *deviceGrant) (string, error) {
	if maintenance.ReadOnly(ctx) {
		return "", errFrozenForMigration
	}
	deviceCode, err := randomToken(deviceCodeBytes)
	if err != nil {
		return "", err
	}
	verifier, err := randomToken(deviceCodeBytes)
	if err != nil {
		return "", err
	}
	grant.Verifier = verifier
	hash := hashHex(deviceCode)
	grant.Ref = deviceRef(hash)

	for attempt := 0; ; attempt++ {
		if attempt == 5 {
			return "", errors.New("no free user code after five draws")
		}
		code, err := newUserCode()
		if err != nil {
			return "", err
		}
		claimed, err := d.storage.state.SetIfAbsent(ctx, deviceUserKey(code), []byte(hash), deviceKept)
		if err != nil {
			return "", err
		}
		if claimed {
			grant.UserCode = code
			break
		}
	}

	if err = setJSON(ctx, d.storage.state, deviceKey(hash), grant, deviceKept); err != nil {
		return "", err
	}
	return deviceCode, nil
}

// load reads a grant by the hash of its device code, with the revision it
// was read at.
func (d *deviceFlow) load(ctx context.Context, hash string) (*deviceGrant, string, error) {
	raw, version, found, err := getVersion(ctx, d.storage.state, deviceKey(hash))
	if err != nil || !found {
		return nil, "", err
	}
	var grant deviceGrant
	if err = json.Unmarshal(raw, &grant); err != nil {
		return nil, "", fmt.Errorf("issuer: a device code is not readable: %w", err)
	}
	return &grant, version, nil
}

// byUserCode reads a grant by its normalised user code.
func (d *deviceFlow) byUserCode(ctx context.Context, code string) (*deviceGrant, string, string, error) {
	raw, found, err := d.storage.state.Get(ctx, deviceUserKey(code))
	if err != nil || !found {
		return nil, "", "", err
	}
	hash := string(raw)
	grant, version, err := d.load(ctx, hash)
	if err != nil || grant == nil || grant.UserCode != code {
		return nil, "", "", err
	}
	return grant, hash, version, nil
}

// save writes a grant over the revision it was read at.
func (d *deviceFlow) save(ctx context.Context, hash string, grant *deviceGrant, version string) error {
	raw, err := json.Marshal(grant)
	if err != nil {
		return err
	}
	return replace(ctx, d.storage.state, deviceKey(hash), raw, d.remaining(grant), version)
}

// remaining is what is left of a grant's records' life, measured on the same
// clock that issued it.
func (d *deviceFlow) remaining(grant *deviceGrant) time.Duration {
	return max(deviceKept-d.now().Sub(grant.Issued), time.Minute)
}

// token is RFC 8628 3.4 and 3.5: a device polls with its code.
func (d *deviceFlow) token(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if !d.served() {
		logRefusal(r, http.StatusBadRequest, "no client allows the device authorization grant", nil, nil)
		deviceError(w, r, http.StatusBadRequest, string(oidc.UnsupportedGrantType))
		return
	}
	ctx := r.Context()
	id, _, refused := d.deviceClient(r)
	noteClient(ctx, id)
	if refused != "" {
		status := http.StatusUnauthorized
		if refused == string(oidc.UnauthorizedClient) {
			status = http.StatusBadRequest
		}
		logRefusal(r, status, "the client may not use the device authorization grant", nil,
			[]any{logattr.SafeString("client_id", id)})
		deviceError(w, r, status, refused)
		return
	}

	code := r.PostForm.Get("device_code")
	if code == "" {
		deviceError(w, r, http.StatusBadRequest, string(oidc.InvalidRequest))
		return
	}
	hash := hashHex(code)
	grant, version, err := d.load(ctx, hash)
	switch {
	case err != nil:
		logRefusal(r, http.StatusInternalServerError, "the device code could not be read", err, nil)
		deviceError(w, r, http.StatusInternalServerError, string(oidc.ServerError))
		return
	case grant == nil:
		logRefusal(r, http.StatusBadRequest, "the device code is not one this issuer issued, or is long gone", nil, nil)
		deviceError(w, r, http.StatusBadRequest, string(oidc.InvalidGrant))
		return
	case grant.ClientID != id:
		// RFC 8628 3.4: the device code is bound to the client it was
		// issued to; presented by another, it is not a code at all.
		logRefusal(r, http.StatusBadRequest, "the device code was issued to another client", nil,
			[]any{logattr.SafeString("client_id", id)})
		deviceError(w, r, http.StatusBadRequest, string(oidc.InvalidGrant))
		return
	}

	now := d.now()
	switch {
	case grant.Status == deviceUsed:
		logRefusal(r, http.StatusBadRequest, "the device code was already redeemed", nil, nil)
		deviceError(w, r, http.StatusBadRequest, string(oidc.InvalidGrant))
	case grant.Status == deviceDenied:
		deviceError(w, r, http.StatusBadRequest, errAccessDenied)
	case !now.Before(grant.Expires):
		d.noteExpiry(ctx, hash, grant, version)
		deviceError(w, r, http.StatusBadRequest, errExpiredToken)
	case grant.Status == devicePending:
		d.pending(w, r, hash, grant, now)
	case grant.Status == deviceApproved:
		d.redeem(w, r, hash, grant, version, next)
	default:
		deviceError(w, r, http.StatusBadRequest, string(oidc.InvalidGrant))
	}
}

// noteExpiry records, once, that a code ran out before anybody decided: when
// a poll or a visit to the device page first sees it. Nothing sweeps for it,
// so a code nobody asks about again leaves no record of its expiry.
func (d *deviceFlow) noteExpiry(ctx context.Context, hash string, grant *deviceGrant, version string) {
	if grant.ExpiryRecorded || grant.Status != devicePending {
		return
	}
	grant.ExpiryRecorded = true
	if d.save(ctx, hash, grant, version) == nil {
		d.iss.record(ctx, audit.DeviceExpired(grant.audit()))
	}
}

// pending answers a poll before anybody has decided: `authorization_pending`,
// or `slow_down` with five more seconds on the interval for a device that
// polled sooner than it was told to (RFC 8628 3.5).
func (d *deviceFlow) pending(w http.ResponseWriter, r *http.Request, hash string, grant *deviceGrant, now time.Time) {
	ctx := r.Context()
	poll, err := getJSON[devicePoll](ctx, d.storage.state, devicePollKey(hash))
	if err != nil {
		logRefusal(r, http.StatusInternalServerError, "the device code's polls could not be read", err, nil)
		deviceError(w, r, http.StatusInternalServerError, string(oidc.ServerError))
		return
	}
	if poll == nil {
		poll = &devicePoll{Interval: grant.Interval}
	}
	answer := errAuthorizationPending
	if !poll.LastPoll.IsZero() && now.Sub(poll.LastPoll) < time.Duration(poll.Interval)*time.Second {
		poll.Interval += int(deviceSlowDown / time.Second)
		answer = errSlowDown
	}
	poll.LastPoll = now
	// Its own key, never the grant's: a poll does not move the revision an
	// approval or a denial writes over.
	if err = setJSON(ctx, d.storage.state, devicePollKey(hash), poll, d.remaining(grant)); err != nil {
		logRefusal(r, http.StatusInternalServerError, "the device code's poll could not be recorded", err, nil)
		deviceError(w, r, http.StatusInternalServerError, string(oidc.ServerError))
		return
	}
	deviceError(w, r, http.StatusBadRequest, answer)
}

// redeem hands an approved code's tokens out, once: the grant is marked used
// over exactly the revision that was read, so of two polls racing one code
// one redeems it and the other is refused, and then the code saved at the
// approval is redeemed through the token endpoint as a loopback CLI's is.
func (d *deviceFlow) redeem(w http.ResponseWriter, r *http.Request, hash string, grant *deviceGrant, version string, next http.Handler) {
	code := grant.Code
	grant.Status, grant.Code = deviceUsed, ""
	if err := d.save(r.Context(), hash, grant, version); err != nil {
		if errors.Is(err, errMoved) || errors.Is(err, errGone) {
			logRefusal(r, http.StatusBadRequest, "the device code was redeemed by another poll", nil, nil)
			deviceError(w, r, http.StatusBadRequest, string(oidc.InvalidGrant))
			return
		}
		logRefusal(r, http.StatusInternalServerError, "the device code could not be marked used", err, nil)
		deviceError(w, r, http.StatusInternalServerError, string(oidc.ServerError))
		return
	}

	form := url.Values{
		"grant_type":    {string(oidc.GrantTypeCode)},
		"code":          {code},
		"redirect_uri":  {grant.RedirectURI},
		"client_id":     {grant.ClientID},
		"code_verifier": {grant.Verifier},
	}
	exchange, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "/token", strings.NewReader(form.Encode()))
	if err != nil {
		deviceError(w, r, http.StatusInternalServerError, string(oidc.ServerError))
		return
	}
	exchange.Header = r.Header.Clone()
	exchange.Header.Del("Authorization")
	exchange.Header.Del("Content-Length")
	exchange.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	exchange.Host, exchange.RemoteAddr, exchange.TLS = r.Host, r.RemoteAddr, r.TLS
	if err = exchange.ParseForm(); err != nil {
		deviceError(w, r, http.StatusInternalServerError, string(oidc.ServerError))
		return
	}
	next.ServeHTTP(w, exchange)
}

// ------------------------------------------------------------ the page

// devicePage serves `/device`: the form a person types a code into, the
// approval page behind the normal sign-in, and the approval itself.
func (d *deviceFlow) devicePage(w http.ResponseWriter, r *http.Request) {
	if !d.served() {
		http.NotFound(w, r)
		return
	}
	if d.signIn.SSO == nil || d.signIn.State == nil {
		logRefusal(r, http.StatusServiceUnavailable, "the device page needs browser sign-ins and a state key", nil, nil)
		_ = writePage(w, http.StatusServiceUnavailable, "Device sign-in is unavailable",
			`<p>This service cannot approve a device just now.</p>`)
		return
	}
	if r.Method == http.MethodPost {
		d.decide(w, r)
		return
	}

	typed := r.URL.Query().Get("user_code")
	if typed == "" {
		d.entry(w, "")
		return
	}
	// The shape alone says nothing about any code, so it is checked first.
	code := normalUserCode(typed)
	if code == "" {
		d.entry(w, `<p class="warn">That is not a code this service issues. Check it and type it again.</p>`)
		return
	}

	// Nobody who is not signed in gets a code looked up: whatever they
	// typed, they are sent to the same sign-in and learn nothing.
	session, ok := standingSignIn(d.signIn, w, r, "", 0)
	if !ok {
		d.toSignIn(w, r, code)
		return
	}

	source, account := requestSource(r), "account:"+strings.ToLower(session.Identity)
	if d.guesses.Blocked(account) || d.guesses.Blocked(source) {
		d.tooMany(w, r)
		return
	}
	grant, hash, version, err := d.byUserCode(r.Context(), code)
	if err != nil {
		refusalPage(w, r, http.StatusInternalServerError, "the device code could not be read", err)
		return
	}
	if grant == nil {
		// Per account and per source, never across everybody: one
		// person's guessing must not lock out another's approval.
		d.guesses.RefusedBySource(account)
		d.guesses.RefusedBySource(source)
		d.entry(w, `<p class="warn">No device is waiting with that code. It may have expired: start again on the device.</p>`)
		return
	}
	if !d.now().Before(grant.Expires) {
		d.noteExpiry(r.Context(), hash, grant, version)
	}
	if grant.Status != devicePending || !d.now().Before(grant.Expires) {
		d.closed(w, grant)
		return
	}
	d.confirm(w, r, grant, session)
}

// entry is the form a person types the code into.
func (d *deviceFlow) entry(w http.ResponseWriter, note string) {
	// The form's answer is a redirect to the sign-in, and from there to the
	// directory: see [httpsec.Policy.FormsMayRedirect].
	httpsec.Apply(w, httpsec.Policy{FormsMayRedirect: true})
	_ = writePage(w, http.StatusOK, "Sign in a device", note+
		`<p>Type the code your device shows.</p>
	<form method="get" action="`+devicePath+`">
		<p><input name="user_code" autocomplete="off" autocapitalize="characters" spellcheck="false" placeholder="XXXX-XXXX" maxlength="16" required></p>
		<p><button type="submit">Continue</button></p>
	</form>
	<p class="note">Only type a code you started yourself. Nobody from this service will ever ask you for one.</p>`)
}

func (d *deviceFlow) tooMany(w http.ResponseWriter, r *http.Request) {
	logRefusal(r, http.StatusTooManyRequests, "this source has typed too many codes that name nothing", nil, nil)
	w.Header().Set("Retry-After", "60")
	_ = writePage(w, http.StatusTooManyRequests, "Too many attempts",
		`<p>Too many codes were tried from here. Wait a minute and try again.</p>`)
}

// closed answers a code that can no longer be decided.
func (d *deviceFlow) closed(w http.ResponseWriter, grant *deviceGrant) {
	words := "This code has expired. Start again on the device."
	switch grant.Status {
	case deviceApproved, deviceUsed:
		words = "This code was already approved. Nothing more to do here."
	case deviceDenied:
		words = "This code was denied. Start again on the device if that was a mistake."
	}
	_ = writePage(w, http.StatusBadRequest, "Code no longer waiting", `<p>`+html.EscapeString(words)+`</p>`)
}

// toSignIn sends a browser with no standing sign-in through the normal
// sign-in, and back to this page with the code it typed
// ([Storage.deviceReturn]): an authorization request made here and bound to
// this browser as `/authorize` binds one. The code is not looked up first, so
// the sign-in is the same whatever was typed: to the first client (sorted)
// that declares the grant, which an installation that declares it on its CLI
// alone, as it should, makes the CLI. Completing that request issues
// nothing; the approval completes a request of its own.
func (d *deviceFlow) toSignIn(w http.ResponseWriter, r *http.Request, code string) {
	clients := d.iss.Policy().DeviceClients()
	declared, ok := d.iss.Policy().Client(clients[0])
	if !ok {
		refusalPage(w, r, http.StatusInternalServerError, "the device sign-in has no client", nil)
		return
	}
	verifier, err := randomToken(deviceCodeBytes)
	if err != nil {
		refusalPage(w, r, http.StatusInternalServerError, "the device sign-in could not be started", err)
		return
	}
	request, err := d.storage.deviceAuthRequest(r.Context(), &deviceGrant{
		ClientID: clients[0], Scopes: []string{oidc.ScopeOpenID}, RedirectURI: declared.Redirects[0],
		Verifier: verifier, UserCode: code,
	})
	if err != nil {
		refusalPage(w, r, http.StatusInternalServerError, "the device sign-in could not be started", err)
		return
	}

	held := ""
	if cookie, err := access.ReadCookie(r, access.AuthRequestCookieName, d.signIn.Secure); err == nil {
		held = cookie.Value
	}
	value, err := d.signIn.State.AddRequest(held, request)
	if err != nil {
		refusalPage(w, r, http.StatusInternalServerError, "the device sign-in could not be bound to this browser", err)
		return
	}
	access.SetCookie(w, access.AuthRequestCookie(value, d.signIn.Secure, authRequestTTL))
	http.Redirect(w, r, "/login?auth="+url.QueryEscape(request), http.StatusFound)
}

// deviceCSRFPurpose binds the approval form's token to the code it is for.
func deviceCSRFPurpose(code string) string { return devicePurpose + "\x00" + code }

// confirm shows what asked and offers Approve and Deny. Every word about the
// device is what its own request said, escaped: the client's name is the
// policy's, never the request's.
func (d *deviceFlow) confirm(w http.ResponseWriter, r *http.Request, grant *deviceGrant, session SSOSession) {
	token, err := d.signIn.State.CSRFToken(deviceCSRFPurpose(grant.UserCode), SSOFromRequest(r, d.signIn.Secure))
	if err != nil {
		refusalPage(w, r, http.StatusInternalServerError, "the device approval could not be prepared", err)
		return
	}
	name := grant.ClientID
	if declared, ok := d.iss.Policy().Client(grant.ClientID); ok {
		name = declared.Title(grant.ClientID)
	}
	agent := grant.UserAgent
	if agent == "" {
		agent = "(none sent)"
	}
	_ = writePage(w, http.StatusOK, "Approve a device?", fmt.Sprintf(
		`<p>A device asked to sign in to <strong>%s</strong> as you, <strong>%s</strong>.</p>
	<p>Code <strong class="host">%s</strong></p>
	<p class="note">Asked from <span class="host">%s</span> at %s<br>Program (as the device describes itself) <span class="host">%s</span></p>
	<p class="warn">Approve only if you started this yourself, just now, on a device you trust. Whoever holds that device will act as you.</p>
	<form method="post" action="%s">
		<input type="hidden" name="csrf" value="%s">
		<input type="hidden" name="user_code" value="%s">
		<p><button type="submit" name="decision" value="approve">Approve</button>
		<button type="submit" name="decision" value="deny">Deny</button></p>
	</form>`,
		html.EscapeString(name), html.EscapeString(session.Identity), html.EscapeString(shownUserCode(grant.UserCode)),
		html.EscapeString(grant.From), html.EscapeString(grant.Issued.UTC().Format("2006-01-02 15:04:05 UTC")),
		html.EscapeString(agent), devicePath, html.EscapeString(token), html.EscapeString(shownUserCode(grant.UserCode))))
}

// decide is the approval form's POST. It is not held to the guess limit: its
// token was minted for this sign-in and this code, on a page that showed the
// code, so it is not a guess, and a limit shared with anybody else must not
// stand between a signed-in person and their own approval.
func (d *deviceFlow) decide(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, deviceFormLimit)
	if err := r.ParseForm(); err != nil {
		refusalPage(w, r, http.StatusBadRequest, "the device approval is not a form", err)
		return
	}
	code := normalUserCode(r.PostForm.Get("user_code"))
	cookie := SSOFromRequest(r, d.signIn.Secure)
	if code == "" || !d.signIn.State.CSRFValid(deviceCSRFPurpose(code), cookie, r.PostForm.Get("csrf")) {
		recordLoginFailure(r.Context(), LoginBadState)
		logRefusal(r, http.StatusBadRequest, "the device decision did not come from the device page", nil, nil)
		_ = writePage(w, http.StatusBadRequest, "Nothing was decided",
			`<p>This answer did not come from the page that asks, so the device was neither approved nor denied.</p>`)
		return
	}
	session, ok := standingSignIn(d.signIn, w, r, "", 0)
	if !ok {
		_ = writePage(w, http.StatusBadRequest, "Sign in again",
			`<p>Your sign-in here has ended, so nothing was decided. <a href="`+
				html.EscapeString(devicePath+"?user_code="+url.QueryEscape(shownUserCode(code)))+`">Open the code again</a>.</p>`)
		return
	}

	decision := r.PostForm.Get("decision")
	if decision != "approve" && decision != "deny" {
		refusalPage(w, r, http.StatusBadRequest, "the device decision is neither approve nor deny", nil)
		return
	}
	ctx := r.Context()
	grant, hash, version, err := d.byUserCode(ctx, code)
	if err != nil {
		refusalPage(w, r, http.StatusInternalServerError, "the device code could not be read", err)
		return
	}
	if grant == nil {
		d.entry(w, `<p class="warn">No device is waiting with that code. It may have expired: start again on the device.</p>`)
		return
	}
	if grant.Status != devicePending || !d.now().Before(grant.Expires) {
		d.closed(w, grant)
		return
	}
	actor := audit.Identified(session.Identity)
	if isRecovery(session.How) {
		actor = audit.RecoveryIdentity(session.Identity)
	}
	subject := strings.ToLower(session.Identity)

	if decision == "deny" {
		if d.settle(w, r, hash, grant, version, func(g *deviceGrant) { g.Status, g.Subject = deviceDenied, subject }) {
			d.iss.record(ctx, audit.DeviceDenied(actor, grant.audit(), audit.Succeeded()))
			_ = writePage(w, http.StatusOK, "Device denied",
				`<p>The device was refused and signs nobody in. You can close this page.</p>`)
		} else {
			d.iss.record(ctx, audit.DeviceDenied(actor, grant.audit(), audit.Failed("the denial could not be saved")))
		}
		return
	}

	// Approve: an authorization request for the device's client, completed
	// as the person signed in here exactly as a sign-in at /authorize
	// completes one, and a code for it that the device's next poll redeems.
	who := Authenticated{Subject: session.Identity, AuthTime: session.AuthTime, SSO: session.ID, How: session.How}
	request, err := d.storage.deviceAuthRequest(ctx, grant)
	if err != nil {
		d.iss.record(ctx, audit.DeviceApproved(actor, grant.audit(), audit.Failed("the approval could not be made")))
		refusalPage(w, r, http.StatusInternalServerError, "the device approval could not be made", err)
		return
	}
	if err = d.storage.Complete(ctx, request, who); err != nil {
		status, why := http.StatusBadRequest, "the device approval could not be completed"
		if errors.Is(err, ErrNotEntitled) {
			status, why = http.StatusForbidden, "the person is not admitted to the device's client"
		}
		d.iss.record(ctx, audit.DeviceApproved(actor, grant.audit(), audit.Denied(why)))
		refusalPage(w, r, status, why, err)
		return
	}
	code, err = randomToken(deviceCodeBytes)
	if err == nil {
		err = d.storage.SaveAuthCode(ctx, request, code)
	}
	if err != nil {
		d.iss.record(ctx, audit.DeviceApproved(actor, grant.audit(), audit.Failed("the approval could not be saved")))
		refusalPage(w, r, http.StatusInternalServerError, "the device approval could not be saved", err)
		return
	}
	if !d.settle(w, r, hash, grant, version, func(g *deviceGrant) {
		g.Status, g.Code, g.Request, g.Subject = deviceApproved, code, request, subject
	}) {
		d.iss.record(ctx, audit.DeviceApproved(actor, grant.audit(), audit.Failed("the approval could not be saved")))
		return
	}
	d.iss.record(ctx, audit.DeviceApproved(actor, grant.audit(), audit.Succeeded()))
	recordLoginSuccess(ctx, "device")
	_ = writePage(w, http.StatusOK, "Device approved",
		`<p>The device signs in within a few seconds. You can close this page.</p>`)
}

// settle writes a decision over the grant as it was read. A poll never moves
// that revision ([devicePoll]); when something else did, the grant is read
// again and the decision applied once more while it is still waiting, a few
// times at most. It reports whether the decision was written, and has
// answered the browser when it was not.
func (d *deviceFlow) settle(w http.ResponseWriter, r *http.Request, hash string, grant *deviceGrant, version string,
	decide func(*deviceGrant),
) bool {
	ctx := r.Context()
	if d.decided != nil {
		d.decided()
	}
	for attempt := 0; ; attempt++ {
		decide(grant)
		err := d.save(ctx, hash, grant, version)
		if err == nil {
			return true
		}
		if !errors.Is(err, errMoved) || attempt == 3 {
			refusalPage(w, r, http.StatusInternalServerError, "the device decision could not be saved", err)
			return false
		}
		again, at, err := d.load(ctx, hash)
		switch {
		case err != nil:
			refusalPage(w, r, http.StatusInternalServerError, "the device code could not be read", err)
			return false
		case again == nil:
			_ = writePage(w, http.StatusBadRequest, "Code no longer waiting", `<p>This code has expired. Start again on the device.</p>`)
			return false
		case again.Status != devicePending || !d.now().Before(again.Expires):
			d.closed(w, again)
			return false
		}
		*grant, version = *again, at
	}
}

// deviceAuthRequest writes the authorization request a device's approval
// completes: for the device's client, its scopes and one of its registered
// redirects, with the PKCE challenge of a verifier only the grant holds.
func (s *Storage) deviceAuthRequest(ctx context.Context, grant *deviceGrant) (string, error) {
	out := &authRequest{
		ID: uuid.NewString(),
		Req: &oidc.AuthRequest{
			Scopes:              grant.Scopes,
			ResponseType:        oidc.ResponseTypeCode,
			ClientID:            grant.ClientID,
			RedirectURI:         grant.RedirectURI,
			CodeChallenge:       pkceChallenge(grant.Verifier),
			CodeChallengeMethod: oidc.CodeChallengeMethodS256,
		},
		AuthTime: s.now(),
		Device:   grant.UserCode,
	}
	if err := setJSON(ctx, s.state, requestKey(out.ID), out, authRequestTTL); err != nil {
		return "", err
	}
	return out.ID, nil
}

// deviceReturn is where a sign-in goes once it has completed a request: back
// to the device page for a request made for a device code, and where next
// says for any other.
func (s *Storage) deviceReturn(next func(context.Context, string) string) func(context.Context, string) string {
	return func(ctx context.Context, id string) string {
		if req, err := s.request(ctx, id); err == nil && req.Device != "" {
			return devicePath + "?user_code=" + url.QueryEscape(shownUserCode(req.Device))
		}
		return next(ctx, id)
	}
}

// deviceDiscovery is what discovery says about the grant: the endpoint, and
// the grant type, only while a client declares it.
func deviceDiscovery(issuerURL string, served func() bool) func(doc map[string]any) {
	return func(doc map[string]any) {
		if served == nil || !served() {
			return
		}
		doc["device_authorization_endpoint"] = strings.TrimSuffix(issuerURL, "/") + deviceAuthorizationPath
		doc["grant_types_supported"] = append(slices.Clone(servedGrantTypes), grantDeviceCode)
	}
}
