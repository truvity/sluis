package resourceproxy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/truvity/sluis/tokens"
)

// caller is who an inbound request was made by, as the outbound side needs
// them: the token they presented, already verified, and its subject for the
// audit line.
type caller struct {
	token   string
	subject string
}

// Handles are the inbound requests in flight in [ModeDelegate], each under an
// opaque handle the stock server carries to the outbound side. A handle is
// 256 random bits, names nothing, and is gone when its request ends, so the
// stock server holds nothing it could use after the request, or anywhere but
// on this process's loopback.
type Handles struct {
	mu      sync.Mutex
	callers map[string]caller
}

// NewHandles is an empty set of handles.
func NewHandles() *Handles { return &Handles{callers: map[string]caller{}} }

// Open registers one request's caller and returns its handle, and the func
// that ends it.
func (h *Handles) Open(token, subject string) (string, func()) {
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	handle := base64.RawURLEncoding.EncodeToString(raw[:])
	h.mu.Lock()
	h.callers[handle] = caller{token: token, subject: subject}
	h.mu.Unlock()
	return handle, func() {
		h.mu.Lock()
		delete(h.callers, handle)
		h.mu.Unlock()
	}
}

// lookup is the caller of a handle in flight.
func (h *Handles) lookup(handle string) (caller, bool) {
	if handle == "" {
		return caller{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.callers[handle]
	return c, ok
}

// errNoCaller is a backend call that names no inbound request in flight.
var errNoCaller = errors.New("no caller to act for: the request carries no live delegation handle")

// Delegator trades a caller's token for one the backend accepts, acting for
// them with this workload's own ServiceAccount token as the actor (RFC 8693
// delegation), and keeps what it was given per caller token until shortly
// before it expires.
//
// A cached token is keyed by the SHA-256 of the caller's token, never by the
// token: the cache holds the delegated tokens, which are the backend's, and no
// caller's credential. It holds at most size entries; a full cache drops the
// expired ones first and then the one that expires soonest.
type Delegator struct {
	ex       *tokens.Exchanger
	file     string
	audience string
	margin   time.Duration
	size     int
	now      func() time.Time

	mu       sync.Mutex
	cache    map[[sha256.Size]byte]delegated
	inflight map[[sha256.Size]byte]*exchange
}

type delegated struct {
	token   string
	issued  time.Time
	expires time.Time
}

// exchange is one exchange in progress, which concurrent calls for the same
// caller wait for instead of making their own.
type exchange struct {
	done  chan struct{}
	token delegated
	err   error
}

// NewDelegator builds the delegator from a validated Config. client is the
// transport to the issuer; nil uses tokens' default.
func NewDelegator(cfg Config, client *http.Client) (*Delegator, error) {
	issuer, err := issuerOf(cfg.OutboundTokenEndpoint)
	if err != nil {
		return nil, err
	}
	return &Delegator{
		ex:       &tokens.Exchanger{Issuer: issuer, ClientID: cfg.OutboundClientID, Client: client},
		file:     cfg.OutboundSATokenFile,
		audience: cfg.OutboundAudience,
		margin:   cfg.RefreshBefore,
		size:     cfg.DelegationCacheSize,
		now:      time.Now,
		cache:    map[[sha256.Size]byte]delegated{},
		inflight: map[[sha256.Size]byte]*exchange{},
	}, nil
}

// Token returns a token for the backend that acts for the holder of
// callerToken, and whether it came from the cache.
func (d *Delegator) Token(ctx context.Context, callerToken string) (string, bool, error) {
	key := sha256.Sum256([]byte(callerToken))

	d.mu.Lock()
	now := d.now()
	if got, ok := d.cache[key]; ok {
		if got.expires.Sub(now) > effectiveMargin(d.margin, got.issued, got.expires) {
			d.mu.Unlock()
			return got.token, true, nil
		}
		delete(d.cache, key)
	}
	if running, ok := d.inflight[key]; ok {
		d.mu.Unlock()
		select {
		case <-running.done:
			return running.token.token, false, running.err
		case <-ctx.Done():
			return "", false, ctx.Err()
		}
	}
	running := &exchange{done: make(chan struct{})}
	d.inflight[key] = running
	d.mu.Unlock()

	running.token, running.err = d.exchange(ctx, callerToken)

	d.mu.Lock()
	delete(d.inflight, key)
	if running.err == nil {
		d.keep(key, running.token)
	}
	d.mu.Unlock()
	close(running.done)
	return running.token.token, false, running.err
}

func (d *Delegator) exchange(ctx context.Context, callerToken string) (delegated, error) {
	actor, err := readToken(d.file)
	if err != nil {
		return delegated{}, err
	}
	issued := d.now()
	granted, err := d.ex.Delegate(ctx, callerToken, actor, tokens.TypeJWT, d.audience)
	if err != nil {
		return delegated{}, err
	}
	out := delegated{token: granted.AccessToken, issued: issued, expires: granted.Expires}
	if out.expires.IsZero() {
		out.expires = issued.Add(unknownLifetime)
	}
	return out, nil
}

// keep stores one token, making room first. Called with d.mu held.
func (d *Delegator) keep(key [sha256.Size]byte, token delegated) {
	if _, ok := d.cache[key]; !ok && len(d.cache) >= d.size {
		now := d.now()
		for k, v := range d.cache {
			if !v.expires.After(now) {
				delete(d.cache, k)
			}
		}
		for len(d.cache) >= d.size {
			var (
				soonest [sha256.Size]byte
				at      time.Time
				first   = true
			)
			for k, v := range d.cache {
				if first || v.expires.Before(at) {
					soonest, at, first = k, v.expires, false
				}
			}
			delete(d.cache, soonest)
		}
	}
	d.cache[key] = token
}

// cached is how many tokens are kept: for tests.
func (d *Delegator) cached() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.cache)
}

