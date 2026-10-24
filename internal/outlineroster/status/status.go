// Package status is the contract between the Outline controller and the
// console: what the controller last did to each Outline workspace, and why,
// written where the console reads it (one blob per workspace).
//
// The document answers, per person, per managed group and per collection, the
// one question the console's page asks: is this true yet, and if not, what
// happens next or what is waiting on somebody.
package status

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/rails"
)

// Version is the document version this build writes and reads. A reader
// refuses any other.
const Version = 1

// Key is the key one workspace's document is written under.
func Key(workspace string) string { return workspace + ".json" }

// WorkspaceOfKey reads a workspace back out of a key, and reports whether the
// key is one this contract wrote.
func WorkspaceOfKey(key string) (string, bool) {
	workspace, found := strings.CutSuffix(key, ".json")
	if !found || !ValidWorkspace(workspace) {
		return "", false
	}
	return workspace, true
}

var validWorkspace = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)

// ValidWorkspace reports whether a string can be a workspace key.
func ValidWorkspace(key string) bool { return validWorkspace.MatchString(key) }

// Workspace is one workspace's report.
type Workspace struct {
	Version   int    `json:"version"`
	Workspace string `json:"workspace"`
	// Enabled is whether the controller acts on this workspace. A disabled
	// one is still derived every tick, and its rows say what WOULD change.
	Enabled bool `json:"enabled"`
	// URL is the connected workspace's origin, and KeyUser the address of
	// the user its API key belongs to.
	URL     string `json:"url,omitempty"`
	KeyUser string `json:"key_user,omitempty"`
	// Held is why the whole workspace is held, when it is: nothing in it is
	// changed until a person acts.
	Held string `json:"held,omitempty"`
	Tick Tick   `json:"tick"`
	// Users are the workspace's people: their role and whether they are
	// suspended.
	Users []User `json:"users,omitempty"`
	// Groups are the managed Outline groups and their members.
	Groups []Group `json:"groups,omitempty"`
	// Collections are the bound collections, their grants and, in a strict
	// collection, their direct members.
	Collections []Collection `json:"collections,omitempty"`
	// Reported are findings that are said and never acted on: drifted
	// settings, shares across companies, integrations that post elsewhere.
	Reported []Finding `json:"reported,omitempty"`
	// SharesScannedAt is when the document shares were last read.
	SharesScannedAt time.Time `json:"shares_scanned_at,omitzero"`
	// Breakers are the breakers this pass tripped.
	Breakers []Breaker `json:"breakers,omitempty"`
	// Recorded are the keys of the holds the controller has already put in
	// the audit trail, written only by a pass that acts. Nothing renders them.
	Recorded []string `json:"recorded,omitempty"`
}

// State is where one row stands.
type State string

// The row states.
const (
	// StateOK: what is true matches what should be.
	StateOK State = "ok"
	// StateWillAdd: a person or a grant belongs and is not there.
	StateWillAdd State = "will-add"
	// StateWillRemove: a person or a grant is there and does not belong, and
	// the directory vouches for it.
	StateWillRemove State = "will-remove"
	// StateWillChange: a role or a grant's level is to change.
	StateWillChange State = "will-change"
	// StateWillSuspend: a leaver is to be suspended.
	StateWillSuspend State = "will-suspend"
	// StateWillActivate: a suspended account the directory has back is to be
	// reactivated.
	StateWillActivate State = "will-activate"
	// StateWillCreate and StateWillAdopt: a group or a collection.
	StateWillCreate State = "will-create"
	StateWillAdopt  State = "will-adopt"
	// StateHeld: something is to be done and is not being done until a
	// person acts; Reason says why.
	StateHeld State = "held"
	// StateRetrying: the directory could not answer for the person this
	// pass; nothing changes for them and the pass after tries again.
	StateRetrying State = "retrying"
	// StateReported: said, never acted on.
	StateReported State = "reported"
	// StateIgnored: on the workspace's ignore list, or the API key's own
	// user: never demoted, removed or suspended.
	StateIgnored State = "ignored"
)

// Action is one change the controller makes to Outline.
type Action string

