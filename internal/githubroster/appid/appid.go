// Package appid is what the three sorts of GitHub App share: one id space.
//
// A GitHub App is one kind of record in storage layout v5 (ADR 0072), keyed by
// its id, with a purpose: the App that links a person's GitHub account (id
// `link`), a catalogue App an operator declares (its own id), or the App a
// self-hosted runner tier registers with in one organisation (id
// `runner-<tier>-<org>`). The purpose is also what makes the ids disjoint, so
// the rules that keep them apart are here and nowhere else.
package appid

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Purpose is what an App is for.
type Purpose string

// The purposes of an App.
const (
	Link      Purpose = "link"
	Catalogue Purpose = "catalogue"
	Runner    Purpose = "runner"
)

// Purposes lists every purpose, in the order above.
func Purposes() []Purpose { return []Purpose{Link, Catalogue, Runner} }

// Valid reports whether p is one of [Purposes].
func (p Purpose) Valid() bool { return slices.Contains(Purposes(), p) }

// LinkID is the id of the link App, of which there is at most one.
const LinkID = "link"

// RunnerPrefix begins the id of every runner App.
const RunnerPrefix = "runner-"

// ErrReserved is a catalogue App named like an App of another purpose.
var ErrReserved = errors.New("appid: the id is reserved")

// RunnerID is the id of the runner App of a tier in an organisation:
// `runner-<tier>-<org>`.
func RunnerID(tier, org string) string { return RunnerPrefix + tier + "-" + org }

// CheckCatalogueID refuses an id a catalogue App may not have: the link App's,
// and any that begins with the runner Apps' prefix.
func CheckCatalogueID(id string) error {
	if id == LinkID {
		return fmt.Errorf("%w: %q is the link App's", ErrReserved, id)
	}
	if strings.HasPrefix(id, RunnerPrefix) {
		return fmt.Errorf("%w: %q begins %q, which is the runner Apps'", ErrReserved, id, RunnerPrefix)
	}
	return nil
}

// PurposeOf is the purpose an id implies: `link` is the link App's, an id that
// begins `runner-` is a runner App's and any other is a catalogue App's.
func PurposeOf(id string) Purpose {
	switch {
	case id == LinkID:
		return Link
	case strings.HasPrefix(id, RunnerPrefix):
		return Runner
	}
	return Catalogue
}

// Limits on the labels of an App.
const (
	MaxLabels     = 16
	maxLabelBytes = 63
)

// labelPattern is a label key or value: lower-case letters, digits, dots,
// dashes and underscores, beginning and ending with a letter or digit.
var labelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)

// CheckLabels refuses labels that are too many, or whose key or value is not a
// short lower-case name. A label has a key and may have an empty value.
func CheckLabels(labels map[string]string) error {
	if len(labels) > MaxLabels {
		return fmt.Errorf("appid: %d labels, at most %d", len(labels), MaxLabels)
	}
	for k, v := range labels {
		if len(k) > maxLabelBytes || !labelPattern.MatchString(k) {
			return fmt.Errorf("appid: the label key %q is not a short lower-case name", k)
		}
		if v != "" && (len(v) > maxLabelBytes || !labelPattern.MatchString(v)) {
			return fmt.Errorf("appid: the value of the label %q is not a short lower-case name", k)
		}
	}
	return nil
}

// idPattern is any App id: a catalogue id (32 characters at most), `link`, or
// a runner id (`runner-` and a tier of 16 and an organisation login of 39).
var idPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

// Valid reports whether id can be the id of an App of any purpose, and so
// name one in an organisation's record.
func Valid(id string) bool { return idPattern.MatchString(id) }
