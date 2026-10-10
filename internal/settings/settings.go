// Package settings keeps the two things an operator can change from the
// console: the OAuth client used for admin consent and for sign-in, and
// the memberships added there.
//
// Everything else an operator sees — the internal groups, what they add,
// the lifetimes, the clients, the intervals — is deployment
// configuration, shown read-only.
package settings

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/truvity/sluis/internal/lazy"
)

// ErrDeclared is returned when the deployment owns what is being changed.
var ErrDeclared = errors.New("settings: declared by the deployment")

// OAuthClient is the client an installation registered once with its
// directory backend: it drives both admin consent and operator sign-in.
type OAuthClient struct {
	ID     string
	Secret string
	// Declared marks a client the chart named, which the console shows
	// but cannot change.
	Declared bool
}

// Configured reports whether both halves are present.
func (c OAuthClient) Configured() bool { return c.ID != "" && c.Secret != "" }

// Store keeps what the deployment configured for the console to read.
//
// It used to keep what the console WROTE — the OAuth client somebody
// pasted in, and the memberships layer. Neither is written any more:
// the client is a Secret, delivered the way every other
// credential in the estate is, and who is in which internal group is the
// policy, rendered from the installation's own access model and reviewed
// in git.
type Store interface {
	// OAuthClient returns the client, which may be unconfigured.
	OAuthClient(ctx context.Context) (OAuthClient, error)
}

// Memory is a Store in process memory: the prototype's, and the fake
// behind tests of the one that persists.
type Memory struct {
	mu     sync.RWMutex
	client OAuthClient
}

var _ Store = (*Memory)(nil)

// NewMemory returns a store, optionally seeded with a declared client.
func NewMemory(declared OAuthClient) *Memory {
	return &Memory{client: declared}
}

// OAuthClient implements [Store].
func (m *Memory) OAuthClient(_ context.Context) (OAuthClient, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.client, nil
}

// Lazy is a Store over a client read from the secrets the deployment names:
// on the first request that asks for it, and again once ttl has passed, so
// that opening the store reads no secret and a rotated secret takes effect
// without a restart. A read that fails is an error to that request and is tried
// again by the next.
type Lazy struct {
	client *lazy.Value[OAuthClient]
}

var _ Store = (*Lazy)(nil)

// NewLazy returns a store that calls read when the client is first wanted.
func NewLazy(ttl time.Duration, read func(context.Context) (OAuthClient, error)) *Lazy {
	return &Lazy{client: lazy.New(ttl, read)}
}

// OAuthClient implements [Store].
func (l *Lazy) OAuthClient(ctx context.Context) (OAuthClient, error) {
	return l.client.Get(ctx)
}
