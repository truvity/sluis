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
	Layout   Layout
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
	if cfg == nil || cfg.Source != "ssm" {
		return nil, ErrNoStore
	}
	layout, err := ParseLayout(cfg.Layout)
	if err != nil {
		return nil, err
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
	return FromStore(base, layout, cfg.KMSKeyID), nil
}

// FromStore splits a store rooted at <root> into the two namespaces. keyAlias,
// when set, is the encryption key both use.
func FromStore(root state.Store, layout Layout, keyAlias string) *Stores {
	var opts []state.Option
	if keyAlias != "" {
		opts = append(opts, state.WithKeyAlias(keyAlias))
	}
	return &Stores{
		Layout:   layout,
		Internal: NewInternal(root.Child("internal", opts...)),
		External: NewExternal(root.Child("external", opts...)),
	}
}