// Ready reports whether the actor token can be read: what readiness means
// for a delegating outbound side, which holds no token of its own until a
// caller arrives.
func (d *Delegator) Ready() error {
	_, err := readToken(d.file)
	return err
}

// delegationRefusal is how a failed delegated exchange is answered to the
// stock server, and named in the audit line: a refusal by the issuer is the
// caller's (403), anything else is the backend's identity being unavailable
// (502).
func delegationRefusal(err error) (status int, reason, message string) {
	var refused *tokens.RefusalError
	switch {
	case errors.Is(err, errNoCaller):
		return http.StatusForbidden, "no_caller", "no caller to act for at the backend"
	case errors.As(err, &refused) && refused.Status >= 400 && refused.Status < 500:
		code := refused.Code
		if code == "" {
			code = "refused"
		}
		return http.StatusForbidden, code, "the issuer refused to let this server act for the caller at the backend"
	default:
		return http.StatusBadGateway, "exchange_failed", "no identity for the backend"
	}
}

// delegate is the outbound handler in [ModeDelegate].
func (o *Outbound) delegate(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	who, ok := o.handles.lookup(r.Header.Get(o.header))
	// Never forwarded, whatever happens next: the handle is the proxy's own.
	r.Header.Del(o.header)

	var (
		token  string
		cached bool
		err    error
	)
	if ok {
		token, cached, err = o.delegator.Token(r.Context(), who.token)
	} else {
		err = errNoCaller
	}

	attrs := []slog.Attr{
		slog.String("http_method", clean(r.Method)),
		slog.String("path", bounded(r.URL.Path, maxPathLen)),
	}
	if who.subject != "" {
		attrs = append(attrs, slog.String("sub", clean(who.subject)))
	}
	if err != nil {
		status, reason, message := delegationRefusal(err)
		attrs = append(attrs, slog.Int("status", status), slog.String("outcome", "refused"),
			slog.String("reason", clean(reason)), slog.String("error", bounded(err.Error(), maxPathLen)),
			slog.Float64("duration_ms", since(start)))
		o.log.LogAttrs(r.Context(), slog.LevelWarn, "mcp_delegation", attrs...)
		http.Error(w, message, status)
		return
	}
	attrs = append(attrs, slog.String("outcome", "acted"), slog.Bool("cached", cached),
		slog.Float64("duration_ms", since(start)))
	o.log.LogAttrs(r.Context(), slog.LevelInfo, "mcp_delegation", attrs...)

	r.Header.Set("Authorization", "Bearer "+token)
	o.proxy.ServeHTTP(w, r)
}

// effectiveMargin never asks for a refresh earlier than half a token's own
// life, or a short-lived token would be exchanged on every call.
func effectiveMargin(margin time.Duration, issued, expires time.Time) time.Duration {
	life := expires.Sub(issued)
	if life > 0 && margin > life/2 {
		return life / 2
	}
	return margin
}

// readToken reads a projected token afresh: the kubelet rotates it under the
// pod.
func readToken(file string) (string, error) {
	subject, err := readFile(file)
	if err != nil {
		return "", fmt.Errorf("read the ServiceAccount token: %w", err)
	}
	if subject == "" {
		return "", errors.New("the ServiceAccount token file is empty")
	}
	return subject, nil
}
