//nolint:lll // messages and fixtures are prose and one-line tables
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	cfrpc "github.com/truvity/sluis/internal/cloudflare/rpc"
	"github.com/truvity/sluis/storage/logattr"
)

// CloudflareModule is the Cloudflare module as the console uses it: the
// minter's own methods when this process holds one ([cfrpc.Local]), or the
// module in another process ([cfrpc.Client]) when `cloudflare.remote` is set.
// Either way the console reaches Cloudflare through the module and holds no
// minter credential reference. An interface so the handlers are tested with a
// fake module.
//
// On-demand minting shares its code path with the token exchange
// (internal/issuer): both end at the minter's MintFor, which checks the grant
// and the lifetime and audits. What differs is only how the caller is
// established (a console session here, a verified proof there), which is why
// each builds its own [cfrpc.Caller].
type CloudflareModule = cfrpc.Admin

// UseCloudflare connects the Cloudflare page to the module, with the same
// timing as [ConsoleServer.UseAuditQuery]: the module is reached after the
// console is made. Without it the console has no Cloudflare page.
func (s *ConsoleServer) UseCloudflare(m CloudflareModule) {
	s.console.deps.Cloudflare = m
}

// cloudflareCallTimeout bounds one call to the module: the page is one call, and
// a cold start of the module must not hold the console.
const cloudflareCallTimeout = 30 * time.Second

func (c *Console) cloudflare() (CloudflareModule, error) {
	if c.deps.Cloudflare == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this deployment has no cloudflare section"))
	}
	return c.deps.Cloudflare, nil
}

// callerOf is who the signed-in person is to the minter: their address and the
// groups the policy put them in, which the grants are read against.
func callerOf(id access.Identity) cfrpc.Caller {
	return cfrpc.Caller{Actor: identityActor(id), Groups: id.Groups}
}

// ListCloudflare implements the administrator's contract.
func (c *Console) ListCloudflare(
	ctx context.Context, _ *connect.Request[directoryrosterv1.ListCloudflareRequest],
) (*connect.Response[directoryrosterv1.ListCloudflareResponse], error) {
	id, err := requireRole(ctx, access.RoleViewer)
	if err != nil {
		return nil, err
	}
	out := &directoryrosterv1.ListCloudflareResponse{CanOperate: id.Can(access.RoleOperator)}
	if c.deps.Cloudflare == nil {
		return connect.NewResponse(out), nil
	}
	ctx, cancel := context.WithTimeout(ctx, cloudflareCallTimeout)
	defer cancel()
	st, err := c.deps.Cloudflare.Status(ctx)
	if err != nil {
		return nil, cloudflareError(err)
	}
	out.Available = true
	for _, a := range st.Accounts {
		out.Accounts = append(out.Accounts, &directoryrosterv1.CloudflareAccount{Name: a.Name, IdLast4: a.IDLast4})
	}
	for i := range st.Presets {
		out.Presets = append(out.Presets, presetProto(st.Presets[i]))
	}
	return connect.NewResponse(out), nil
}

// presetProto is the module's view of a preset as the page's message.
func presetProto(p cfrpc.PresetView) *directoryrosterv1.CloudflarePreset {
	out := &directoryrosterv1.CloudflarePreset{
		Name: p.Name, Description: p.Description, Account: p.Account, Endpoint: p.Endpoint,
		Store: storeOf(p.Store), LifetimeSeconds: int64(p.Lifetime / time.Second), RotationSeconds: int64(p.Rotation / time.Second), RotationTickSeconds: int64(p.Tick / time.Second),
		Prototype: &directoryrosterv1.CloudflarePrototype{Id: p.Prototype.ID, Status: p.Prototype.Status, Detail: p.Prototype.Detail},
		LiveError: p.LiveError,
		Stored: &directoryrosterv1.CloudflareStored{
			Present: p.Stored.Present, TokenId: p.Stored.TokenID, MintedAt: stamp(p.Stored.MintedAt), ExpiresOn: stamp(p.Stored.ExpiresOn),
			Error: p.Stored.Error,
		},
	}
	for _, t := range p.Live {
		out.Live = append(out.Live, &directoryrosterv1.CloudflareLiveToken{
			Id: t.ID, Caller: t.Caller, MintedAt: stamp(t.MintedAt), ExpiresOn: stamp(t.ExpiresOn), Stored: t.Stored,
		})
	}
	return out
}

