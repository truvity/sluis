package console

import (
	"context"
	"strings"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
)

// BeginGitHubAppConnect starts creating any App, or finishing installing one
// created before.
//
// It hands the work to the same code the per-kind calls run, so the signed
// state, the cookie it is pinned to and the callback that finishes the flow are
// the ones that kind of App has always used: a browser part-way through a flow
// is unaffected by which call started it.
func (c *Service) BeginGitHubAppConnect(ctx context.Context, r consolewire.BeginAppConnectRequest) (consolewire.Begun, error) {
	if _, err := requireAnywhere(ctx, access.RoleOperator); err != nil {
		return consolewire.Begun{}, err
	}
	spec, err := c.githubAppSpec(ctx, r.ID)
	if err != nil {
		return consolewire.Begun{}, err
	}
	id, err := c.requireApp(ctx, access.RoleOperator, &spec)
	if err != nil {
		return consolewire.Begun{}, err
	}
	var begun githubBegin
	switch spec.purpose {
	case appLink:
		begun, err = c.beginLinkAppConnect(ctx, strings.TrimSpace(r.Owner))
	case appController:
		begun, err = c.beginOrganisationConnect(ctx, id, spec.org, r.OwnerDirectory)
	case appRunners:
		begun, err = c.beginRunnerAppConnect(ctx, spec.org, spec.tier)
	default:
		begun, err = c.beginCatalogueAppConnect(ctx, spec.id)
	}
	if err != nil {
		return consolewire.Begun{}, err
	}
	return begun.wire(), nil
}

// DisconnectGitHubApp uninstalls any App, then forgets it.
func (c *Service) DisconnectGitHubApp(
	ctx context.Context, req *directoryrosterv1.DisconnectGitHubAppRequest,
) (*directoryrosterv1.DisconnectGitHubAppResponse, error) {
	if _, err := requireAnywhere(ctx, access.RoleOperator); err != nil {
		return nil, err
	}
	spec, err := c.githubAppSpec(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	if _, err = c.requireApp(ctx, access.RoleOperator, &spec); err != nil {
		return nil, err
	}
	var gone githubDisconnect
	switch spec.purpose {
	case appLink:
		gone, err = c.disconnectLinkApp(ctx)
	case appController:
		gone, err = c.disconnectOrganisation(ctx, spec.org)
	case appRunners:
		gone, err = c.disconnectRunnerApp(ctx, spec.org, spec.tier)
	default:
		gone, err = c.disconnectCatalogueApp(ctx, spec.id)
	}
	if err != nil {
		return nil, err
	}
	return &directoryrosterv1.DisconnectGitHubAppResponse{
		Uninstalled:    gone.uninstalled,
		Detail:         gone.detail,
		AppSettingsUrl: gone.settingsURL,
		Invalidated:    int32(gone.invalidated), //nolint:gosec // a count of people
	}, nil
}
