package portstore

import (
	"strings"
)

// WithPlace sets the secrets adapter and the installation root the places are
// reported under. An empty adapter reports no place.
func (b *Base) WithPlace(adapter, root string) *Base {
	b.placeAdapter, b.placeRoot = adapter, strings.TrimRight(root, "/")
	return b
}

// GitHubAppPlace is where a GitHub App's key is kept; the id is the one
// [GitHubApps] gives (`link`, a catalogue id, `runner-<tier>-<org>`). On layout
// v5 that is the App's own prefix, and on layout v4 only the root. ok is false
// when the secrets adapter is not known.
func (b *Base) GitHubAppPlace(id string) (adapter, location string, ok bool) {
	if b.placeAdapter == "" {
		return "", "", false
	}
	if b.v5 == nil || id == "" {
		return b.placeAdapter, b.placeRoot + "/", true
	}
	return b.placeAdapter, b.placeRoot + "/" + b.v5.GitHub().AppPrefix(id), true
}
