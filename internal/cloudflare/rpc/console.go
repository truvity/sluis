package rpc

import (
	"context"
	"errors"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/modcall"
)

// What the console's Cloudflare page asks of the module, and what a person does
// there. It is the half of the console that used to reach the minter inside the
// issuer's process: the page now reads the module's own view of it, so that the
// process that serves the console holds no minter credential reference.

// Types the console speaks.
type (
	// AccountInfo is a declared account: its key and its Cloudflare id.
	AccountInfo = minter.AccountInfo
	// RevokeResult says what a revoke did beyond deleting the token.
	RevokeResult = minter.RevokeResult
)

// RefusedError is a prototype the module will not clone. Its detail names a
// token id and a reason, never a value.
type RefusedError struct{ Detail string }

func (e *RefusedError) Error() string { return "cloudflare: the prototype is refused: " + e.Detail }

// Source is what the module holds that the console's methods read and drive:
// the minter ([*minter.Minter]).
type Source interface {
	Minting
	Accounts() []minter.AccountInfo
	Presets() []minter.PresetInfo
	PrototypeID(preset string) (string, bool)
	CheckPreset(ctx context.Context, preset string) error
	Stored(ctx context.Context, preset string) (minter.StoredInfo, error)
	Live(ctx context.Context, preset string) ([]minter.LiveToken, error)
	RotateNow(ctx context.Context, preset string, actor audit.Actor) (*minter.Minted, error)
	Revoke(ctx context.Context, preset, tokenID string, actor audit.Actor) (minter.RevokeResult, error)
}

var _ Source = (*minter.Minter)(nil)

// Admin is the module as the console sees it: [Client] calls it in another
// process and [Local] in this one.
type Admin interface {
	Minting
	// Status is the whole page: the accounts and, per preset, its prototype,
	// stored credential and live tokens. A part that could not be read is that
	// part's message, never the call's error.
	Status(ctx context.Context) (Status, error)
	Presets(ctx context.Context) ([]PresetInfo, error)
	Accounts(ctx context.Context) ([]AccountInfo, error)
	// Rotate mints and stores a preset's next credential now, as actor.
	Rotate(ctx context.Context, preset string, actor audit.Actor) (*Minted, error)
	// Revoke deletes one of the preset's live tokens, as actor.
	Revoke(ctx context.Context, preset, tokenID string, actor audit.Actor) (RevokeResult, error)
	// Credential mints a credential for the signed-in person, whom the console
	// vouches for; the module checks the grant and audits the refusal.
	Credential(ctx context.Context, preset string, caller Caller, lifetime time.Duration) (*Minted, error)
}

type actorRequest struct {
	Preset  string      `json:"preset"`
	TokenID string      `json:"token_id,omitempty"`
	Actor   audit.Actor `json:"actor"`
}

type rotateResponse struct {
	TokenID   string    `json:"token_id"`
	ExpiresOn time.Time `json:"expires_on"`
}

type revokeResponse struct {
	Rotated bool `json:"rotated"`
}

type presetsResponse struct {
	Presets []PresetInfo `json:"presets"`
}

type accountsResponse struct {
	Accounts []AccountInfo `json:"accounts"`
}

// RegisterConsole makes src answer the console's methods on s, to the caller
// class `console` alone: the issuer, the controllers and every other class are
// refused before a method runs.
func RegisterConsole(s *modcall.Server, src Source) {
	only := modcall.Allow(CallerConsole)
	modcall.Handle(s, MethodStatus, func(ctx context.Context, _ struct{}) (Status, error) {
		st := BuildStatus(ctx, src)
		return st, nil
	}, only)
	modcall.Handle(s, MethodListPresets, func(_ context.Context, _ struct{}) (presetsResponse, error) {
		return presetsResponse{Presets: src.Presets()}, nil
	}, only)
	modcall.Handle(s, MethodAccounts, func(_ context.Context, _ struct{}) (accountsResponse, error) {
		return accountsResponse{Accounts: src.Accounts()}, nil
	}, only)
	modcall.Handle(s, MethodRotate, func(ctx context.Context, r actorRequest) (rotateResponse, error) {
		minted, err := src.RotateNow(ctx, r.Preset, r.Actor)
		if err != nil {
			return rotateResponse{}, codeOf(err)
		}
		return rotateResponse{TokenID: minted.TokenID, ExpiresOn: minted.ExpiresOn}, nil
	}, only)
	modcall.Handle(s, MethodRevoke, func(ctx context.Context, r actorRequest) (revokeResponse, error) {
		res, err := src.Revoke(ctx, r.Preset, r.TokenID, r.Actor)
		return revokeResponse{Rotated: res.Rotated}, codeOf(err)
	}, only)
	modcall.Handle(s, MethodCredential, func(ctx context.Context, r mintRequest) (*Minted, error) {
		minted, err := src.MintFor(ctx, r.Preset, r.Caller, r.Lifetime)
		return minted, codeOf(err)
	}, only)
}

