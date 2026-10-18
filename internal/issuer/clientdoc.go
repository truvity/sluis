package issuer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/truvity/sluis/internal/egress"
	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/storage/logattr"
)

// A client that identifies itself with a URL.
//
// The Model Context Protocol's clients are editors and hosted
// assistants: software this installation does not deploy and cannot
// enumerate, so it cannot be given a row in the policy. Such a client
// presents an HTTPS URL as its `client_id`, and that URL serves a
// document describing it. This resolves one.
//
// [policy.ClientDocuments] carries the argument for why admitting them
// is proportionate, and the allow-list that bounds it. What follows is
// the part that has to be careful, because every input here comes from
// the caller.
const (
	// documentMaxBytes is what will be read before the body is refused. A
	// client document is a few hundred bytes; the limit is here because
	// the URL is chosen by the caller, so the response is an untrusted
	// stream and an unbounded read of one is a way to exhaust this
	// process from outside.
	documentMaxBytes = 64 << 10
	// documentFetchTimeout bounds ONE attempt at the fetch, connection to
	// last byte. A sign-in is waiting on it, so it is finite; it is also
	// generous enough that one slow answer from somebody else's web
	// server does not fail a sign-in. The retry below runs inside the
	// sign-in's own request context, which has the last word.
	documentFetchTimeout = 10 * time.Second
	// documentRetryBackoff is the pause before the single retry of a
	// transient failure. Short: long enough for a reset connection or a
	// restarting pod to be gone, short enough to stay inside a sign-in.
	documentRetryBackoff = 300 * time.Millisecond
	// documentStaleFor is how long past its normal expiry the last good
	// copy of a document may still be served, and ONLY when refreshing it
	// failed on the transport (see Resolve).
	documentStaleFor = time.Hour
	// documentCacheFor is how long a fetched document is honoured without
	// asking again. Short enough that a client correcting its redirect
	// URIs is not locked out for an afternoon, long enough that a browser
	// redirect does not wait on somebody else's web server every time.
	documentCacheFor = 10 * time.Minute
	// documentCacheMin and documentCacheMax bound what the origin's
	// `Cache-Control` may ask for. The floor stops `no-store` or
	// `max-age=0` turning every sign-in into a fetch from somebody else's
	// web server; the ceiling stops a long `max-age` outliving a client's
	// correction of its redirect URIs by a day.
	documentCacheMin = time.Minute
	documentCacheMax = 24 * time.Hour
	// documentNameMax bounds the display name taken from the document.
	//
	// That name is shown on the sign-in page BEFORE anybody has proved
	// who they are, and for a document client it is written by whoever
	// served the document. It is therefore attacker-controlled text on a
	// pre-authentication page: the console escapes it, so the risk is not
	// injection but a name long enough to push the real one off the page,
	// or to be a sentence claiming to be somebody else.
	documentNameMax = 64
	// documentFailedFor is how long a failed fetch of one document is
	// remembered and answered again without asking its origin. The id is
	// chosen by the caller, so each unknown or broken one is a request from
	// this process to somebody else's server for as long as callers keep
	// asking. A document that has been corrected is picked up this long after.
	documentFailedFor = time.Minute
	// documentFailedMax bounds how many failures are remembered. Past it a
	// failure is not remembered, which is the old behaviour, rather than
	// memory growing with the ids a caller invents.
	documentFailedMax = 1024
	// documentHostRate and documentHostWindow bound how many fetches one host
	// is asked for in the window, whatever the ids: a host that serves
	// documents at a million paths is one origin, and sign-ins for them are not
	// a reason to fetch a million times.
	documentHostRate   = 30
	documentHostWindow = time.Minute
	// documentHostsMax bounds how many hosts the rate limit remembers.
	documentHostsMax = 1024
)

// errThrottled is a fetch not made because its host has been asked for too
// many documents lately.
var errThrottled = errors.New("too many client document fetches from this host; try again shortly")

// errTransient marks a fetch failure that says nothing about the document
// itself: the origin could not be reached in time, reset the connection or
// answered 5xx. Only these are retried, and only these may be answered from
// a stale copy. A redirect, a 4xx, a body that is too large or does not
// validate is the origin's answer and is never transient.
var errTransient = errors.New("transient")

