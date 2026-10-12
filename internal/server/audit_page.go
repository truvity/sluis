package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/storage/logattr"
)

// AuditQuery is the audit installation's query service, and how the console
// reaches it as the person signed in.
//
// The console's Audit page is the installation's own view, and it reads the
// trail through this console rather than from the browser: the browser keeps
// the console's session, and the query service gets a token for the person,
// minted here, that never reaches the browser. So there is no second sign-in,
// no bearer in the page, and no cross-origin call, and what the person may read
// is decided where the trail is kept, by its grants.
type AuditQuery struct {
	// URL is the query service.
	URL *url.URL
	// Token mints a token the query service accepts, for the person: the
	// issuer beside this console, for the installation's audience. An error
	// wrapping ErrNotAdmitted is somebody the audience does not admit.
	Token func(ctx context.Context, id access.Identity) (token string, expires time.Time, err error)
	// Transport reaches the query service; default http.DefaultTransport.
	Transport http.RoundTripper
}

// ErrNotAdmitted is a person the audit installation's audience does not
// admit: they may use the console and may not read the trail.
var ErrNotAdmitted = errors.New("not admitted to the audit trail")

// auditQueryPrefix is where the page's calls arrive, relative to the
// console's mount. Only the query service's methods pass.
const (
	auditQueryPrefix = "/audit/"
	auditService     = "audit.v1.QueryService/"
)

// auditProxy is the Audit page's way to the query service.
func (s *ConsoleServer) auditProxy() http.Handler {
	q := s.auditQuery
	tokens := &auditTokens{mint: q.Token, now: time.Now}
	transport := q.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(q.URL)
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.log.WarnContext(r.Context(), "the audit query service could not be reached", logattr.SafeError("error", err))
			connectRefusal(w, http.StatusBadGateway, "unavailable", "the audit trail cannot be read just now")
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := IdentityFrom(r.Context())
		if !ok {
			connectRefusal(w, http.StatusUnauthorized, "unauthenticated", "sign in first")
			return
		}
		method := strings.TrimPrefix(r.URL.Path, auditQueryPrefix)
		if r.Method != http.MethodPost || !strings.HasPrefix(method, auditService) || strings.Contains(method, "..") {
			http.NotFound(w, r)
			return
		}
		// A recovery sign-in is the root for the day the directory and the
		// policy cannot be trusted, and a token for the trail is the
		// directory's and the policy's to give: it is a person's, with the
		// groups the directory says they hold. Nothing here may mint one
		// for a recovery, so say so rather than fail trying.
		if id.Source == access.SourceRecovery {
			connectRefusal(w, http.StatusForbidden, "permission_denied", errRecoveryAudit)
			return
		}
		token, err := tokens.get(r.Context(), id)
		if err != nil {
			if errors.Is(err, ErrNotAdmitted) {
				connectRefusal(w, http.StatusForbidden, "permission_denied", "this sign-in may not read the audit trail")
				return
			}
			s.log.WarnContext(r.Context(), "a token for the audit trail could not be minted",
				logattr.SafeString("who", id.Who()), logattr.SafeError("error", err))
			connectRefusal(w, http.StatusBadGateway, "unavailable", "the audit trail cannot be read just now")
			return
		}
		out := r.Clone(r.Context())
		out.URL.Path = "/" + method
		out.URL.RawPath = ""
		// The console's own credentials stay here; the query service gets
		// the person's token for it and nothing else.
		out.Header.Del("Cookie")
		out.Header.Set("Authorization", "Bearer "+token)
		proxy.ServeHTTP(w, out)
	})
}

// errRecoveryAudit is said to a recovery sign-in that asks for the trail.
const errRecoveryAudit = "the audit trail is not read with a recovery sign-in: it is read as a person the directory admits, " +
	"and a recovery has no directory identity. Sign in through the directory to read it"

// connectRefusal answers in the Connect protocol's error JSON, which is what
// the page's client reads a message from; a plain-text body would reach the
// person as nothing but its status ("HTTP 502").
func connectRefusal(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

// auditTokens keeps one minted token per person until shortly before it
// expires, so that paging through the trail is not a token exchange per page.
type auditTokens struct {
	mint func(ctx context.Context, id access.Identity) (string, time.Time, error)
	now  func() time.Time

	mu   sync.Mutex
	held map[string]heldToken
}

type heldToken struct {
	token   string
	expires time.Time
}

// auditTokenMargin is how long before its expiry a token is replaced, so that
// one is never presented in the second it stops being accepted.
const auditTokenMargin = time.Minute

func (t *auditTokens) get(ctx context.Context, id access.Identity) (string, error) {
	who := id.Who()
	now := t.now()
	t.mu.Lock()
	if h, ok := t.held[who]; ok && now.Before(h.expires.Add(-auditTokenMargin)) {
		t.mu.Unlock()
		return h.token, nil
	}
	t.mu.Unlock()

	token, expires, err := t.mint(ctx, id)
	if err != nil {
		return "", err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.held == nil {
		t.held = map[string]heldToken{}
	}
	// Whoever's token has lapsed is forgotten on the way, so the map holds
	// the people reading the trail now and not everyone who ever did.
	for k, h := range t.held {
		if !now.Before(h.expires) {
			delete(t.held, k)
		}
	}
	t.held[who] = heldToken{token: token, expires: expires}
	return token, nil
}
