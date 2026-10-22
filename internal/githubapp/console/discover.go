package console

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/storage/logattr"
)

// findInstallation is what a re-check does for an App that is created and
// recorded as not installed: it asks GitHub, as the App, whether somebody
// installed it directly on GitHub, outside the console's Install flow, and
// records the installation if so. The setup redirect is then never seen, and
// the record would otherwise stay at "created, not installed" for good.
//
// It is an action the operator asked for by pressing Re-check, not a repair
// pass: nothing runs it on a schedule, and a listing of Apps never calls it.
//
// The returned spec is the one to render: installed when GitHub named an
// installation on the declared organisation, unchanged otherwise. An App that
// is not installed yet is not a failure, so it returns no error; GitHub that
// cannot answer is the spec's installLookup, shown beside the App, and the
// record is left as it was. An error is returned only when GitHub named the
// installation and it could not be recorded.
func (c *Service) findInstallation(ctx context.Context, spec githubAppSpec, actor string) (githubAppSpec, error) {
	if !spec.created || spec.installed || !spec.declared || spec.key == nil || spec.org == "" {
		return spec, nil
	}
	switch {
	case spec.origin == appCatalogue && spec.purpose == appTokens && c.deps.CatalogueApps != nil:
		// An installation is looked for on the organisation the declaration
		// names, and on no other.
		if !strings.EqualFold(spec.entry.Org, spec.org) {
			return spec, nil
		}
	case spec.purpose == appRunners && c.deps.RunnerApps != nil:
	default:
		return spec, nil
	}

	key := spec.key()
	if key == "" {
		return spec, nil
	}
	token, err := githubapp.AppToken(spec.appID, key, time.Now())
	if err != nil {
		spec.installLookup = "The App's stored key is not usable: " + err.Error()
		return spec, nil
	}
	askCtx, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	installation, err := githubapp.FindInstallation(askCtx, c.githubHTTP(), token, spec.org)
	switch {
	case errors.Is(err, githubapp.ErrNotInstalled):
		return spec, nil
	case err != nil:
		spec.installLookup = "GitHub could not say whether the App is installed: " + err.Error()
		return spec, nil
	}

	app := audit.App{ID: spec.appID, Slug: spec.appSlug}
	if spec.purpose == appRunners {
		app.Tier = spec.tier
		err = c.recordRunnerInstallation(ctx, spec, installation)
	} else {
		app.Name = spec.id
		err = c.recordCatalogueInstallation(ctx, spec, installation)
	}
	if err != nil {
		return spec, fmt.Errorf("the App is installed on %s and the installation could not be recorded here: %w", spec.org, err)
	}
	c.log().InfoContext(ctx, "an App installation was found by a re-check", logattr.SafeString("id", spec.id),
		logattr.SafeString("org", spec.org), slog.Int64("installation", installation), logattr.SafeString("by", actor))
	if spec.purpose == appRunners {
		c.record(ctx, audit.RunnerAppInstallationFound(audit.Identified(actor), spec.org, app, installation))
	} else {
		c.record(ctx, audit.CatalogueAppInstallationFound(audit.Identified(actor), spec.org, app, installation))
	}
	// What was cached was asked about the App without an installation.
	c.seen.forget(spec.id)
	spec.installed, spec.installationID = true, installation
	return spec, nil
}

// recordCatalogueInstallation keeps the installation in the App's record, as
// the setup redirect does.
func (c *Service) recordCatalogueInstallation(ctx context.Context, spec githubAppSpec, installation int64) error {
	store := c.deps.CatalogueApps
	record, key, created, err := store.Get(ctx, spec.id)
	if err != nil || !created || key == "" {
		return fmt.Errorf("the App's record is gone: %w", err)
	}
	record.InstallationID = installation
	return store.Put(ctx, record, key)
}

// recordRunnerInstallation is recordCatalogueInstallation for a runner App.
func (c *Service) recordRunnerInstallation(ctx context.Context, spec githubAppSpec, installation int64) error {
	store := c.deps.RunnerApps
	record, created, err := c.runnerApp(ctx, spec.tier, spec.org)
	key, hasKey, keyErr := store.PrivateKey(ctx, spec.tier, spec.org)
	if err = errors.Join(err, keyErr); err != nil || !created || !hasKey {
		return fmt.Errorf("the App's record is gone: %w", err)
	}
	record.InstallationID = installation
	return store.Put(ctx, record, key)
}
