//nolint:lll // messages and fixtures are prose and one-line tables
package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/logsafe"
)

// CloudflareSTS is the minter of Cloudflare credentials, as the console uses
// it (*minter.Minter). It is an interface so the handlers are tested with a
// fake, and so the console never reaches Cloudflare itself.
//
// On-demand minting shares its code path with the token exchange
// (internal/issuer): both end at [minter.Minter.MintFor], which checks the
// grant and the lifetime and audits. What differs is only how the caller is
// established (a console session here, a verified proof there), which is why
// each builds its own [minter.Caller].
type CloudflareSTS interface {
	Accounts() []minter.AccountInfo
	Presets() []minter.PresetInfo
	Granted(c minter.Caller) []minter.PresetInfo
	PrototypeID(preset string) (string, bool)
	CheckPreset(ctx context.Context, preset string) error
	Stored(ctx context.Context, preset string) (minter.StoredInfo, error)
	Live(ctx context.Context, preset string) ([]minter.LiveToken, error)
	RotateNow(ctx context.Context, preset string, actor audit.Actor) (*minter.Minted, error)
	Revoke(ctx context.Context, preset, tokenID string, actor audit.Actor) (minter.RevokeResult, error)
	MintFor(ctx context.Context, preset string, caller minter.Caller, lifetime time.Duration) (*minter.Minted, error)
}

var _ CloudflareSTS = (*minter.Minter)(nil)

// UseCloudflare connects the Cloudflare page to the minter, with the same
// timing as [ConsoleServer.UseAuditQuery]: the minter is made after the
// console. Without it the console has no Cloudflare page.
func (s *ConsoleServer) UseCloudflare(sts CloudflareSTS) {
	s.console.deps.Cloudflare = sts
}

// Prototype statuses, as the page shows them.
const (
	prototypeOK          = "ok"
	prototypeActive      = "active"
	prototypeForbidden   = "forbidden"
	prototypeMissing     = "missing"
	prototypeUnreachable = "unreachable"
)

func (c *Console) cloudflare() (CloudflareSTS, error) {
	if c.deps.Cloudflare == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this deployment has no cloudflare section"))
	}
	return c.deps.Cloudflare, nil
}

// last4 redacts an account id to its last four characters.
func last4(id string) string {
	if len(id) <= 4 {
		return id
	}
	return id[len(id)-4:]
}

func prototypeOf(id string, err error) *directoryrosterv1.CloudflarePrototype {
	out := &directoryrosterv1.CloudflarePrototype{Id: id, Status: prototypeOK}
	if err == nil {
		return out
	}
	if pe, ok := cloudflare.IsPrototypeError(err); ok {
		out.Detail = pe.Detail
		switch pe.Reason {
		case cloudflare.ReasonPrototypeActive:
			out.Status = prototypeActive
		case cloudflare.ReasonPrototypeForbidden:
			out.Status = prototypeForbidden
		default:
			out.Status = prototypeMissing
		}
		return out
	}
	out.Status = prototypeUnreachable
	out.Detail = logsafe.Error(err)
	return out
}

// callerOf is who the signed-in person is to the minter: their address and the
// groups the policy put them in, which the grants are read against.
func callerOf(id access.Identity) minter.Caller {
	return minter.Caller{Actor: identityActor(id), Groups: id.Groups}
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
	sts := c.deps.Cloudflare
	out.Available = true
	for _, a := range sts.Accounts() {
		out.Accounts = append(out.Accounts, &directoryrosterv1.CloudflareAccount{Name: a.Name, IdLast4: last4(a.ID)})
	}
	for _, info := range sts.Presets() {
		out.Presets = append(out.Presets, c.presetView(ctx, sts, info))
	}
	return connect.NewResponse(out), nil
}

// presetView reads one preset's live state. A failure to read one part is that
// part's message, never the whole page's: a prototype that is refused is what
// the page exists to show.
func (c *Console) presetView(ctx context.Context, sts CloudflareSTS, info minter.PresetInfo) *directoryrosterv1.CloudflarePreset {
	protoID, _ := sts.PrototypeID(info.Name)
	out := &directoryrosterv1.CloudflarePreset{
		Name: info.Name, Description: info.Description, Account: info.Account, Endpoint: info.Endpoint,
		LifetimeSeconds: int64(info.Lifetime / time.Second), RotationSeconds: int64(info.Rotation / time.Second),
		Prototype: prototypeOf(protoID, sts.CheckPreset(ctx, info.Name)),
	}
	live, lerr := sts.Live(ctx, info.Name)
	if lerr != nil {
		out.LiveError = logsafe.Error(lerr)
	}
	stored, serr := sts.Stored(ctx, info.Name)
	switch {
	case serr != nil:
		out.Stored = &directoryrosterv1.CloudflareStored{Error: logsafe.Error(serr)}
	case stored.Present:
		out.Stored = &directoryrosterv1.CloudflareStored{
			Present: true, TokenId: stored.AccessKeyID, MintedAt: stamp(stored.MintedAt), ExpiresOn: stamp(stored.ExpiresOn),
		}
	default:
		out.Stored = &directoryrosterv1.CloudflareStored{}
	}
	for _, t := range live {
		if t.Stored {
			// The stored token is the live stored-named one that expires with the
			// stored document; the others of that name are older ones awaiting
			// their sweep, and are listed as revocable tokens.
			if out.Stored.Present && out.Stored.TokenId == "" && out.Stored.ExpiresOn != nil && t.ExpiresOn.UTC().Truncate(time.Second).Equal(out.Stored.ExpiresOn.AsTime()) {
				out.Stored.TokenId = t.ID
				continue
			}
		}
		out.Live = append(out.Live, &directoryrosterv1.CloudflareLiveToken{
			Id: t.ID, Caller: t.Caller, MintedAt: stamp(t.MintedAt), ExpiresOn: stamp(t.ExpiresOn), Stored: t.Stored,
		})
	}
	if out.Stored.TokenId != "" {
		// An R2 preset knows its stored token by the access key id; do not list it twice.
		kept := out.Live[:0]
		for _, t := range out.Live {
			if t.Id != out.Stored.TokenId {
				kept = append(kept, t)
			}
		}
		out.Live = kept
	}
	sort.SliceStable(out.Live, func(i, j int) bool { return out.Live[i].ExpiresOn.AsTime().After(out.Live[j].ExpiresOn.AsTime()) })
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
	minted, err := sts.RotateNow(ctx, req.Msg.GetPreset(), identityActor(id))
	if err != nil {
		return nil, cloudflareError(err)
	}
	c.log().InfoContext(ctx, "a Cloudflare preset was rotated from the console", "preset", logsafe.Value(req.Msg.GetPreset()), "by", logsafe.Value(id.Who()))
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
	minted, err := sts.MintFor(ctx, req.Msg.GetPreset(), callerOf(id), lifetime)
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
	var pe *cloudflare.PrototypeError
	switch {
	case errors.Is(err, minter.ErrUnknownPreset):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, minter.ErrNotGranted):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, minter.ErrLifetime):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, minter.ErrNotOurs):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, minter.ErrContended):
		return connect.NewError(connect.CodeAborted, err)
	case errors.As(err, &pe):
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the prototype is refused: %s", pe.Detail))
	case errors.Is(err, minter.ErrNoMinter):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("the call to Cloudflare or the secrets store did not complete; see the service log"))
	}
}
