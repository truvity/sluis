package console

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"

	"connectrpc.com/connect/v2"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubapp/catalogue"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/githubroster/runnerapp"
	"github.com/truvity/sluis/internal/githubroster/status"
)

// BeginGitHubCatalogueAppConnect starts creating a catalogue App, or
// finishing installing one created before. The URL carries the state
// placeholder the issuer signs.
func (c *Service) BeginGitHubCatalogueAppConnect(ctx context.Context, id string) (consolewire.Begun, error) {
	id = strings.TrimSpace(id)
	if _, err := c.requireCatalogueApp(ctx, access.RoleOperator, id); err != nil {
		return consolewire.Begun{}, err
	}
	begun, err := c.beginCatalogueAppConnect(ctx, id)
	if err != nil {
		return consolewire.Begun{}, err
	}
	return begun.wire(), nil
}

// beginCatalogueAppConnect is the work of it, which
// [Console.BeginGitHubAppConnect] does too.
func (c *Service) beginCatalogueAppConnect(ctx context.Context, id string) (githubBegin, error) {
	store, err := c.catalogueStore()
	if err != nil {
		return githubBegin{}, err
	}
	entry, declared := c.deps.Catalogue.Get(id)
	if !declared {
		return githubBegin{}, connect.Errorf(connect.CodeInvalidArgument, "the catalogue declares no App %q", id)
	}
	existing, _, created, err := store.Get(ctx, id)
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	if created && existing.Installed() {
		return githubBegin{}, connect.Errorf(connect.CodeFailedPrecondition,
			"%s is already created and installed as %s: disconnect it first, or a second App would sit beside the first", id, existing.AppSlug)
	}

	state := consolewire.StatePlaceholder
	out := githubBegin{state: state, bind: consolewire.BindCatalogue + id}
	if created {
		out.url = githubapp.InstallURL(existing.AppSlug, state)
		return out, nil
	}
	root := c.githubRoot(ctx)
	manifest, err := json.Marshal(githubapp.ManifestFor(entry, c.publicURL(ctx), root+consolewire.CatalogueCallbackPath, root+consolewire.CatalogueSetupPath, nil))
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	out.url, out.manifest = githubapp.CreateURL(entry.Org, state), string(manifest)
	return out, nil
}

// DisconnectGitHubCatalogueApp uninstalls a catalogue App, then forgets
// it. A failed uninstall still forgets, and says what is left to do by
// hand. The App stays on GitHub: the API cannot delete one.
func (c *Service) DisconnectGitHubCatalogueApp(
	ctx context.Context, req *directoryrosterv1.DisconnectGitHubCatalogueAppRequest,
) (*directoryrosterv1.DisconnectGitHubCatalogueAppResponse, error) {
	id := strings.TrimSpace(req.GetId())
	if _, err := c.requireCatalogueApp(ctx, access.RoleOperator, id); err != nil {
		return nil, err
	}
	gone, err := c.disconnectCatalogueApp(ctx, id)
	if err != nil {
		return nil, err
	}
	return &directoryrosterv1.DisconnectGitHubCatalogueAppResponse{
		Uninstalled: gone.uninstalled, Detail: gone.detail, AppSettingsUrl: gone.settingsURL,
	}, nil
}

// disconnectCatalogueApp is the work of it, which
// [Console.DisconnectGitHubApp] does too.
func (c *Service) disconnectCatalogueApp(ctx context.Context, id string) (githubDisconnect, error) {
	store, err := c.catalogueStore()
	if err != nil {
		return githubDisconnect{}, err
	}
	if !catalogue.ValidID(id) {
		return githubDisconnect{}, connect.Errorf(connect.CodeInvalidArgument, "%q is not a catalogue id", id)
	}
	record, key, created, err := store.Get(ctx, id)
	if err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	if !created {
		return githubDisconnect{}, connect.Errorf(connect.CodeNotFound, "no App %s has been created", id)
	}

	out := c.uninstall(ctx, record.AppID, record.InstallationID, key)
	out.settingsURL = appSettingsURL(record.Org, record.AppSlug)
	if err = store.Delete(ctx, id); err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	c.record(ctx, audit.CatalogueAppDisconnected(actorOf(ctx), record.Org,
		audit.App{Name: id, ID: record.AppID, Slug: record.AppSlug}, out.uninstalled, out.detail))
	return out, nil
}

// runnerTier checks a tier against the ones the deployment declares.
func (c *Service) runnerTier(tier string) error {
	switch {
	case c.deps.RunnerApps == nil || len(c.deps.RunnerTiers) == 0:
		return connect.NewError(connect.CodeFailedPrecondition, "this deployment declares no runner tiers")
	case !slices.Contains(c.deps.RunnerTiers, tier):
		return connect.Errorf(connect.CodeInvalidArgument,
			"%q is not a runner tier here; the deployment declares %s", tier, strings.Join(c.deps.RunnerTiers, ", "))
	}
	return nil
}

