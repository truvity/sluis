package portstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/githubroster/appid"
)

// GitHubApp is one GitHub App of any purpose, as the one kind it is in storage
// layout v5: an id, a purpose and the part of the record the three purposes
// share. It carries no credential.
type GitHubApp struct {
	// ID is `link`, a catalogue App's own id, or `runner-<tier>-<org>`.
	ID      string
	Purpose appid.Purpose
	Labels  map[string]string
	// Org is the organisation the App belongs to: a catalogue or runner App's
	// organisation, and the link App's owner.
	Org string
	// Tier is a runner App's tier, and empty for the others.
	Tier           string
	AppID          int64
	AppSlug        string
	InstallationID int64
	HTMLURL        string
	ConnectedAt    time.Time
	ConnectedBy    string
}

// Installed reports whether a token can be minted for the App yet. The link
// App has no installation: it acts for the people who authorize it.
func (a GitHubApp) Installed() bool { return a.Purpose == appid.Link || a.InstallationID != 0 }

// GitHubApps reads every GitHub App as the one kind it is, whichever of the
// three typed stores wrote it: [GitHubCatalogueApps], [GitHubRunnerApps] and
// the link App of [GitHubOrgs] keep their methods and their item keys, so a
// record written before it had a purpose reads back with the one its key
// implies. An organisation names an App by this id.
type GitHubApps struct{ b *Base }

// NewGitHubApps returns the store.
func NewGitHubApps(b *Base) *GitHubApps { return &GitHubApps{b: b} }

// List returns every App, sorted by id.
func (s *GitHubApps) List(ctx context.Context) ([]GitHubApp, error) {
	var out []GitHubApp
	if link, ok, err := NewGitHubOrgs(s.b).LinkApp(ctx); err != nil {
		return nil, err
	} else if ok {
		out = append(out, GitHubApp{
			ID: appid.LinkID, Purpose: appid.Link, Labels: link.Labels, Org: link.Owner, AppID: link.AppID, AppSlug: link.AppSlug,
			HTMLURL: link.HTMLURL, ConnectedAt: link.ConnectedAt, ConnectedBy: link.ConnectedBy,
		})
	}
	catalogue, err := NewGitHubCatalogueApps(s.b).List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range catalogue {
		r := &catalogue[i]
		out = append(out, GitHubApp{
			ID: r.ID, Purpose: appid.Catalogue, Labels: r.Labels, Org: r.Org, AppID: r.AppID, AppSlug: r.AppSlug,
			InstallationID: r.InstallationID, HTMLURL: r.HTMLURL, ConnectedAt: r.ConnectedAt, ConnectedBy: r.ConnectedBy,
		})
	}
	runners, err := NewGitHubRunnerApps(s.b).List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range runners {
		r := &runners[i]
		out = append(out, GitHubApp{
			ID: r.ID(), Purpose: appid.Runner, Labels: r.Labels, Org: r.Org, Tier: r.Tier, AppID: r.AppID, AppSlug: r.AppSlug,
			InstallationID: r.InstallationID, HTMLURL: r.HTMLURL, ConnectedAt: r.ConnectedAt, ConnectedBy: r.ConnectedBy,
		})
	}
	slices.SortFunc(out, func(a, b GitHubApp) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// Get returns the App with the id.
func (s *GitHubApps) Get(ctx context.Context, id string) (GitHubApp, bool, error) {
	all, err := s.List(ctx)
	if err != nil {
		return GitHubApp{}, false, err
	}
	for i := range all {
		if all[i].ID == id {
			return all[i], true, nil
		}
	}
	return GitHubApp{}, false, nil
}

// PrivateKey reads the key of the App with the id: the link App has none (its
// credential is a client secret), so it is reported absent.
func (s *GitHubApps) PrivateKey(ctx context.Context, id string) (string, bool, error) {
	switch appid.PurposeOf(id) {
	case appid.Link:
		return "", false, nil
	case appid.Runner:
		app, ok, err := s.Get(ctx, id)
		if err != nil || !ok {
			return "", false, err
		}
		return NewGitHubRunnerApps(s.b).PrivateKey(ctx, app.Tier, app.Org)
	}
	_, key, ok, err := NewGitHubCatalogueApps(s.b).Get(ctx, id)
	if err != nil || !ok {
		return "", false, err
	}
	return key, key != "", nil
}

// errNoApp is an organisation that names an App nobody has kept.
var errNoApp = errors.New("portstore: no such GitHub App")

// requireKey reads the key an organisation's App id names, so that an
// organisation never refers to an App that cannot sign.
func (s *GitHubApps) requireKey(ctx context.Context, id string) (string, error) {
	if id == appid.LinkID {
		return "", fmt.Errorf("portstore: the link App signs no installation token; an organisation cannot use it")
	}
	key, ok, err := s.PrivateKey(ctx, id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%w: %s", errNoApp, id)
	}
	return key, nil
}
