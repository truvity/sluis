package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/lazy"
	"github.com/truvity/sluis/internal/secrets"
)

// lazyBackend reads a declared workspace through a service-account key that is
// read when the workspace is first used, and again after [lazy.TTL]: a start
// that attached it reads no secret, and a rotated key is picked up by opening
// the backend again. A key that cannot be read fails that call and is tried
// again by the next.
type lazyBackend struct {
	declared hub.Declared
	key      func(context.Context) (string, error)

	// opener builds the backend from the key; openBackend unless a test says.
	opener func(context.Context, *hub.Declared) (backend.Backend, error)

	mu    sync.Mutex
	inner backend.Backend
	fp    string
	at    time.Time
	now   func() time.Time
}

var _ backend.Backend = (*lazyBackend)(nil)

func newLazyBackend(d hub.Declared, src secrets.Source, name string) *lazyBackend {
	return &lazyBackend{
		declared: d,
		key:      func(ctx context.Context) (string, error) { return src.Get(ctx, name) },
		opener:   openBackend,
		now:      time.Now,
	}
}

// open returns the backend, reading the key when there is none or it is older
// than the time to live. The backend is opened again only when the key changed.
func (l *lazyBackend) open(ctx context.Context) (backend.Backend, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inner != nil && l.now().Sub(l.at) < lazy.TTL {
		return l.inner, nil
	}
	key, err := l.key(ctx)
	if err != nil {
		l.inner = nil
		return nil, fmt.Errorf("declared workspace %q: keySecret: %w", l.declared.Backend+"/"+l.declared.Admin, err)
	}
	if l.inner != nil && key == l.fp {
		l.at = l.now()
		return l.inner, nil
	}
	d := l.declared
	d.Key = []byte(key)
	inner, err := l.opener(ctx, &d)
	if err != nil {
		l.inner = nil
		return nil, fmt.Errorf("declared workspace %q: %w", d.Backend+"/"+d.Admin, err)
	}
	l.inner, l.fp, l.at = inner, key, l.now()
	return inner, nil
}

// Kind implements [backend.Backend] without opening anything.
func (l *lazyBackend) Kind() string { return l.declared.Backend }

// Tenant implements [backend.Backend].
func (l *lazyBackend) Tenant(ctx context.Context) (backend.Tenant, error) {
	b, err := l.open(ctx)
	if err != nil {
		return backend.Tenant{}, err
	}
	return b.Tenant(ctx)
}

// Probe implements [backend.Backend].
func (l *lazyBackend) Probe(ctx context.Context) error {
	b, err := l.open(ctx)
	if err != nil {
		return err
	}
	return b.Probe(ctx)
}

// Accounts implements [backend.Backend].
func (l *lazyBackend) Accounts(ctx context.Context) ([]backend.Account, error) {
	b, err := l.open(ctx)
	if err != nil {
		return nil, err
	}
	return b.Accounts(ctx)
}

// Groups implements [backend.Backend].
func (l *lazyBackend) Groups(ctx context.Context) ([]backend.Group, error) {
	b, err := l.open(ctx)
	if err != nil {
		return nil, err
	}
	return b.Groups(ctx)
}

// Account implements [backend.Backend].
func (l *lazyBackend) Account(ctx context.Context, email string) (backend.Account, bool, error) {
	b, err := l.open(ctx)
	if err != nil {
		return backend.Account{}, false, err
	}
	return b.Account(ctx, email)
}

// GroupsOf implements [backend.Backend].
func (l *lazyBackend) GroupsOf(ctx context.Context, email string) ([]string, error) {
	b, err := l.open(ctx)
	if err != nil {
		return nil, err
	}
	return b.GroupsOf(ctx, email)
}

// Revoke implements [backend.Backend].
func (l *lazyBackend) Revoke(ctx context.Context) error {
	b, err := l.open(ctx)
	if err != nil {
		return err
	}
	return b.Revoke(ctx)
}