// BeginGitHubRunnerAppConnect starts creating a runner App, or finishing
// installing one created before.
//
// Only in an organisation the policy binds, for the same reason as an
// organisation's App: a typo in the login would otherwise meet GitHub's
// 404 after the operator has left this page.
func (c *Service) BeginGitHubRunnerAppConnect(ctx context.Context, org, tier string) (consolewire.Begun, error) {
	org = strings.TrimSpace(org)
	if _, err := c.requireOrg(ctx, access.RoleOperator, org); err != nil {
		return consolewire.Begun{}, err
	}
	begun, err := c.beginRunnerAppConnect(ctx, org, strings.TrimSpace(tier))
	if err != nil {
		return consolewire.Begun{}, err
	}
	return begun.wire(), nil
}

// beginRunnerAppConnect is the work of it, which
// [Console.BeginGitHubAppConnect] does too.
func (c *Service) beginRunnerAppConnect(ctx context.Context, org, tier string) (githubBegin, error) {
	if !status.ValidOrg(org) {
		return githubBegin{}, connect.Errorf(connect.CodeInvalidArgument, "%q is not an organisation login", org)
	}
	if err := c.runnerTier(tier); err != nil {
		return githubBegin{}, err
	}
	if _, bound := c.bindings()[org]; !bound {
		return githubBegin{}, connect.Errorf(connect.CodeFailedPrecondition,
			"the policy binds no organisation %s: bind its teams first", org)
	}
	existing, created, err := c.runnerApp(ctx, tier, org)
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	if created && existing.Installed() {
		return githubBegin{}, connect.Errorf(connect.CodeFailedPrecondition,
			"%s already has a %s runner App, %s: disconnect it first, or a second App would sit beside the first", org, tier, existing.AppSlug)
	}

	state := consolewire.StatePlaceholder
	out := githubBegin{state: state, bind: consolewire.BindRunner + tier + ":" + org}
	if created {
		out.url = githubapp.InstallURL(existing.AppSlug, state)
		return out, nil
	}
	root := c.githubRoot(ctx)
	manifest, err := json.Marshal(githubapp.NewRunnerManifest(org, tier, c.publicURL(ctx), root+consolewire.RunnerCallbackPath, root+consolewire.RunnerSetupPath))
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	out.url, out.manifest = githubapp.CreateURL(org, state), string(manifest)
	return out, nil
}

// DisconnectGitHubRunnerApp uninstalls a runner App, then forgets it. A
// failed uninstall still forgets, and says what is left to do by hand.
func (c *Service) DisconnectGitHubRunnerApp(
	ctx context.Context, req *directoryrosterv1.DisconnectGitHubRunnerAppRequest,
) (*directoryrosterv1.DisconnectGitHubRunnerAppResponse, error) {
	org := strings.TrimSpace(req.GetOrg())
	if _, err := c.requireOrg(ctx, access.RoleOperator, org); err != nil {
		return nil, err
	}
	gone, err := c.disconnectRunnerApp(ctx, org, strings.TrimSpace(req.GetTier()))
	if err != nil {
		return nil, err
	}
	return &directoryrosterv1.DisconnectGitHubRunnerAppResponse{
		Uninstalled: gone.uninstalled, Detail: gone.detail, AppSettingsUrl: gone.settingsURL,
	}, nil
}

// disconnectRunnerApp is the work of it, which
// [Console.DisconnectGitHubApp] does too.
func (c *Service) disconnectRunnerApp(ctx context.Context, org, tier string) (githubDisconnect, error) {
	switch {
	case !status.ValidOrg(org) || !runnerapp.ValidTier(tier):
		return githubDisconnect{}, connect.Errorf(connect.CodeInvalidArgument, "%q/%q is not an organisation and a tier", org, tier)
	case c.deps.RunnerApps == nil:
		return githubDisconnect{}, connect.NewError(connect.CodeFailedPrecondition, "this deployment keeps no runner Apps")
	}
	record, created, err := c.runnerApp(ctx, tier, org)
	if err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	key, hasKey, err := c.deps.RunnerApps.PrivateKey(ctx, tier, org)
	if err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	if !created && !hasKey {
		return githubDisconnect{}, connect.Errorf(connect.CodeNotFound, "%s has no %s runner App", org, tier)
	}

	out := c.uninstall(ctx, record.AppID, record.InstallationID, key)
	out.settingsURL = appSettingsURL(org, record.AppSlug)
	if err = c.deps.RunnerApps.Delete(ctx, tier, org); err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	c.record(ctx, audit.RunnerAppDisconnected(actorOf(ctx), org,
		audit.App{ID: record.AppID, Slug: record.AppSlug, Tier: tier}, out.uninstalled, out.detail))
	return out, nil
}