// The actions.
const (
	ActionCreate   Action = "create"
	ActionAdopt    Action = "adopt"
	ActionAdd      Action = "add"
	ActionRemove   Action = "remove"
	ActionPromote  Action = "promote"
	ActionDemote   Action = "demote"
	ActionSuspend  Action = "suspend"
	ActionActivate Action = "activate"
	ActionGrant    Action = "grant"
	ActionRevoke   Action = "revoke"
)

// User is one Outline account.
type User struct {
	Email string `json:"email,omitempty"`
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	// Role is the account's role now; Target what the policy gives it.
	Role      string `json:"role"`
	Target    string `json:"target,omitempty"`
	Suspended bool   `json:"suspended,omitempty"`
	State     State  `json:"state"`
	Action    Action `json:"action,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Member is one person in a group, or a direct member of a collection.
type Member struct {
	Email  string `json:"email,omitempty"`
	UserID string `json:"user_id"`
	// Permission is a direct collection member's permission.
	Permission string `json:"permission,omitempty"`
	State      State  `json:"state"`
	Action     Action `json:"action,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// Group is one managed Outline group: the internal group of that name.
type Group struct {
	Name    string   `json:"name"`
	ID      string   `json:"id,omitempty"`
	State   State    `json:"state"`
	Action  Action   `json:"action,omitempty"`
	Reason  string   `json:"reason,omitempty"`
	Members []Member `json:"members,omitempty"`
}

// Grant is one group's permission on a collection.
type Grant struct {
	Group   string `json:"group"`
	GroupID string `json:"group_id,omitempty"`
	// Permission is the grant now, empty when there is none; Target what the
	// policy gives, empty for a grant to be revoked.
	Permission string `json:"permission,omitempty"`
	Target     string `json:"target,omitempty"`
	State      State  `json:"state"`
	Action     Action `json:"action,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// Collection is one bound collection.
type Collection struct {
	Name string `json:"name"`
	// ID is empty for a collection that does not exist yet.
	ID        string   `json:"id,omitempty"`
	Mode      string   `json:"mode"`
	Directory string   `json:"directory,omitempty"`
	State     State    `json:"state"`
	Action    Action   `json:"action,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Grants    []Grant  `json:"grants,omitempty"`
	Members   []Member `json:"members,omitempty"`
}

// Finding is one thing reported and never acted on.
type Finding struct {
	// Kind is `setting`, `share` or `integration`.
	Kind string `json:"kind"`
	// Subject is what it is about: a setting's name, a document, an
	// integration.
	Subject string `json:"subject"`
	// Collection is the collection a share or an integration is in.
	Collection string `json:"collection,omitempty"`
	Detail     string `json:"detail"`
}

// The finding kinds.
const (
	FindingSetting     = "setting"
	FindingShare       = "share"
	FindingIntegration = "integration"
)

// Breaker is a set of changes held until an operator confirms it.
type Breaker struct {
	// Scope names the breaker: `admins`, `suspensions`, `group:<name>` or
	// `collection:<name>`.
	Scope string `json:"scope"`
	// Affected is how many the held changes concern, and Total how many the
	// scope has.
	Affected int `json:"affected"`
	Total    int `json:"total"`
	// Reason says which rule tripped.
	Reason string `json:"reason"`
	// Fingerprint names exactly this set. Confirming it lets this set, and
	// no other, go ahead.
	Fingerprint string `json:"fingerprint"`
	Confirmed   bool   `json:"confirmed,omitempty"`
}

// Tick is how the last pass over the workspace went.
type Tick struct {
	At      time.Time `json:"at"`
	Outcome Outcome   `json:"outcome"`
	Error   string    `json:"error,omitempty"`
	// Changes is how many actions were taken, or, when disabled, would have
	// been.
	Changes  int `json:"changes"`
	Held     int `json:"held"`
	Retrying int `json:"retrying,omitempty"`
	Waiting  int `json:"waiting,omitempty"`
}

// Outcome is a tick's result, as one word.
type Outcome string

// The outcomes.
const (
	OutcomeInSync   Outcome = "in-sync"
	OutcomeApplied  Outcome = "applied"
	OutcomeDryRun   Outcome = "dry-run"
	OutcomeHeld     Outcome = "held"
	OutcomeRetrying Outcome = "retrying"
	// OutcomeWaiting: nothing to do yet; the workspace is not connected.
	OutcomeWaiting Outcome = "waiting"
	// OutcomeFailed: the tick could not complete. Nothing was changed on its
	// account.
	OutcomeFailed Outcome = "failed"
)

// OutcomeOf is the report word for a rails outcome.
func OutcomeOf(o rails.Outcome) Outcome {
	switch o {
	case rails.OutcomeDryRun:
		return OutcomeDryRun
	case rails.OutcomeApplied:
		return OutcomeApplied
	case rails.OutcomeHeld:
		return OutcomeHeld
	case rails.OutcomeRetrying:
		return OutcomeRetrying
	case rails.OutcomeWaiting:
		return OutcomeWaiting
	default:
		return OutcomeInSync
	}
}

// ErrVersion is a document of a version this build does not read.
var ErrVersion = errors.New("status: unsupported document version")

// Encode writes one workspace's document in a fixed order, so two ticks'
// documents differ by what changed and not by what moved. It never reorders
// the caller's own slices.
func Encode(w Workspace) (string, error) {
	switch {
	case !ValidWorkspace(w.Workspace):
		return "", fmt.Errorf("status: %q is not a workspace key", w.Workspace)
	case w.Version != 0 && w.Version != Version:
		return "", fmt.Errorf("%w: %d", ErrVersion, w.Version)
	}
	w.Version = Version
	w.Users = slices.Clone(w.Users)
	sort.Slice(w.Users, func(i, j int) bool { return less(w.Users[i].Email, w.Users[i].ID, w.Users[j].Email, w.Users[j].ID) })
	w.Groups = slices.Clone(w.Groups)
	sort.Slice(w.Groups, func(i, j int) bool { return w.Groups[i].Name < w.Groups[j].Name })
	for i := range w.Groups {
		w.Groups[i].Members = sortedMembers(w.Groups[i].Members)
	}
	w.Collections = slices.Clone(w.Collections)
	sort.Slice(w.Collections, func(i, j int) bool { return w.Collections[i].Name < w.Collections[j].Name })
	for i := range w.Collections {
		c := &w.Collections[i]
		c.Members = sortedMembers(c.Members)
		c.Grants = slices.Clone(c.Grants)
		sort.Slice(c.Grants, func(i, j int) bool {
			return less(c.Grants[i].Group, c.Grants[i].GroupID, c.Grants[j].Group, c.Grants[j].GroupID)
		})
	}
	w.Reported = slices.Clone(w.Reported)
	sort.Slice(w.Reported, func(i, j int) bool {
		a, b := w.Reported[i], w.Reported[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return less(a.Collection, a.Subject+"\x00"+a.Detail, b.Collection, b.Subject+"\x00"+b.Detail)
	})
	w.Breakers = slices.Clone(w.Breakers)
	sort.Slice(w.Breakers, func(i, j int) bool { return w.Breakers[i].Scope < w.Breakers[j].Scope })
	w.Recorded = slices.Sorted(slices.Values(w.Recorded))
	raw, err := json.Marshal(w)
	if err != nil {
		return "", fmt.Errorf("status: encode %s: %w", w.Workspace, err)
	}
	return string(raw), nil
}

// Decode reads one workspace's document.
func Decode(raw string) (Workspace, error) {
	var w Workspace
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		return Workspace{}, fmt.Errorf("status: decode: %w", err)
	}
	if w.Version != Version {
		return Workspace{}, fmt.Errorf("%w: %d", ErrVersion, w.Version)
	}
	return w, nil
}

func less(a1, a2, b1, b2 string) bool {
	if a1 != b1 {
		return a1 < b1
	}
	return a2 < b2
}

func sortedMembers(members []Member) []Member {
	out := slices.Clone(members)
	sort.SliceStable(out, func(i, j int) bool { return less(out[i].Email, out[i].UserID, out[j].Email, out[j].UserID) })
	return out
}