// errRedirected is what the no-redirect policy returns, so a redirect is
// never mistaken for a transport failure.
var errRedirected = errors.New("redirected")

// errRefused is a non-5xx answer that is not a document.
var errRefused = errors.New("refused")

// errNotADocumentClient says the id was never a URL, so the caller should
// go on treating it as a declared client's id and refuse it as unknown.
var errNotADocumentClient = errors.New("not a client document URL")

// documentClients resolves document clients, and refuses the rest.
type documentClients struct {
	allow policy.ClientDocuments
	fetch *http.Client
	now   func() time.Time
	log   *slog.Logger
	// backoff is the pause before the retry; a field so tests need not wait.
	backoff time.Duration

	mu     sync.Mutex
	cached map[string]cachedDocument
	// failed remembers a fetch that failed, by client id, for [documentFailedFor].
	failed map[string]failedFetch
	// hosts is when each host was last fetched from, within [documentHostWindow].
	hosts map[string][]time.Time
}

type failedFetch struct {
	err   error
	until time.Time
}

// cacheLifetime is how long a response may be reused: its `max-age`,
// held between [documentCacheMin] and [documentCacheMax], and
// [documentCacheFor] when it states none. `no-store`, `no-cache` and
// `max-age=0` ask for the floor.
func cacheLifetime(header http.Header) time.Duration {
	life := documentCacheFor
	for _, value := range header.Values("Cache-Control") {
		for _, directive := range strings.Split(value, ",") {
			name, arg, _ := strings.Cut(strings.TrimSpace(directive), "=")
			switch strings.ToLower(name) {
			case "no-store", "no-cache":
				return documentCacheMin
			case "max-age":
				seconds, err := strconv.ParseInt(strings.Trim(arg, `"`), 10, 64)
				if err != nil || seconds < 0 {
					continue
				}
				if seconds > int64(documentCacheMax/time.Second) {
					seconds = int64(documentCacheMax / time.Second)
				}
				life = time.Duration(seconds) * time.Second
			}
		}
	}
	return max(documentCacheMin, min(life, documentCacheMax))
}

type cachedDocument struct {
	client policy.Client
	until  time.Time
}

// newDocumentClients builds a resolver over an allow-list.
//
// The HTTP client is its own, and deliberately not the process default:
// it follows no redirects. The draft puts the document AT the client id,
// so a redirect is either a misconfiguration or an attempt to reach
// somewhere the allow-list does not name -- and a redirect chain is how
// an allow-list on the first hop stops meaning anything.
func newDocumentClients(allow policy.ClientDocuments) *documentClients {
	return newDocumentClientsWith(allow, egress.Public)
}

// newDocumentClientsWith is [newDocumentClients] with the network policy the
// fetch connects under. The address is checked where the socket connects,
// after the name is resolved, so a host that answers a public address to a
// look and a private one to a connection is refused as the private one is.
func newDocumentClientsWith(allow policy.ClientDocuments, network egress.Policy) *documentClients {
	return &documentClients{
		allow:   allow,
		now:     time.Now,
		log:     slog.Default(),
		backoff: documentRetryBackoff,
		fetch: &http.Client{
			Timeout:   documentFetchTimeout,
			Transport: network.Transport(),
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return fmt.Errorf("%w: the document redirected to %s; a client document is served at its own id", errRedirected, req.URL.Redacted())
			},
		},
		cached: map[string]cachedDocument{},
		failed: map[string]failedFetch{},
		hosts:  map[string][]time.Time{},
	}
}

