package console

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubapp/catalogue"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/githubroster/runnerapp"
	"github.com/truvity/sluis/internal/githubroster/status"
	"github.com/truvity/sluis/storage/logattr"
)

// catalogueFlow checks a catalogue flow's hand-over and names its entry and who
// finishes it.
func (c *Service) catalogueFlow(ctx context.Context, f consolewire.Flow) (catalogue.App, string, error) {
	id, isCatalogue := strings.CutPrefix(f.Bind, consolewire.BindCatalogue)
	if !isCatalogue || !catalogue.ValidID(id) {
		return catalogue.App{}, "", flowProblem(http.StatusBadRequest, "This is not a catalogue App's connect.", "")
	}
	actor, err := c.flowActor(ctx, f)
	if err != nil {
		return catalogue.App{}, "", err
	}
	var entry catalogue.App
	var declared bool
	if c.deps.Catalogue != nil {
		entry, declared = c.deps.Catalogue.Get(id)
	}
	if c.deps.CatalogueApps == nil || !declared {
		return catalogue.App{}, "", flowProblem(http.StatusConflict, fmt.Sprintf("This deployment's catalogue declares no App %s.", id), "")
	}
	return entry, actor, nil
}

// CompleteGitHubCatalogueCreate is where GitHub sends the owner after creating
// a catalogue App, with a one-time code for its key. The key is kept and the
// owner is sent straight on to Install.
func (c *Service) CompleteGitHubCatalogueCreate(ctx context.Context, f consolewire.Flow) (consolewire.Continued, error) {
	entry, actor, err := c.catalogueFlow(ctx, f)
	if err != nil {
		return consolewire.Continued{}, err
	}
	registration, err := githubapp.Convert(ctx, c.githubHTTP(), f.Code)
	if err != nil {
		c.log().WarnContext(ctx, "a catalogue App was created and its key could not be collected",
			slog.String("id", entry.ID), slog.String("org", entry.Org), logattr.SafeError("error", err))
		return consolewire.Continued{}, keyNotCollected(err)
	}
	if !strings.EqualFold(registration.Owner, entry.Org) {
		return consolewire.Continued{}, flowProblem(http.StatusConflict,
			fmt.Sprintf("The App was created under %s, and the catalogue declares it under %s. Delete it on GitHub and create it again.",
				registration.Owner, entry.Org), "")
	}
	record := catalogueapp.Record{
		ID: entry.ID, Org: entry.Org, AppID: registration.ID, AppSlug: registration.Slug, HTMLURL: registration.HTMLURL,
		ConnectedAt: time.Now().UTC(), ConnectedBy: actor,
	}
	// A declared webhook gets a secret this service makes, never the one the
	// conversion returns: it is generated before anything is kept, so a
	// failure to generate leaves the App as it was.
	var hookSecret string
	if entry.Webhook != nil {
		if hookSecret, err = clientcreds.Generate(); err != nil {
			return consolewire.Continued{}, flowProblem(http.StatusConflict,
				"GitHub created the App, and a webhook secret could not be generated. Delete it on GitHub and create it again.", err.Error())
		}
	}
	if err = c.deps.CatalogueApps.Put(ctx, record, registration.PEM); err != nil {
		c.log().ErrorContext(ctx, "a catalogue App was created and could not be kept", slog.String("id", entry.ID), slog.String("org", entry.Org),
			logattr.SafeError("error", err))
		return consolewire.Continued{}, flowProblem(http.StatusConflict,
			"GitHub created the App, and it could not be saved here. Delete it on GitHub and create it again.", err.Error())
	}
	c.log().InfoContext(ctx, "catalogue App created", slog.String("id", entry.ID), slog.String("org", entry.Org), slog.Int64("app", registration.ID),
		slog.String("slug", registration.Slug), logattr.SafeString("by", actor))
	app := audit.App{Name: entry.ID, ID: registration.ID, Slug: registration.Slug}
	c.record(ctx, audit.CatalogueAppCreated(audit.Identified(actor), entry.Org, app))
	if hookSecret != "" {
		if err = c.configureWebhook(ctx, entry, actor, record, registration.PEM, hookSecret, app); err != nil {
			return consolewire.Continued{}, err
		}
	}
	return consolewire.Continued{
		URL: githubapp.InstallURL(registration.Slug, consolewire.StatePlaceholder), Bind: consolewire.BindCatalogue + entry.ID, Actor: actor,
	}, nil
}

// keyNotCollected is the page for a code GitHub would not exchange.
func keyNotCollected(err error) error {
	return flowProblem(http.StatusConflict, "GitHub created the App, and then would not hand over its key.", err.Error(),
		"The page was reloaded: the code GitHub returns can be exchanged once.",
		"More than an hour passed between creating the App and returning here.",
		"This service cannot reach api.github.com: the cluster's egress policy has to allow it.")
}

