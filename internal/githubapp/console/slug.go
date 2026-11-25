package console

import (
	"context"
	"log/slog"
	"time"

	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/storage/logattr"
)

// An App's slug is its name on GitHub, and an owner who renames the App there
// changes it: github.com/apps/<slug> and the organisation's settings page of
// the App move with it, and the old slug answers 404. A record keeps the slug
// the App was created with, so every URL this service builds from a slug is
// built from the one GitHub gives now, which `GET /app` answers to the App's
// own JWT, and the record is brought up to date beside it.
//
// The Apps list asks nothing more for it: the slug comes with the answer the
// page already asks for, through the same short cache (githubCacheWindow), so
// a list costs what it did. Starting an install and disconnecting ask once,
// for one App. When GitHub cannot be asked, the stored slug is used and the
// page is not failed for it.

// slugRename writes the slug and page GitHub gives an App now into the App's
// record; refreshed is false when nothing changed.
type slugRename func(ctx context.Context, appID int64, slug, htmlURL string) (refreshed bool, err error)

// catalogueSlugRefresher is a store of catalogue Apps that can bring one
// App's slug up to date (internal/portstore, internal/kube). A store without
// it keeps the slug it was given, and the console still shows the current one.
type catalogueSlugRefresher interface {
	RefreshAppSlug(ctx context.Context, id string, appID int64, slug, htmlURL string) (bool, error)
}

// runnerSlugRefresher is catalogueSlugRefresher for runner Apps.
type runnerSlugRefresher interface {
	RefreshAppSlug(ctx context.Context, tier, org string, appID int64, slug, htmlURL string) (bool, error)
}

// catalogueRename writes back the slug of the catalogue App kept under id, or
// is nil where the store cannot.
func (c *Service) catalogueRename(id string) slugRename {
	store, ok := c.deps.CatalogueApps.(catalogueSlugRefresher)
	if !ok || id == "" {
		return nil
	}
	return func(ctx context.Context, appID int64, slug, htmlURL string) (bool, error) {
		return store.RefreshAppSlug(ctx, id, appID, slug, htmlURL)
	}
}

// runnerRename writes back the slug of one tier's runner App in org.
func (c *Service) runnerRename(tier, org string) slugRename {
	store, ok := c.deps.RunnerApps.(runnerSlugRefresher)
	if !ok {
		return nil
	}
	return func(ctx context.Context, appID int64, slug, htmlURL string) (bool, error) {
		return store.RefreshAppSlug(ctx, tier, org, appID, slug, htmlURL)
	}
}

// controllerRename writes back the slug of an organisation's App: into the
// catalogue App its record names (`app_ref`, layout v5), where the App's own
// record is. The organisation's record is not rewritten: the id an
// organisation's own App is kept under is derived from the slug it was
// connected with (internal/portstore ownAppID), and a record whose slug moved
// would no longer find its own App. A record that names no App keeps its slug,
// and the console still shows the current one.
func (c *Service) controllerRename(appRef string) slugRename {
	return c.catalogueRename(appRef)
}

// currentSlug is the slug of the App GitHub answered about, when it answered
// about this App, else the stored one.
func currentSlug(stored string, appID int64, app *githubapp.AppInfo) string {
	if app == nil || app.Slug == "" || (app.ID != 0 && app.ID != appID) {
		return stored
	}
	return app.Slug
}

// keepSlug writes a slug that moved back into the App's record. A failure is
// logged and never fails the page: the next answer from GitHub tries again.
func (c *Service) keepSlug(ctx context.Context, rename slugRename, appID int64, stored string, app *githubapp.AppInfo) {
	slug := currentSlug(stored, appID, app)
	if rename == nil || slug == stored {
		return
	}
	refreshed, err := rename(ctx, appID, slug, app.HTMLURL)
	switch {
	case err != nil:
		c.log().WarnContext(ctx, "an App renamed on GitHub could not have its record brought up to date", slog.Int64("app", appID),
			logattr.SafeString("from", stored), logattr.SafeString("to", slug), logattr.SafeError("error", err))
	case refreshed:
		c.log().InfoContext(ctx, "an App renamed on GitHub has its record brought up to date", slog.Int64("app", appID),
			logattr.SafeString("from", stored), logattr.SafeString("to", slug))
	}
}

// liveSlug is the App's slug as GitHub gives it now, asked as the App, with a
// slug that moved written back; the stored slug when GitHub cannot be asked.
// For one App and an operator's action: a list reads the slug from its
// observations instead.
func (c *Service) liveSlug(ctx context.Context, appID int64, key, stored string, rename slugRename) string {
	if appID == 0 || key == "" {
		return stored
	}
	token, err := githubapp.AppToken(appID, key, time.Now())
	if err != nil {
		return stored
	}
	askCtx, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	app, err := githubapp.GetApp(askCtx, c.githubHTTP(), token)
	if err != nil {
		c.log().WarnContext(ctx, "an App's slug could not be asked of GitHub; the stored one is used", slog.Int64("app", appID),
			logattr.SafeString("slug", stored), logattr.SafeError("error", err))
		return stored
	}
	c.keepSlug(ctx, rename, appID, stored, &app)
	return currentSlug(stored, appID, &app)
}