// Resolve turns a client id that is a URL into a public client.
//
// It returns [errNotADocumentClient] when the id is not a URL at all,
// which is the ordinary case and not a fault.
func (d *documentClients) Resolve(ctx context.Context, clientID string) (policy.Client, error) {
	if !d.allow.Enabled() {
		return policy.Client{}, errNotADocumentClient
	}

	target, err := documentURL(clientID)
	if err != nil {
		return policy.Client{}, err
	}
	if !d.allow.Permits(target) {
		// Named, because a refusal a person cannot act on wastes the
		// support conversation it causes.
		return policy.Client{}, fmt.Errorf(
			"%w: %q serves client documents, and %q is not among the origins this installation admits",
			ErrUnknownTarget, target.Host, target.Host)
	}

	if client, ok := d.fromCache(clientID); ok {
		return client, nil
	}

	if err := d.recentFailure(clientID); err != nil {
		return policy.Client{}, err
	}
	if !d.admitHost(target.Host) {
		return policy.Client{}, fmt.Errorf("fetch the client document at %s: %w", target.Redacted(), errThrottled)
	}

	client, life, err := d.loadWithRetry(ctx, target)
	if err != nil {
		// BOUNDED STALE-WHILE-ERROR, on the transport only. If the origin
		// could not be reached (timeout, reset, 5xx) and a copy that was
		// ALREADY VALIDATED expired less than documentStaleFor ago, that
		// copy is served, with a warning each time.
		//
		// Never on anything else: a document that changed and no longer
		// validates, a redirect, a 4xx, an oversized body are the origin
		// SAYING something, and honouring the old copy over it would be
		// honouring redirect URIs the client may have retired on purpose.
		//
		// Why this is safe: the copy passed every check when it was
		// fetched; it only grants what the allow-list's `requires` already
		// grants, since access is decided by the person's groups and not
		// by the document; and the window is short, so a retired redirect
		// URI outlives its retirement by at most the cache life plus an
		// hour -- and only while the origin is down.
		if stale, ok := d.fromStale(clientID); ok && errors.Is(err, errTransient) {
			d.log.WarnContext(ctx, "serving a stale client document: the origin could not be reached",
				logattr.SafeString("origin", target.Host), logattr.SafeString("client_id", clientID), logattr.SafeError("error", err))
			return stale, nil
		}
		if ctx.Err() == nil {
			// A fetch the caller gave up on says nothing about the document.
			d.rememberFailure(clientID, err)
		}
		return policy.Client{}, err
	}

	d.mu.Lock()
	d.cached[clientID] = cachedDocument{client: client, until: d.now().Add(life)}
	delete(d.failed, clientID)
	d.mu.Unlock()

	return client, nil
}

// recentFailure is the error this document's last fetch failed with, while it
// is still within [documentFailedFor], and nil otherwise.
func (d *documentClients) recentFailure(clientID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	f, ok := d.failed[clientID]
	if !ok {
		return nil
	}
	if !d.now().Before(f.until) {
		delete(d.failed, clientID)
		return nil
	}
	return f.err
}

func (d *documentClients) rememberFailure(clientID string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.now()
	if len(d.failed) >= documentFailedMax {
		for id, f := range d.failed {
			if !now.Before(f.until) {
				delete(d.failed, id)
			}
		}
		if len(d.failed) >= documentFailedMax {
			return
		}
	}
	d.failed[clientID] = failedFetch{err: err, until: now.Add(documentFailedFor)}
}

// admitHost counts a fetch against its host's allowance, and says whether it
// may go ahead.
func (d *documentClients) admitHost(host string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.now()
	recent := d.hosts[host][:0:0]
	for _, at := range d.hosts[host] {
		if now.Sub(at) < documentHostWindow {
			recent = append(recent, at)
		}
	}
	if len(recent) >= documentHostRate {
		d.hosts[host] = recent
		return false
	}
	if _, known := d.hosts[host]; !known && len(d.hosts) >= documentHostsMax {
		for h, times := range d.hosts {
			if len(times) == 0 || now.Sub(times[len(times)-1]) >= documentHostWindow {
				delete(d.hosts, h)
			}
		}
		if len(d.hosts) >= documentHostsMax {
			// Every slot is a host asked for within the minute. The table is
			// full of hosts that matter more than this one.
			return false
		}
	}
	d.hosts[host] = append(recent, now)
	return true
}

func (d *documentClients) fromCache(clientID string) (policy.Client, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	entry, ok := d.cached[clientID]
	if !ok {
		return policy.Client{}, false
	}
	if d.now().After(entry.until) {
		// Kept, not deleted: it is the stale copy, until its window ends.
		if d.now().After(entry.until.Add(documentStaleFor)) {
			delete(d.cached, clientID)
		}
		return policy.Client{}, false
	}
	return entry.client, true
}