// configureWebhook gives a new App its webhook: the secret is kept first, so
// a failure after GitHub has it never leaves GitHub holding a secret nobody
// has, then GitHub is told it and the URL in one call. It says why on the
// page and returns the problem when it could not; the App then exists with no
// working webhook, which a rotation finishes.
func (c *Service) configureWebhook(
	ctx context.Context, entry catalogue.App, actor string, record catalogueapp.Record, key, secret string, app audit.App,
) error {
	store, who := c.deps.CatalogueApps, audit.Identified(actor)
	fail := func(step, summary string, err error) error {
		c.log().WarnContext(ctx, "a catalogue App's webhook could not be set", slog.String("id", entry.ID), slog.String("org", entry.Org),
			slog.String("step", step), logattr.SafeError("error", err))
		c.record(ctx, audit.CatalogueAppWebhookChanged(who, entry.Org, app, step, audit.Failed(err.Error())))
		return flowProblem(http.StatusConflict, summary, err.Error(),
			"The App exists and its key is kept. Rotate its webhook secret from the console once it is installed to finish.")
	}
	if err := store.PutWebhookSecret(ctx, entry.ID, secret); err != nil {
		return fail(audit.WebhookStaged, "GitHub created the App, and its webhook secret could not be kept here.", err)
	}
	target := entry.Webhook.Target(secret)
	token, err := githubapp.AppToken(record.AppID, key, time.Now())
	if err != nil {
		return fail(audit.WebhookConfigured, "GitHub created the App, and its webhook could not be set.", err)
	}
	if _, err = githubapp.PatchHookConfig(ctx, c.githubHTTP(), token, githubapp.HookUpdate{URL: target, Secret: secret}); err != nil {
		return fail(audit.WebhookConfigured, "GitHub created the App, and its webhook could not be set.", err)
	}
	record.WebhookURL, record.HookRotatedAt = target, time.Now().UTC()
	if err = store.Put(ctx, record, key); err != nil {
		return fail(audit.WebhookConfigured, "The App's webhook was set, and could not be recorded here.", err)
	}
	c.record(ctx, audit.CatalogueAppWebhookChanged(who, entry.Org, app, audit.WebhookConfigured, audit.Succeeded()))
	return nil
}

// CompleteGitHubCatalogueSetup is where GitHub sends the owner after
// installing a catalogue App. What is kept is where GitHub says the App is
// installed, asked as the App.
func (c *Service) CompleteGitHubCatalogueSetup(ctx context.Context, f consolewire.Flow) (consolewire.Done, error) {
	entry, actor, err := c.catalogueFlow(ctx, f)
	if err != nil {
		return consolewire.Done{}, err
	}
	store := c.deps.CatalogueApps
	record, key, created, err := store.Get(ctx, entry.ID)
	if err != nil || !created || key == "" {
		return consolewire.Done{}, flowProblem(http.StatusConflict,
			fmt.Sprintf("There is no App %s to finish installing. Start Create again.", entry.ID), errString(err))
	}
	token, err := githubapp.AppToken(record.AppID, key, time.Now())
	if err != nil {
		return consolewire.Done{}, flowProblem(http.StatusConflict, "The App's stored key is not usable. Disconnect and create it again.", err.Error())
	}
	installation, err := githubapp.FindInstallation(ctx, c.githubHTTP(), token, record.Org)
	if err != nil {
		return consolewire.Done{}, notInstalled(err, record.Org)
	}
	record.InstallationID = installation
	if err = store.Put(ctx, record, key); err != nil {
		return consolewire.Done{}, flowProblem(http.StatusConflict, "The App is installed and could not be recorded here.", err.Error())
	}
	c.log().InfoContext(ctx, "catalogue App installed", slog.String("id", entry.ID), slog.String("org", record.Org),
		slog.Int64("installation", installation), logattr.SafeString("by", actor))
	c.record(ctx, audit.CatalogueAppInstalled(audit.Identified(actor), record.Org,
		audit.App{Name: entry.ID, ID: record.AppID, Slug: record.AppSlug}, installation))
	return consolewire.Done{}, nil
}

// notInstalled is the page for an installation GitHub cannot name.
func notInstalled(err error, org string) error {
	summary := "GitHub could not say where the App is installed."
	if errors.Is(err, githubapp.ErrNotInstalled) {
		summary = fmt.Sprintf("The App is not installed on %s yet. Finish installing from the console.", org)
	}
	return flowProblem(http.StatusConflict, summary, err.Error())
}

