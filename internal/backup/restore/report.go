package restore

import (
	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/port"
)

// Action is what a restore does (or did) with one record.
type Action string

// The actions.
const (
	// Create: the destination holds nothing under the name.
	Create Action = "create"
	// Overwrite: the destination holds a different value.
	Overwrite Action = "overwrite"
	// Same: the destination already holds the archive's value.
	Same Action = "same"
	// Expired: the record had expired by now and is skipped.
	Expired Action = "expired"
	// Regenerated: a lease, cache or the like, which the module rebuilds.
	Regenerated Action = "regenerated"
)

// Record types of an [Item].
const (
	TypeState  = "state"
	TypeIndex  = "index"
	TypeSecret = "secret"
	TypeBlob   = "blob"
)

// Item is one record that is not [Same]: its name and, for an overwrite, the
// version the destination holds now. It never carries a value.
type Item struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	Action Action `json:"action"`
	// Version is the destination's current revision, for [Overwrite].
	Version string `json:"version,omitempty"`
}

// SectionReport is one module's section.
type SectionReport struct {
	Module      port.Module    `json:"module"`
	Section     backup.Section `json:"section"`
	Create      int            `json:"create"`
	Overwrite   int            `json:"overwrite"`
	Same        int            `json:"same"`
	Expired     int            `json:"expired"`
	Regenerated int            `json:"regenerated"`
	// Items lists the records that are not Same, up to [Options.MaxItems];
	// Truncated says the list is shorter than the counts.
	Items     []Item `json:"items,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Report is the outcome of [Preview] or [Apply].
type Report struct {
	Installation string          `json:"installation"`
	ID           string          `json:"id"`
	Layout       backup.Layout   `json:"layout"`
	Created      string          `json:"created"`
	Sections     []SectionReport `json:"sections"`
}

// Totals sums the counts of every section.
func (r *Report) Totals() SectionReport {
	var t SectionReport
	for _, s := range r.Sections {
		t.Create += s.Create
		t.Overwrite += s.Overwrite
		t.Same += s.Same
		t.Expired += s.Expired
		t.Regenerated += s.Regenerated
	}
	return t
}

func (s *SectionReport) add(a Action, it Item, max int) {
	switch a {
	case Create:
		s.Create++
	case Overwrite:
		s.Overwrite++
	case Same:
		s.Same++
		return
	case Expired:
		s.Expired++
	case Regenerated:
		s.Regenerated++
	}
	if len(s.Items) < max {
		it.Action = a
		s.Items = append(s.Items, it)
	} else {
		s.Truncated = true
	}
}
