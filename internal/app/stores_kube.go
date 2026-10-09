//go:build !lambda

package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/kube"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/storage/logattr"
)

// openKubeStores is the `legacy` adapter's domain stores: today's ConfigMaps
// and Secrets, which the Lambda build does not have (stores_lambda.go).
func openKubeStores(ctx context.Context, cfg Config, st *store.Stores, log *slog.Logger) (stores, error) {
	// Today's ConfigMaps and Secrets, opened once by the ports' factory.
	var client *kube.Client
	if st.Backend != nil {
		client = st.Backend.Kube
	}
	if client == nil {
		return stores{}, errors.New("store: kubernetes needs the namespace's objects, and this process has none")
	}
	key, err := client.SessionKey(ctx, access.NewSessionKey)
	if err != nil {
		return stores{}, err
	}
	log.InfoContext(ctx, "keeping state in this namespace",
		slog.String("store", storeKubernetes), slog.String("namespace", client.Namespace()),
		slog.String("session_key_secret", client.SessionKeyName()),
		slog.String("oauth_client_secret", cfg.oauthSecretName))
	// The report the GitHub controller writes into is created HERE, by the
	// service, so that the controller's Role can name the one object it
	// updates: `create` cannot be narrowed to a name. Cheap and idempotent,
	// so it is done whether or not a controller is deployed, and a failure
	// is a warning — the directory works without it.
	github := kube.NewGitHubStatus(client)
	if err = github.Ensure(ctx); err != nil {
		log.WarnContext(ctx, "the GitHub status report could not be created; the GitHub page will show bindings only",
			slog.String("config_map", github.Name()), slog.Any("error", err))
	}
	// The Slack controller's report, created here for the same reason.
	slackStatus := kube.NewSlackStatus(client)
	if err = slackStatus.Ensure(ctx); err != nil {
		log.WarnContext(ctx, "the Slack status report could not be created; the Slack controller cannot report until it exists",
			slog.String("config_map", slackStatus.Name()), slog.Any("error", err))
	}
	// And the two objects connecting an organisation writes into, empty,
	// so the controller's Secret volume always has a Secret behind it.
	githubOrgs := kube.NewGitHubOrgs(client)
	if err = githubOrgs.Ensure(ctx); err != nil {
		log.WarnContext(ctx, "the objects GitHub organisations are connected into could not be created",
			slog.String("config_map", githubOrgs.ConfigMapName()), slog.String("secret", githubOrgs.SecretName()), slog.Any("error", err))
	}
	// And the Secret people's links are written into, so the controller's
	// Role can name an object that exists.
	githubLinks := kube.NewGitHubLinks(client)
	if err = githubLinks.Ensure(ctx); err != nil {
		log.WarnContext(ctx, "the Secret GitHub accounts are linked into could not be created",
			slog.String("secret", githubLinks.Name()), slog.Any("error", err))
	}
	// And the Secret runner Apps are kept in, so a deployment copying it
	// finds it before the first App is created.
	githubRunnerApps := kube.NewGitHubRunnerApps(client)
	if err = githubRunnerApps.Ensure(ctx); err != nil {
		log.WarnContext(ctx, "the Secret runner Apps are kept in could not be created",
			slog.String("secret", githubRunnerApps.SecretName()), slog.Any("error", err))
	}
	// And the Secret catalogue Apps are kept in, for the same reason.
	githubCatalogueApps := kube.NewGitHubCatalogueApps(client)
	if err = githubCatalogueApps.Ensure(ctx); err != nil {
		log.WarnContext(ctx, "the Secret catalogue Apps are kept in could not be created",
			slog.String("secret", githubCatalogueApps.SecretName()), slog.Any("error", err))
	}
	// And the Secret Slack workspaces are connected into, empty, so the
	// Slack controller's volume always has a Secret behind it. The records'
	// ConfigMap beside it is slackShared's, ensured once below.
	slackWorkspaces := kube.NewSlackWorkspaces(client)
	if err = slackWorkspaces.Ensure(ctx); err != nil {
		log.WarnContext(ctx, "the Secret Slack workspaces are connected into could not be created",
			slog.String("secret", slackWorkspaces.SecretName()), slog.Any("error", err))
	}
	// And the Secret catalogue Slack Apps are kept in, for the same reason.
	slackCatalogueApps := kube.NewSlackCatalogueApps(client)
	if err = slackCatalogueApps.Ensure(ctx); err != nil {
		log.WarnContext(ctx, "the Secret catalogue Slack Apps are kept in could not be created",
			slog.String("secret", slackCatalogueApps.SecretName()), slog.Any("error", err))
	}
	// And the ConfigMap Slack Connect channel definitions are kept in,
	// beside the workspaces' records the controller mounts.
	slackShared := kube.NewSlackShared(client)
	if err = slackShared.Ensure(ctx); err != nil {
		log.WarnContext(ctx, "the ConfigMap Slack Connect channels are kept in could not be created",
			slog.String("config_map", slackShared.ConfigMapName()), slog.Any("error", err))
	}
	// The records ConfigMap has a mirror Secret, the thing the chart's
	// recovery copy pushes (a PushSecret reads Secrets only). A ConfigMap
	// left with no records beside a mirror that has them is a restore, and
	// the records come back; otherwise the mirror is brought up to date.
	if restored, err := slackWorkspaces.ReconcileRecords(ctx); err != nil {
		log.WarnContext(ctx, "the Slack records and their recovery copy could not be reconciled",
			slog.String("config_map", slackWorkspaces.ConfigMapName()), slog.String("secret", slackWorkspaces.RecordsSecretName()),
			logattr.SafeError("error", err))
	} else if len(restored) > 0 {
		log.InfoContext(ctx, "restored Slack records from their recovery copy", slog.Any("keys", restored))
	}
	// Each connection's credential carries its record, so the GitHub Apps
	// Secret alone restores every organisation: put back a record a
	// restore left missing, and copy records into credentials written
	// before they carried one.
	if changed, err := githubOrgs.ReconcileRecords(ctx); err != nil {
		log.WarnContext(ctx, "the GitHub connections' records and credentials could not be reconciled",
			slog.String("config_map", githubOrgs.ConfigMapName()), slog.String("secret", githubOrgs.SecretName()), slog.Any("error", err))
	} else if len(changed) > 0 {
		log.InfoContext(ctx, "reconciled GitHub connection records with their credentials", slog.Any("keys", changed))
	}
	// Workspace credentials are one Secret, each entry carrying its
	// workspace's record, for the same reason. An older release kept one
	// Secret per workspace: those move in, and records a restore left
	// missing come back before the stored workspaces are reopened. A
	// failure warns rather than stops: an unmoved credential is still read
	// where it is.
	workspaces := kube.NewWorkspaces(client)
	credentials := kube.NewCredentials(client)
	if err = credentials.Ensure(ctx); err != nil {
		log.WarnContext(ctx, "the Secret workspace credentials are kept in could not be created",
			slog.String("secret", credentials.SecretName()), slog.Any("error", err))
	}
	if moved, err := credentials.Migrate(ctx, workspaces); err != nil {
		log.WarnContext(ctx, "not every workspace credential could be moved into one Secret; the rest are read where they are",
			slog.String("secret", credentials.SecretName()), slog.Any("moved", moved), slog.Any("error", err))
	} else if len(moved) > 0 {
		log.InfoContext(ctx, "moved workspace credentials into one Secret",
			slog.String("secret", credentials.SecretName()), slog.Any("workspaces", moved))
	}
	if restored, err := credentials.RestoreRecords(ctx, workspaces); err != nil {
		log.WarnContext(ctx, "workspace records missing beside their credentials could not be restored",
			slog.String("secret", credentials.SecretName()), slog.Any("restored", restored), slog.Any("error", err))
	} else if len(restored) > 0 {
		log.InfoContext(ctx, "restored workspace records from their credentials", slog.Any("workspaces", restored))
	}
	return stores{
		// The controllers' reports are read through the blob port, which is
		// the same two ConfigMaps in this adapter.
		githubReports: rails.NewBlobReports(st.Ports.Blob, "reports/github/"),
		slackReports:  rails.NewBlobReports(st.Ports.Blob, "reports/slack/"),
		githubOrgs:    githubOrgs,
		githubLinks:   githubLinks,
		// runner Apps are created only for declared tiers, but the store
		// is kept either way: an App created before a tier was dropped
		// stays visible, so it can be disconnected.
		githubRunnerApps: githubRunnerApps,
		// Likewise an App whose entry the catalogue no longer declares.
		githubCatalogueApps: githubCatalogueApps,
		slackCatalogueApps:  slackCatalogueApps,
		slackShared:         slackShared,
		slackChannels:       kube.NewSlackChannels(client),
		slackWorkspaces:     slackWorkspaces,
		workspaces:          workspaces,
		credentials:         credentials,
		settings: kube.NewSettings(client, kube.DeclaredClient{
			Name:      cfg.oauthSecretName,
			IDKey:     cfg.oauthIDKey,
			SecretKey: cfg.oauthSecretKey,
		}),
		sessionKey:  key,
		reviewToken: st.Ports.Identity.Verify,
		namespace:   client.Namespace(),
	}, nil
}

// useCluster adds what is still the cluster's with any adapter: the token
// review, and the declared OAuth client a deployment mounts, which is an input
// and not a record this service writes.
func useCluster(out *stores, cfg Config, st *store.Stores) {
	if st.Backend != nil && st.Backend.Kube != nil {
		client := st.Backend.Kube
		out.reviewToken = st.Ports.Identity.Verify
		out.namespace = client.Namespace()
		// A client declared by file or variable is already in out.settings;
		// the Kubernetes settings store would replace it with an empty one.
		if cfg.oauthSecretName != "" || !cfg.oauthDeclared.Configured() {
			out.settings = kube.NewSettings(client, kube.DeclaredClient{
				Name: cfg.oauthSecretName, IDKey: cfg.oauthIDKey, SecretKey: cfg.oauthSecretKey,
			})
		}
	}
}