// runnerFlow checks a runner flow's hand-over and names its tier, organisation
// and who finishes it.
func (c *Service) runnerFlow(ctx context.Context, f consolewire.Flow) (tier, org, actor string, err error) {
	rest, isRunner := strings.CutPrefix(f.Bind, consolewire.BindRunner)
	tier, org, split := strings.Cut(rest, ":")
	if !isRunner || !split || !runnerapp.ValidTier(tier) || !status.ValidOrg(org) {
		return "", "", "", flowProblem(http.StatusBadRequest, "This is not a runner App's connect.", "")
	}
	if actor, err = c.flowActor(ctx, f); err != nil {
		return "", "", "", err
	}
	if c.deps.RunnerApps == nil || !slices.Contains(c.deps.RunnerTiers, tier) {
		return "", "", "", flowProblem(http.StatusConflict, fmt.Sprintf("This deployment keeps no %s runner Apps.", tier), "")
	}
	return tier, org, actor, nil
}

// CompleteGitHubRunnerCreate is where GitHub sends the owner after creating a
// runner App, with a one-time code for its key. The key is kept and the owner
// is sent straight on to Install.
func (c *Service) CompleteGitHubRunnerCreate(ctx context.Context, f consolewire.Flow) (consolewire.Continued, error) {
	tier, org, actor, err := c.runnerFlow(ctx, f)
	if err != nil {
		return consolewire.Continued{}, err
	}
	registration, err := githubapp.Convert(ctx, c.githubHTTP(), f.Code)
	if err != nil {
		c.log().WarnContext(ctx, "a runner App was created and its key could not be collected",
			logattr.SafeString("org", org), logattr.SafeString("tier", tier), logattr.SafeError("error", err))
		return consolewire.Continued{}, keyNotCollected(err)
	}
	if !strings.EqualFold(registration.Owner, org) {
		return consolewire.Continued{}, flowProblem(http.StatusConflict,
			fmt.Sprintf("The App was created under %s, not %s.", registration.Owner, org), "")
	}
	record := runnerapp.Record{
		Tier: tier, Org: org, AppID: registration.ID, AppSlug: registration.Slug, HTMLURL: registration.HTMLURL,
		ConnectedAt: time.Now().UTC(), ConnectedBy: actor,
	}
	if err = c.deps.RunnerApps.Put(ctx, record, registration.PEM); err != nil {
		c.log().ErrorContext(ctx, "a runner App was created and could not be kept",
			logattr.SafeString("org", org), logattr.SafeString("tier", tier), logattr.SafeError("error", err))
		return consolewire.Continued{}, flowProblem(http.StatusConflict,
			"GitHub created the App, and it could not be saved here. Delete it on GitHub and create it again.", err.Error())
	}
	c.log().InfoContext(ctx, "runner App created", logattr.SafeString("org", org), logattr.SafeString("tier", tier),
		slog.Int64("app", registration.ID), logattr.SafeString("slug", registration.Slug), logattr.SafeString("by", actor))
	c.record(ctx, audit.RunnerAppCreated(audit.Identified(actor), org,
		audit.App{ID: registration.ID, Slug: registration.Slug, Tier: tier}))
	return consolewire.Continued{
		URL: githubapp.InstallURL(registration.Slug, consolewire.StatePlaceholder), Bind: consolewire.BindRunner + tier + ":" + org, Actor: actor,
	}, nil
}

// CompleteGitHubRunnerSetup is where GitHub sends the owner after installing a
// runner App. What is kept is where GitHub says the App is installed, asked as
// the App.
func (c *Service) CompleteGitHubRunnerSetup(ctx context.Context, f consolewire.Flow) (consolewire.Done, error) {
	tier, org, actor, err := c.runnerFlow(ctx, f)
	if err != nil {
		return consolewire.Done{}, err
	}
	store := c.deps.RunnerApps
	record, created, err := c.runnerApp(ctx, tier, org)
	key, hasKey, keyErr := store.PrivateKey(ctx, tier, org)
	if err = errors.Join(err, keyErr); err != nil || !created || !hasKey {
		return consolewire.Done{}, flowProblem(http.StatusConflict,
			fmt.Sprintf("There is no %s runner App for %s to finish installing. Start Create again.", tier, org), errString(err))
	}
	token, err := githubapp.AppToken(record.AppID, key, time.Now())
	if err != nil {
		return consolewire.Done{}, flowProblem(http.StatusConflict, "The App's stored key is not usable. Disconnect and create it again.", err.Error())
	}
	installation, err := githubapp.FindInstallation(ctx, c.githubHTTP(), token, org)
	if err != nil {
		return consolewire.Done{}, notInstalled(err, org)
	}
	record.InstallationID = installation
	if err = store.Put(ctx, record, key); err != nil {
		return consolewire.Done{}, flowProblem(http.StatusConflict, "The App is installed and could not be recorded here.", err.Error())
	}
	c.log().InfoContext(ctx, "runner App installed", logattr.SafeString("org", org), logattr.SafeString("tier", tier),
		slog.Int64("installation", installation), logattr.SafeString("by", actor))
	c.record(ctx, audit.RunnerAppInstalled(audit.Identified(actor), org,
		audit.App{ID: record.AppID, Slug: record.AppSlug, Tier: tier}, installation))
	return consolewire.Done{}, nil
}
