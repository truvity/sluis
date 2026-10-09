// Package rpc is the Cloudflare minter as another module sees it
// (docs/decisions/0071 D93 a): the on-demand mint and the grants listing, as
// methods of the `cloudflare` module over internal/modcall. A caller holds a
// [Minting]; the minter itself is one, so a process that has the minter keeps
// calling it directly, and a process that does not holds a [Client] that calls
// the module that does.
//
// The types are the minter's own, so that moving a caller onto this package
// changes no signature. A package that imports this one holds no minter.
package rpc

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/modcall"
)

// Types a caller speaks, and the errors it tells apart.
type (
	// Caller is who asks: the actor for the audit trail and the groups the
	// policy's grants read.
	Caller = minter.Caller
	// Minted is a credential just made. It carries the secret.
	Minted = minter.Minted
	// PresetInfo is what a preset shows to someone allowed to ask for it.
	PresetInfo = minter.PresetInfo
)

var (
	// ErrUnknownPreset is a preset the service document does not declare.
	ErrUnknownPreset = minter.ErrUnknownPreset
	// ErrNotGranted is a caller the policy does not open the preset to.
	ErrNotGranted = minter.ErrNotGranted
	// ErrLifetime is a lifetime outside the preset's.
	ErrLifetime = minter.ErrLifetime
)

// Minting is what the on-demand exchange needs of the minter: the grants
// decide, and the minter audits the mint itself, whether it succeeds or not.
type Minting interface {
	MintFor(ctx context.Context, preset string, caller Caller, lifetime time.Duration) (*Minted, error)
	Granted(c Caller) []PresetInfo
}

var _ Minting = (*minter.Minter)(nil)

// Module is the name of the module and the methods it answers.
const (
	Module        = "cloudflare"
	MethodMint    = "mint"
	MethodGranted = "granted"
)

// The codes of the errors that cross the boundary.
const (
	codeUnknownPreset = "unknown_preset"
	codeNotGranted    = "not_granted"
	codeLifetime      = "lifetime"
)

type mintRequest struct {
	Preset   string        `json:"preset"`
	Caller   Caller        `json:"caller"`
	Lifetime time.Duration `json:"lifetime,omitempty"`
}

type grantedRequest struct {
	Caller Caller `json:"caller"`
}

type grantedResponse struct {
	Presets []PresetInfo `json:"presets"`
}

// Register makes m answer the module's methods on s.
func Register(s *modcall.Server, m Minting) {
	modcall.Handle(s, MethodMint, func(ctx context.Context, r mintRequest) (*Minted, error) {
		minted, err := m.MintFor(ctx, r.Preset, r.Caller, r.Lifetime)
		return minted, codeOf(err)
	})
	modcall.Handle(s, MethodGranted, func(_ context.Context, r grantedRequest) (grantedResponse, error) {
		return grantedResponse{Presets: m.Granted(r.Caller)}, nil
	})
}

// codeOf is an error as the boundary tells it: the three a caller answers
// differently by name and nothing else, since what went wrong at Cloudflare is
// the audit trail's and the log's.
func codeOf(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrUnknownPreset):
		return modcall.Coded(codeUnknownPreset, "")
	case errors.Is(err, ErrNotGranted):
		return modcall.Coded(codeNotGranted, "")
	case errors.Is(err, ErrLifetime):
		return modcall.Coded(codeLifetime, "")
	}
	return err
}

// Client is a [Minting] in another module.
type Client struct {
	c   modcall.Caller
	log *slog.Logger
}

var _ Minting = (*Client)(nil)

// NewClient calls the Cloudflare module through c.
func NewClient(c modcall.Caller, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{c: c, log: log}
}

// MintFor implements [Minting].
func (c *Client) MintFor(ctx context.Context, preset string, caller Caller, lifetime time.Duration) (*Minted, error) {
	out, err := modcall.Do[mintRequest, *Minted](ctx, c.c, Module, MethodMint, mintRequest{Preset: preset, Caller: caller, Lifetime: lifetime})
	if err != nil {
		return nil, errOf(err)
	}
	if out == nil {
		return nil, errors.New("cloudflare: the module answered no credential")
	}
	return out, nil
}

// Granted implements [Minting]. It has no error to return: a module that
// cannot be reached lists nothing, and says so in the log.
func (c *Client) Granted(caller Caller) []PresetInfo {
	ctx := context.Background()
	out, err := modcall.Do[grantedRequest, grantedResponse](ctx, c.c, Module, MethodGranted, grantedRequest{Caller: caller})
	if err != nil {
		c.log.WarnContext(ctx, "the Cloudflare grants could not be listed", slog.Any("error", err))
		return nil
	}
	return out.Presets
}

// errOf is the sentinel a code stands for.
func errOf(err error) error {
	var e *modcall.Error
	if !errors.As(err, &e) {
		return err
	}
	switch e.Code {
	case codeUnknownPreset:
		return ErrUnknownPreset
	case codeNotGranted:
		return ErrNotGranted
	case codeLifetime:
		return ErrLifetime
	}
	return err
}
