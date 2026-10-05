package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/server"
	"github.com/truvity/sluis/internal/settings"
	"github.com/truvity/sluis/internal/store"
)

// CappedSessionLifetimeForTest exposes cappedSessionLifetime to
// internal/app_test, following the export_test.go idiom the issuer
// package already uses (see internal/issuer/export_service_test.go):
// the black-box tests exercise the package through its real API
// everywhere else, and this is the one calculation with no API of its
// own to call through.
func CappedSessionLifetimeForTest(session, absolute time.Duration) time.Duration {
	return cappedSessionLifetime(session, absolute)
}

// OpenStoredForTest exposes openStored to internal/app_test, the same
// idiom: it is only ever wired in as a hub.Reopener, so there is no real
// API of its own for a black-box test to call through.
func OpenStoredForTest(
	ctx context.Context, connectors []server.Connector, kind string, cred backend.Credential,
) (backend.Backend, error) {
	return openStored(ctx, connectors, kind, cred)
}

// KeptForTest is what openStores chose, for internal/app_test to look at:
// which of the domain stores exist, and the ones a test writes through.
type KeptForTest struct {
	Workspaces  hub.Store
	Credentials hub.CredentialStore
	GitHubOrgs  bool
	GitHubLinks bool
	SlackShared bool
	SessionKey  []byte
	// OAuthClient is the client the settings store hands the Google connector.
	OAuthClient settings.OAuthClient
}

// OpenStoresForTest runs the one switch between the kube-backed domain stores
// and the port-backed ones.
func OpenStoresForTest(ctx context.Context, cfg Config, st *store.Stores, log *slog.Logger) (KeptForTest, error) {
	declared, err := declaredOAuthClient(ctx, cfg.oauthClient, st.Secrets)
	if err != nil {
		return KeptForTest{}, err
	}
	cfg.oauthDeclared = declared
	kept, err := openStores(ctx, cfg, st, log)
	if err != nil {
		return KeptForTest{}, err
	}
	client, err := kept.settings.OAuthClient(ctx)
	if err != nil {
		return KeptForTest{}, err
	}
	return KeptForTest{
		OAuthClient: client,
		Workspaces:  kept.workspaces, Credentials: kept.credentials, GitHubOrgs: kept.githubOrgs != nil,
		GitHubLinks: kept.githubLinks != nil, SlackShared: kept.slackShared != nil, SessionKey: kept.sessionKey,
	}, nil
}
