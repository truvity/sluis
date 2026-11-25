package kube

import (
	"context"
	"encoding/json"

	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/runnerapp"
)

// The RefreshAppSlug methods write the slug GitHub gives an App now into its
// record: an owner who renames an App on GitHub changes its slug, and the
// record keeps the one it was created with. Only the record's slug and page
// change, under the Secret's version; the App's keys are not touched, and a
// record that is gone or names another GitHub App by now is left alone.

// RefreshAppSlug writes a catalogue App's current slug and page into its
// record.
func (s *GitHubCatalogueApps) RefreshAppSlug(ctx context.Context, id string, appID int64, slug, htmlURL string) (bool, error) {
	var refreshed bool
	err := s.edit(ctx, func(data map[string][]byte) {
		refreshed = false
		record, err := catalogueapp.DecodeRecord(data[catalogueapp.RecordKey(id)])
		if err != nil || record.ID != id || !renamed(&record.AppSlug, &record.HTMLURL, record.AppID, appID, slug, htmlURL) {
			return
		}
		if raw, err := json.Marshal(record); err == nil {
			data[catalogueapp.RecordKey(id)], refreshed = raw, true
		}
	})
	return refreshed && err == nil, err
}

// RefreshAppSlug writes a runner App's current slug and page into its record.
func (s *GitHubRunnerApps) RefreshAppSlug(ctx context.Context, tier, org string, appID int64, slug, htmlURL string) (bool, error) {
	var refreshed bool
	err := s.edit(ctx, func(data map[string][]byte) {
		refreshed = false
		key := runnerapp.RecordKey(tier, org)
		record, err := runnerapp.DecodeRecord(data[key])
		if err != nil || record.Tier != tier || record.Org != org ||
			!renamed(&record.AppSlug, &record.HTMLURL, record.AppID, appID, slug, htmlURL) {
			return
		}
		if raw, err := json.Marshal(record); err == nil {
			data[key], refreshed = raw, true
		}
	})
	return refreshed && err == nil, err
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
