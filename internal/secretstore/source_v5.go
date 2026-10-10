package secretstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/storage/state"
)

// SourceV5 is the [secrets.Source] of an installation on layout v5 (ADR 0072):
// the names the code asks for by a fixed spelling are read from the module-first
// address they moved to, and any other name is delivered by Next.
//
//	clients/<id>/secret                      internal/oidc/clients/<id>
//	providers/google/<p>/client-id           internal/oidc/signin/<p>/client-id
//	providers/google/<p>/client-secret       internal/oidc/signin/<p>/client-secret
//
// The state secret and the recovery password are not here: a document names
// them freely, and on layout v5 their addresses are fixed
// (internal/oidc/state-secret, internal/oidc/recovery-password), so their
// callers read [OIDCInternal] directly.
type SourceV5 struct {
	stores *StoresV5
	next   secrets.Source
}

var _ secrets.Source = (*SourceV5)(nil)

// NewSourceV5 returns the source over stores. next, which may be nil, delivers
// the names that did not move.
func NewSourceV5(stores *StoresV5, next secrets.Source) *SourceV5 {
	return &SourceV5{stores: stores, next: next}
}

// Get implements [secrets.Source].
func (s *SourceV5) Get(ctx context.Context, name string) (string, error) {
	v, ok := s.value(name)
	if !ok {
		if s.next == nil {
			return "", fmt.Errorf("%w: %s", secrets.ErrNotFound, name)
		}
		return s.next.Get(ctx, name)
	}
	raw, _, err := v.Get(ctx)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", fmt.Errorf("%w: %s", secrets.ErrNotFound, s.Describe(name))
		}
		return "", fmt.Errorf("read %s: %w", s.Describe(name), err)
	}
	if len(raw) == 0 {
		return "", fmt.Errorf("%w: %s (empty)", secrets.ErrNotFound, s.Describe(name))
	}
	return string(raw), nil
}

// Describe implements [secrets.Source]: the address a name is read from.
func (s *SourceV5) Describe(name string) string {
	if _, ok := s.value(name); ok {
		return "ssm " + s.stores.OIDC().Prefix() + "/" + s.rel(name)
	}
	if s.next != nil {
		return s.next.Describe(name)
	}
	return name
}

// rel is the address of a moved name below internal/oidc.
func (s *SourceV5) rel(name string) string {
	switch parts := strings.Split(name, "/"); {
	case len(parts) == 3 && parts[0] == "clients" && parts[2] == "secret":
		return "clients/" + segment(parts[1])
	case len(parts) == 4 && parts[0] == "providers" && parts[1] == "google":
		return "signin/" + segment(parts[2]) + "/" + parts[3]
	}
	return ""
}

// value is the Value a moved name is read from.
func (s *SourceV5) value(name string) (state.Value[[]byte], bool) {
	if secrets.Check(name) != nil {
		return state.Value[[]byte]{}, false
	}
	parts := strings.Split(name, "/")
	oidc := s.stores.OIDC()
	switch {
	case len(parts) == 3 && parts[0] == "clients" && parts[2] == "secret":
		return oidc.Client(parts[1]), true
	case len(parts) == 4 && parts[0] == "providers" && parts[1] == "google" && parts[3] == "client-id":
		return oidc.SignInClientID(parts[2]), true
	case len(parts) == 4 && parts[0] == "providers" && parts[1] == "google" && parts[3] == "client-secret":
		return oidc.SignInClientSecret(parts[2]), true
	}
	return state.Value[[]byte]{}, false
}