// Local is the module in this process: the same answers as a [Client] gives,
// with the errors in the same shape, and no call.
type Local struct{ src Source }

var _ Admin = (*Local)(nil)

// NewLocal is the [Admin] over a minter this process holds.
func NewLocal(src Source) *Local { return &Local{src: src} }

// local is an error as a [Client] would have returned it.
func local(err error) error {
	if err == nil {
		return nil
	}
	if coded := codeOf(err); coded != err {
		return errOf(coded)
	}
	return err
}

// MintFor implements [Minting].
func (l *Local) MintFor(ctx context.Context, preset string, caller Caller, lifetime time.Duration) (*Minted, error) {
	m, err := l.src.MintFor(ctx, preset, caller, lifetime)
	return m, local(err)
}

// Granted implements [Minting].
func (l *Local) Granted(c Caller) []PresetInfo { return l.src.Granted(c) }

// Status implements [Admin].
func (l *Local) Status(ctx context.Context) (Status, error) { return BuildStatus(ctx, l.src), nil }

// Presets implements [Admin].
func (l *Local) Presets(context.Context) ([]PresetInfo, error) { return l.src.Presets(), nil }

// Accounts implements [Admin].
func (l *Local) Accounts(context.Context) ([]AccountInfo, error) { return l.src.Accounts(), nil }

// Rotate implements [Admin].
func (l *Local) Rotate(ctx context.Context, preset string, actor audit.Actor) (*Minted, error) {
	m, err := l.src.RotateNow(ctx, preset, actor)
	return m, local(err)
}

// Revoke implements [Admin].
func (l *Local) Revoke(ctx context.Context, preset, tokenID string, actor audit.Actor) (RevokeResult, error) {
	r, err := l.src.Revoke(ctx, preset, tokenID, actor)
	return r, local(err)
}

// Credential implements [Admin].
func (l *Local) Credential(ctx context.Context, preset string, caller Caller, lifetime time.Duration) (*Minted, error) {
	return l.MintFor(ctx, preset, caller, lifetime)
}

var _ Admin = (*Client)(nil)

// Status implements [Admin].
func (c *Client) Status(ctx context.Context) (Status, error) {
	out, err := modcall.Do[struct{}, Status](ctx, c.c, Module, MethodStatus, struct{}{})
	return out, errOf(err)
}

// Presets implements [Admin].
func (c *Client) Presets(ctx context.Context) ([]PresetInfo, error) {
	out, err := modcall.Do[struct{}, presetsResponse](ctx, c.c, Module, MethodListPresets, struct{}{})
	return out.Presets, errOf(err)
}

// Accounts implements [Admin].
func (c *Client) Accounts(ctx context.Context) ([]AccountInfo, error) {
	out, err := modcall.Do[struct{}, accountsResponse](ctx, c.c, Module, MethodAccounts, struct{}{})
	return out.Accounts, errOf(err)
}

// Rotate implements [Admin]. Only the new token's id and expiry come back: the
// console shows neither a value nor anything else of the credential.
func (c *Client) Rotate(ctx context.Context, preset string, actor audit.Actor) (*Minted, error) {
	out, err := modcall.Do[actorRequest, rotateResponse](ctx, c.c, Module, MethodRotate, actorRequest{Preset: preset, Actor: actor})
	if err != nil {
		return nil, errOf(err)
	}
	return &Minted{Preset: preset, TokenID: out.TokenID, ExpiresOn: out.ExpiresOn}, nil
}

// Revoke implements [Admin].
func (c *Client) Revoke(ctx context.Context, preset, tokenID string, actor audit.Actor) (RevokeResult, error) {
	out, err := modcall.Do[actorRequest, revokeResponse](ctx, c.c, Module, MethodRevoke, actorRequest{Preset: preset, TokenID: tokenID, Actor: actor})
	return RevokeResult{Rotated: out.Rotated}, errOf(err)
}

// Credential implements [Admin].
func (c *Client) Credential(ctx context.Context, preset string, caller Caller, lifetime time.Duration) (*Minted, error) {
	out, err := modcall.Do[mintRequest, *Minted](ctx, c.c, Module, MethodCredential, mintRequest{Preset: preset, Caller: caller, Lifetime: lifetime})
	if err != nil {
		return nil, errOf(err)
	}
	if out == nil {
		return nil, errors.New("cloudflare: the module answered no credential")
	}
	return out, nil
}