// fromStale returns an expired entry still inside its stale window.
func (d *documentClients) fromStale(clientID string) (policy.Client, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	entry, ok := d.cached[clientID]
	if !ok || d.now().After(entry.until.Add(documentStaleFor)) {
		return policy.Client{}, false
	}
	return entry.client, true
}

// loadWithRetry is [documentClients.load] with one retry of a transient
// failure, after a short backoff, and never past the caller's context.
func (d *documentClients) loadWithRetry(ctx context.Context, target *url.URL) (policy.Client, time.Duration, error) {
	client, life, err := d.load(ctx, target)
	if err == nil || !errors.Is(err, errTransient) || ctx.Err() != nil {
		return client, life, err
	}

	timer := time.NewTimer(d.backoff)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return policy.Client{}, 0, err
	case <-timer.C:
	}
	return d.load(ctx, target)
}

// documentURL decides whether an id is a document URL, and refuses the
// shapes that are URLs but must not be fetched.
func documentURL(clientID string) (*url.URL, error) {
	if !strings.HasPrefix(clientID, "https://") {
		// `http://` is included here on purpose rather than refused
		// separately: an id that is not an HTTPS URL is simply not a
		// document client, and a declared client may legitimately be
		// named anything.
		return nil, errNotADocumentClient
	}
	target, err := url.Parse(clientID)
	if err != nil {
		return nil, fmt.Errorf("%w: %q is not a URL: %w", ErrUnknownTarget, clientID, err)
	}
	if target.Host == "" {
		return nil, fmt.Errorf("%w: %q names no host", ErrUnknownTarget, clientID)
	}
	if target.User != nil {
		return nil, fmt.Errorf("%w: a client document URL carries no credentials", ErrUnknownTarget)
	}
	if target.Fragment != "" {
		// The id is compared byte for byte against the document's own
		// `client_id`, and a fragment is not sent to the server -- so a
		// fragment makes the two disagree in a way nobody can see.
		return nil, fmt.Errorf("%w: a client document URL carries no fragment", ErrUnknownTarget)
	}
	// draft-ietf-oauth-client-id-metadata-document 3: the URL has a path
	// component, and no dot segments. A bare origin names a site, not a
	// document, and a dot segment is a second spelling of another id that a
	// byte-for-byte comparison would not see as the same.
	if target.Path == "" || target.Path == "/" {
		return nil, fmt.Errorf("%w: a client document URL has a path component; %q names only an origin", ErrUnknownTarget, clientID)
	}
	for _, segment := range strings.Split(target.EscapedPath(), "/") {
		switch strings.ToLower(segment) {
		case ".", "..", "%2e", "%2e%2e", ".%2e", "%2e.":
			return nil, fmt.Errorf("%w: a client document URL has no dot segments; %q has %q", ErrUnknownTarget, clientID, segment)
		}
	}
	return target, nil
}

