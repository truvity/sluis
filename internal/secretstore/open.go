package secretstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/storage/state"
)

// Opener opens the backend's store at a prefix. It is the signature of the
// backends' own Open (storage/state/ssm.Open); the caller supplies the one the
// document's source names, so this package does not link every backend.
type Opener func(ctx context.Context, prefix string, opts ...state.Option) (state.Store, error)

// Stores are the two namespaces of one installation on one backend.
type Stores struct {
	Internal Internal
	External External
}

// ErrNoStore is a `secrets` section whose source keeps nothing a store can
// hold: `env` and `file` are read-only deliveries.
var ErrNoStore = errors.New("secretstore: this secrets source is not a store")

// Open builds the installation's Internal and External from a serve document's
// `secrets` section: the ssm source's root, region, endpoint and kmsKeyId (the
// key alias the stores encrypt with). Both namespaces are on the one backend
// open returns, under <root>/internal and <root>/external.
func Open(ctx context.Context, cfg *config.Secrets, open Opener) (*Stores, error) {
	if cfg != nil {
		if err := CheckLayout(cfg.Layout); err != nil {
			return nil, err
		}
	}
	base, err := openBase(ctx, cfg, open)
	if err != nil {
		return nil, err
	}
	return FromStore(base, cfg.KMSKeyID), nil
}

// OpenV5 is [Open] for layout v5 (ADR 0072): the `secrets` section must name
// layout v5, and the result is module first. Opening it makes no request.
func OpenV5(ctx context.Context, cfg *config.Secrets, open Opener) (*StoresV5, error) {
	if cfg != nil && cfg.Layout != LayoutV5 {
		return nil, fmt.Errorf("secrets.layout: %q is not %s", cfg.Layout, LayoutV5)
	}
	base, err := openBase(ctx, cfg, open)
	if err != nil {
		return nil, err
	}
	return FromStoreV5(base, cfg.KMSKeyID), nil
}

// openBase opens the backend at the section's root.
func openBase(ctx context.Context, cfg *config.Secrets, open Opener) (state.Store, error) {
	if cfg == nil || cfg.Source != "ssm" {
		return nil, ErrNoStore
	}
	root := strings.TrimSuffix(cfg.Root, "/")
	if root == "" || !strings.HasPrefix(root, "/") {
		return nil, fmt.Errorf("secrets.root %q: want /sluis/<instance>", cfg.Root)
	}
	var opts []state.Option
	if cfg.Region != "" {
		opts = append(opts, state.WithRegion(cfg.Region))
	}
	if cfg.Endpoint != "" {
		opts = append(opts, state.WithEndpoint(cfg.Endpoint))
	}
	base, err := open(ctx, root, opts...)
	if err != nil {
		return nil, fmt.Errorf("secrets: opening %s: %w", root, err)
	}
	return base, nil
}

// FromStore splits a store rooted at <root> into the two namespaces. keyAlias,
// when set, is the encryption key both use.
func FromStore(root state.Store, keyAlias string) *Stores {
	var opts []state.Option
	if keyAlias != "" {
		opts = append(opts, state.WithKeyAlias(keyAlias))
	}
	return &Stores{
		Internal: NewInternal(root.Child("internal", opts...)),
		External: NewExternal(root.Child("external", opts...)),
	}
}