// RotateCloudflarePreset implements the operator contract.
func (c *Console) RotateCloudflarePreset(
	ctx context.Context, req *connect.Request[directoryrosterv1.RotateCloudflarePresetRequest],
) (*connect.Response[directoryrosterv1.RotateCloudflarePresetResponse], error) {
	id, err := requireRole(ctx, access.RoleOperator)
	if err != nil {
		return nil, err
	}
	sts, err := c.cloudflare()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, cloudflareCallTimeout)
	defer cancel()
	minted, err := sts.Rotate(ctx, req.Msg.GetPreset(), identityActor(id))
	if err != nil {
		return nil, cloudflareError(err)
	}
	c.log().InfoContext(ctx, "a Cloudflare preset was rotated from the console", slog.String("preset", logattr.Safe(req.Msg.GetPreset())), slog.String("by", logattr.Safe(id.Who())))
	return connect.NewResponse(&directoryrosterv1.RotateCloudflarePresetResponse{
		TokenId: minted.TokenID, ExpiresOn: stamp(minted.ExpiresOn),
	}), nil
}

// RevokeCloudflareToken implements the operator contract.
func (c *Console) RevokeCloudflareToken(
	ctx context.Context, req *connect.Request[directoryrosterv1.RevokeCloudflareTokenRequest],
) (*connect.Response[directoryrosterv1.RevokeCloudflareTokenResponse], error) {
	id, err := requireRole(ctx, access.RoleOperator)
	if err != nil {
		return nil, err
	}
	sts, err := c.cloudflare()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, cloudflareCallTimeout)
	defer cancel()
	res, err := sts.Revoke(ctx, req.Msg.GetPreset(), req.Msg.GetTokenId(), identityActor(id))
	if err != nil {
		return nil, cloudflareError(err)
	}
	return connect.NewResponse(&directoryrosterv1.RevokeCloudflareTokenResponse{Replaced: res.Rotated}), nil
}

// ListMyCloudflarePresets implements the person contract.
func (c *Console) ListMyCloudflarePresets(
	ctx context.Context, _ *connect.Request[directoryrosterv1.ListMyCloudflarePresetsRequest],
) (*connect.Response[directoryrosterv1.ListMyCloudflarePresetsResponse], error) {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in first"))
	}
	out := &directoryrosterv1.ListMyCloudflarePresetsResponse{}
	if c.deps.Cloudflare == nil {
		return connect.NewResponse(out), nil
	}
	out.Available = true
	for _, p := range c.deps.Cloudflare.Granted(callerOf(id)) {
		out.Presets = append(out.Presets, &directoryrosterv1.MyCloudflarePreset{
			Name: p.Name, Description: p.Description, Endpoint: p.Endpoint, LifetimeSeconds: int64(p.Lifetime / time.Second),
		})
	}
	return connect.NewResponse(out), nil
}

// GetCloudflareCredential implements the person contract. The grant is the
// minter's to check, and it records the refusal.
func (c *Console) GetCloudflareCredential(
	ctx context.Context, req *connect.Request[directoryrosterv1.GetCloudflareCredentialRequest],
) (*connect.Response[directoryrosterv1.GetCloudflareCredentialResponse], error) {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in first"))
	}
	sts, err := c.cloudflare()
	if err != nil {
		return nil, err
	}
	if req.Msg.GetLifetimeSeconds() < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("lifetime_seconds is negative"))
	}
	lifetime := time.Duration(req.Msg.GetLifetimeSeconds()) * time.Second
	ctx, cancel := context.WithTimeout(ctx, cloudflareCallTimeout)
	defer cancel()
	minted, err := sts.Credential(ctx, req.Msg.GetPreset(), callerOf(id), lifetime)
	if err != nil {
		return nil, cloudflareError(err)
	}
	out := &directoryrosterv1.GetCloudflareCredentialResponse{
		Preset: minted.Preset, TokenId: minted.TokenID, ExpiresOn: stamp(minted.ExpiresOn),
	}
	if minted.R2 {
		out.AccessKeyId, out.SecretAccessKey, out.Endpoint = minted.AccessKeyID, minted.SecretAccessKey, minted.Endpoint
	} else {
		out.Token = minted.Token
	}
	resp := connect.NewResponse(out)
	resp.Header().Set("Cache-Control", "no-store")
	return resp, nil
}

// cloudflareError maps the minter's errors to RPC codes. Nothing in them is a
// value; a Cloudflare or store failure is reported by kind only.
func cloudflareError(err error) error {
	var re *cfrpc.RefusedError
	switch {
	case errors.Is(err, cfrpc.ErrUnknownPreset):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, cfrpc.ErrNotGranted):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, cfrpc.ErrLifetime):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, cfrpc.ErrMaintenance):
		return connect.NewError(connect.CodeUnavailable, err)
	case errors.Is(err, cfrpc.ErrNotOurs):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, cfrpc.ErrOnDemandOnly):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, cfrpc.ErrContended):
		return connect.NewError(connect.CodeAborted, err)
	case errors.As(err, &re):
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the prototype is refused: %s", re.Detail))
	case errors.Is(err, cfrpc.ErrNoMinter):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("the call to Cloudflare or the secrets store did not complete; see the service log"))
	}
}

// storeOf is a preset's store as the page's message spells it: the default is
// written out.
func storeOf(store string) string {
	if store == "" {
		return "external"
	}
	return store
}