// load fetches and validates one document.
func (d *documentClients) load(ctx context.Context, target *url.URL) (policy.Client, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, documentFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return policy.Client{}, 0, fmt.Errorf("client document request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := d.fetch.Do(req)
	if err != nil {
		// A redirect and a refused address are the answer, not a hiccup:
		// neither is retried, and neither is covered by a stale copy.
		if !errors.Is(err, errRedirected) && !errors.Is(err, egress.ErrBlocked) {
			err = fmt.Errorf("%w: %w", errTransient, err)
		}
		return policy.Client{}, 0, fmt.Errorf("fetch the client document at %s: %w", target.Redacted(), err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		kind := errRefused
		if resp.StatusCode >= 500 {
			kind = errTransient
		}
		return policy.Client{}, 0, fmt.Errorf("%w: the client document at %s answered %s", kind, target.Redacted(), resp.Status)
	}

	// One byte past the limit is enough to know it was exceeded, and
	// enough not to have read the rest.
	body, err := io.ReadAll(io.LimitReader(resp.Body, documentMaxBytes+1))
	if err != nil {
		return policy.Client{}, 0, fmt.Errorf("%w: read the client document at %s: %w", errTransient, target.Redacted(), err)
	}
	if len(body) > documentMaxBytes {
		return policy.Client{}, 0, fmt.Errorf("the client document at %s is larger than %d bytes", target.Redacted(), documentMaxBytes)
	}

	var doc struct {
		ClientID     string   `json:"client_id"`
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
		ClientURI    string   `json:"client_uri"`
		// What the document says about how it authenticates. Only read to
		// refuse a shared secret; see below.
		AuthMethod   string           `json:"token_endpoint_auth_method"`
		ClientSecret *json.RawMessage `json:"client_secret"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return policy.Client{}, 0, fmt.Errorf("the client document at %s is not JSON: %w", target.Redacted(), err)
	}

	// THE CHECK THAT MATTERS MOST. Without it a document served anywhere
	// allow-listed could claim to be any client at all -- including one
	// declared in the policy -- and the id a person sees, the id the
	// audit records and the id the token is minted for would all be a
	// name its holder chose. The draft requires the equality; this is
	// where it is enforced.
	if doc.ClientID != target.String() {
		return policy.Client{}, 0, fmt.Errorf(
			"the client document at %s calls itself %q; a document's `client_id` is the URL it is served from",
			target.Redacted(), doc.ClientID)
	}
	if len(doc.RedirectURIs) == 0 {
		return policy.Client{}, 0, fmt.Errorf("the client document at %s declares no redirect_uris", target.Redacted())
	}
	// A document is public, always, and is not a place for a shared secret:
	// anyone can fetch it, so a secret in it is no secret, and a method
	// that authenticates with one is a promise this issuer cannot keep for
	// a client it holds no secret of
	// (draft-ietf-oauth-client-id-metadata-document, "Metadata Document":
	// `token_endpoint_auth_method` MUST NOT be a shared-secret method and
	// `client_secret` MUST NOT be present). Refused, not ignored: ignoring
	// it admits the client as public while its author believes it
	// confidential.
	if sharedSecretMethod(doc.AuthMethod) {
		return policy.Client{}, 0, fmt.Errorf(
			"the client document at %s declares token_endpoint_auth_method %q; a client described by a document is public "+
				"and authenticates with no shared secret", target.Redacted(), doc.AuthMethod)
	}
	if doc.ClientSecret != nil {
		return policy.Client{}, 0, fmt.Errorf(
			"the client document at %s carries a client_secret; a document is public and holds no secret", target.Redacted())
	}

	return policy.Client{
		// Public, always. A document carries no secret and there is
		// nowhere to put one, so PKCE is the only thing standing between
		// a code and whoever intercepts it -- which is the same posture
		// every CLI here already has.
		Kind:        policy.KindPublic,
		DisplayName: documentName(doc.ClientName, target),
		Description: "Registered by the document it serves at its own address.",
		Redirects:   doc.RedirectURIs,
		// The gate, and it is the allow-list's rather than the document's:
		// a client does not get to say who may use it.
		Requires: d.allow.Requires,
		TTLCap:   d.allow.TTLCap,
	}, cacheLifetime(resp.Header), nil
}

// sharedSecretMethod reports a `token_endpoint_auth_method` that proves a
// client with a secret the issuer and the client share (RFC 7591 2,
// RFC 7523): `client_secret_basic`, `client_secret_post`, `client_secret_jwt`,
// and any other spelling that starts that way. `none` and `private_key_jwt`
// are not.
func sharedSecretMethod(method string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(method)), "client_secret")
}

// documentName is the name shown on the sign-in page, bounded.
//
// It is cleaned with what a declared client's display name is held to
// ([policy.CleanDisplayText]): a bidirectional override that reorders it on the
// screen, a zero-width or other formatting character, a line break or bytes
// that are not text are taken out, and it is cut to [documentNameMax]
// characters, never in the middle of one.
//
// Falling back to the host rather than to the whole URL: the host is the
// part a person can recognise, and it is the part the allow-list blessed.
func documentName(name string, target *url.URL) string {
	if name = policy.CleanDisplayText(name, documentNameMax); name != "" {
		return name
	}
	return target.Host
}
