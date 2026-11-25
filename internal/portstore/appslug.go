package portstore

import (
	"context"
	"encoding/json"

	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/runnerapp"
)

// An App's slug is its name on GitHub, and an owner who renames the App there
// changes it. A record keeps the slug the App had when it was created, so the
// console, which reads GitHub as the App, writes the one GitHub gives now back
// into the record: the RefreshAppSlug methods below.
//
// Each changes the slug and the App's page and nothing else, under the item's
// revision, so a concurrent install or rotation is never written over. A
// record that is gone, or names another GitHub App by now, is left alone.
// refreshed is false when nothing was written.

// RefreshAppSlug writes a catalogue App's current slug and page into its
// record.
func (s *GitHubCatalogueApps) RefreshAppSlug(ctx context.Context, id string, appID int64, slug, htmlURL string) (bool, error) {
	return s.b.refreshAppSlug(ctx, ghCatalogueKey(id), func(raw []byte) ([]byte, error) {
		record, err := catalogueapp.DecodeRecord(raw)
		if err != nil || record.ID != id || !renamed(&record.AppSlug, &record.HTMLURL, record.AppID, appID, slug, htmlURL) {
			return nil, nil //nolint:nilerr // a record that does not decode is not this write's to repair
		}
		return json.Marshal(record)
	})
}

// RefreshAppSlug writes a runner App's current slug and page into its record.
func (s *GitHubRunnerApps) RefreshAppSlug(ctx context.Context, tier, org string, appID int64, slug, htmlURL string) (bool, error) {
	return s.b.refreshAppSlug(ctx, runnerKey(tier, org), func(raw []byte) ([]byte, error) {
		record, err := runnerapp.DecodeRecord(raw)
		if err != nil || record.Tier != tier || record.Org != org ||
			!renamed(&record.AppSlug, &record.HTMLURL, record.AppID, appID, slug, htmlURL) {
			return nil, nil //nolint:nilerr // a record that does not decode is not this write's to repair
		}
		return json.Marshal(record)
	})
}

// renamed moves a record's slug and page to the ones GitHub gives, when the
// record is the App asked about and the slug differs; false leaves both.
func renamed(slug, page *string, recordApp, appID int64, nextSlug, nextPage string) bool {
	if nextSlug == "" || recordApp != appID || *slug == nextSlug {
		return false
	}
	*slug = nextSlug
	if nextPage != "" {
		*page = nextPage
	}
	return true
}

// refreshAppSlug rewrites one item's record with what change makes of it, and
// keeps the item as it is when change makes nothing.
func (b *Base) refreshAppSlug(ctx context.Context, key string, change func(raw []byte) ([]byte, error)) (bool, error) {
	var refreshed bool
	err := b.editItem(ctx, key, 0, func(cur *item) (*item, error) {
		refreshed = false
		if cur == nil || len(cur.Record) == 0 {
			return nil, errKeep
		}
		next, err := change(cur.Record)
		if err != nil {
			return nil, err
		}
		if next == nil {
			return nil, errKeep
		}
		refreshed = true
		return &item{Record: json.RawMessage(next), Secret: cur.Secret}, nil
	})
	return refreshed && err == nil, err
}
