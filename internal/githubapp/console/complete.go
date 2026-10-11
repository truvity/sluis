package console

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/storage/logattr"
)

// CompleteGitHubCreate is where GitHub sends the owner after Create, with a
// one-time code for the App's key. The key is kept and the owner is sent
// straight on to Install, under a fresh state: the one that brought them here
// is spent. The code is single use, so a lost answer after the exchange loses
// the key, as it always did: the call is not retried.
func (c *Service) CompleteGitHubCreate(ctx context.Context, f consolewire.Flow) (consolewire.Continued, error) {
	org, err := flowOrg(f, githubBind)
	if err != nil {
		return consolewire.Continued{}, err
	}
	actor, err := c.flowActor(ctx, f)
	if err != nil {
		return consolewire.Continued{}, err
	}
	store := c.deps.Orgs
	if store == nil {
		return consolewire.Continued{}, flowProblem(http.StatusConflict, "This deployment keeps no connected organisations.", "")
	}

	registration, err := githubapp.Convert(ctx, c.githubHTTP(), f.Code)
	if err != nil {
		c.log().WarnContext(ctx, "a GitHub App was created and its key could not be collected",
			logattr.SafeString("org", org), logattr.SafeError("error", err))
		return consolewire.Continued{}, flowProblem(http.StatusConflict,
			"GitHub created the App, and then would not hand over its key.", err.Error(),
			"The page was reloaded: the code GitHub returns can be exchanged once.",
			"More than an hour passed between creating the App and returning here.",
			"This service cannot reach api.github.com: the cluster's egress policy has to allow it.")
	}
	// The create page was for this organisation, so an App owned by
	// anybody else is not one this connect asked for.
	if !strings.EqualFold(registration.Owner, org) {
		return consolewire.Continued{}, flowProblem(http.StatusConflict,
			fmt.Sprintf("The App was created under %s, not %s.", registration.Owner, org), "")
	}
	record := connection.Record{
		Org: org, AppID: registration.ID, AppSlug: registration.Slug, HTMLURL: registration.HTMLURL,
		ConnectedAt: time.Now().UTC(), ConnectedBy: actor, Owner: f.Owner,
	}
	credential := connection.Credential{Org: org, AppID: registration.ID, PrivateKey: registration.PEM}
	if err = store.Put(ctx, record, credential); err != nil {
		c.log().ErrorContext(ctx, "a GitHub App was created and could not be kept", logattr.SafeString("org", org), logattr.SafeError("error", err))
		return consolewire.Continued{}, flowProblem(http.StatusConflict,
			"GitHub created the App, and it could not be saved here. Delete it on GitHub and connect again.", err.Error())
	}
	c.log().InfoContext(ctx, "a GitHub App was created", logattr.SafeString("org", org), slog.Int64("app", registration.ID),
		slog.String("slug", registration.Slug), logattr.SafeString("by", actor))
	c.record(ctx, audit.GitHubAppCreated(audit.Identified(actor), org, audit.App{ID: registration.ID, Slug: registration.Slug}))
	return consolewire.Continued{
		URL: githubapp.InstallURL(registration.Slug, consolewire.StatePlaceholder), Bind: githubBind + org, Owner: f.Owner, Actor: actor,
	}, nil
}

// CompleteGitHubInstall is where GitHub sends the owner after Install. The
// installation id in the query is a browser's word for it; what is kept is
// what GitHub says, asked as the App.
func (c *Service) CompleteGitHubInstall(ctx context.Context, f consolewire.Flow) (consolewire.Done, error) {
	org, err := flowOrg(f, githubBind)
	if err != nil {
		return consolewire.Done{}, err
	}
	actor, err := c.flowActor(ctx, f)
	if err != nil {
		return consolewire.Done{}, err
	}
	store := c.deps.Orgs
	if store == nil {
		return consolewire.Done{}, flowProblem(http.StatusConflict, "This deployment keeps no connected organisations.", "")
	}
	credential, found, err := store.Credential(ctx, org)
	if err != nil || !found {
		return consolewire.Done{}, flowProblem(http.StatusConflict,
			fmt.Sprintf("There is no App for %s to finish installing. Start Connect again.", org), errString(err))
	}
	token, err := githubapp.AppToken(credential.AppID, credential.PrivateKey, time.Now())
	if err != nil {
		return consolewire.Done{}, flowProblem(http.StatusConflict, "The App's stored key is not usable. Disconnect and connect again.", err.Error())
	}
	installation, err := githubapp.FindInstallation(ctx, c.githubHTTP(), token, org)
	if err != nil {
		summary := "GitHub could not say where the App is installed."
		if errors.Is(err, githubapp.ErrNotInstalled) {
			summary = fmt.Sprintf("The App is not installed on %s yet. Finish installing from the console.", org)
		}
		return consolewire.Done{}, flowProblem(http.StatusConflict, summary, err.Error())
	}
	if claimed := f.InstallationID; claimed != "" && claimed != strconv.FormatInt(installation, 10) {
		c.log().WarnContext(ctx, "the setup redirect named another installation than GitHub reports; keeping GitHub's",
			logattr.SafeString("org", org), logattr.SafeString("claimed", claimed), slog.Int64("installation", installation))
	}

	records, err := store.List(ctx)
	if err != nil {
		return consolewire.Done{}, flowProblem(http.StatusConflict, "The App is installed and could not be recorded here.", err.Error())
	}
	record, known := recordOf(records, org)
	if !known {
		record = connection.Record{Org: org, AppID: credential.AppID, ConnectedAt: time.Now().UTC(), ConnectedBy: actor, Owner: f.Owner}
	}
	record.InstallationID, credential.InstallationID = installation, installation
	if record.AppSlug == "" {
		return consolewire.Done{}, flowProblem(http.StatusConflict, "The App is installed and its record is incomplete. Disconnect and connect again.", "")
	}
	if err = store.Put(ctx, record, credential); err != nil {
		return consolewire.Done{}, flowProblem(http.StatusConflict, "The App is installed and could not be recorded here.", err.Error())
	}
	c.log().InfoContext(ctx, "a GitHub App was installed", logattr.SafeString("org", org), slog.Int64("installation", installation),
		logattr.SafeString("by", actor))
	c.record(ctx, audit.GitHubOrgConnected(audit.Identified(actor), org, credential.AppID, installation, record.Owner))
	// The installation is a write the controller acts on: tick it now, as the
	// module's own self-tick.
	return consolewire.Done{}, nil
}
