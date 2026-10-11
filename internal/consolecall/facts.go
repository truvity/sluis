package consolecall

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// DirectoryFacts are what a module may know of the connected directories for one
// call. A module reads no directory of its own (it holds no grant on the Google
// module's snapshot): the console, which already reads that snapshot, resolves
// what the method needs and sends it in the envelope. The module takes it as the
// console's word, like the caller snapshot, and refuses a field that is not
// well formed ([DirectoryFacts.Validate]).
//
// Only what the methods ask is here, and each part is bounded.
type DirectoryFacts struct {
	// Directories are every connected directory: for choosing and showing an
	// owner.
	Directories []Directory `json:"directories,omitempty"`
	// Sources are the directories' groups as a picker lists them, with
	// SourcesError saying why they could not be read.
	Sources      []SourceDirectory `json:"sources,omitempty"`
	SourcesError string            `json:"sources_error,omitempty"`
	// Groups and Users are, by lowercase address, what the call's own addresses
	// are: whether a source is a group of a connected directory, and whether a
	// listed person is an active user of one.
	Groups map[string]GroupFact `json:"groups,omitempty"`
	Users  map[string]UserFact  `json:"users,omitempty"`
}

// Directory is one connected directory as an owner is chosen and shown.
type Directory struct {
	ID      string   `json:"id"`
	Primary string   `json:"primary,omitempty"`
	Served  []string `json:"served,omitempty"`
	// Authoritative is the served domains the directory answers for with
	// confidence, sorted: what an owner is labelled with.
	Authoritative []string `json:"authoritative,omitempty"`
}

// SourceDirectory is a connected directory and its groups, as a picker lists them.
type SourceDirectory struct {
	Workspace string        `json:"workspace"`
	Domains   []string      `json:"domains,omitempty"`
	Groups    []SourceGroup `json:"groups,omitempty"`
}

// SourceGroup is one group of a directory.
type SourceGroup struct {
	Email   string `json:"email"`
	Members int    `json:"members,omitempty"`
}

// GroupFact is whether an address is a group of a connected directory, and of
// which. Err is the hub's own words when the address is not one.
type GroupFact struct {
	Found     bool   `json:"found,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Err       string `json:"err,omitempty"`
}

// UserFact is whether an address is an account a connected directory holds.
// InDomain is whether the address is in a domain a connected directory serves
// (Workspace says which), and Authoritative whether the directory can vouch for
// the rest just now.
type UserFact struct {
	InDomain      bool   `json:"in_domain,omitempty"`
	Authoritative bool   `json:"authoritative,omitempty"`
	Found         bool   `json:"found,omitempty"`
	Suspended     bool   `json:"suspended,omitempty"`
	Workspace     string `json:"workspace,omitempty"`
	Err           string `json:"err,omitempty"`
}

// The bounds of the facts of one call.
const (
	MaxDirectories  = 256
	MaxSourceGroups = 4000
	MaxAddressFacts = 2000
)

// ErrFacts is a facts field that is not well formed.
var ErrFacts = errors.New("the directory facts are not valid")

// Validate refuses a field that is oversized, names a directory twice or none,
// or says an address belongs to a directory the field does not list. A forged
// field can still lie within these rules, as the caller snapshot can: only the
// console class can send it.
func (f *DirectoryFacts) Validate() error {
	if f == nil {
		return nil
	}
	if len(f.Directories) > MaxDirectories || len(f.Groups) > MaxAddressFacts || len(f.Users) > MaxAddressFacts {
		return fmt.Errorf("%w: too many entries", ErrFacts)
	}
	ids := map[string]bool{}
	for i := range f.Directories {
		id := f.Directories[i].ID
		if id == "" || ids[id] {
			return fmt.Errorf("%w: a directory has no id, or is listed twice", ErrFacts)
		}
		ids[id] = true
	}
	groups := 0
	for i := range f.Sources {
		if !ids[f.Sources[i].Workspace] {
			return fmt.Errorf("%w: groups of a directory that is not listed", ErrFacts)
		}
		groups += len(f.Sources[i].Groups)
	}
	if groups > MaxSourceGroups {
		return fmt.Errorf("%w: too many groups", ErrFacts)
	}
	for addr, g := range f.Groups {
		if addr == "" || (g.Found && !ids[g.Workspace]) || (g.Found && g.Err != "") {
			return fmt.Errorf("%w: a group of a directory that is not listed", ErrFacts)
		}
	}
	for addr, u := range f.Users {
		if addr == "" || (u.Workspace != "" && !ids[u.Workspace]) || (u.Found && u.Workspace == "" && !u.InDomain) {
			return fmt.Errorf("%w: a user of a directory that is not listed", ErrFacts)
		}
	}
	return nil
}

// IDs are the connected directories' ids, sorted.
func (f *DirectoryFacts) IDs() []string {
	if f == nil {
		return nil
	}
	out := make([]string, 0, len(f.Directories))
	for i := range f.Directories {
		out = append(out, f.Directories[i].ID)
	}
	slices.Sort(out)
	return out
}

type factsKey struct{}

// WithFacts puts the facts a call carries in its context: the console's proxy
// sends them from it, and a module's handler reads them from it.
func WithFacts(ctx context.Context, f *DirectoryFacts) context.Context {
	return context.WithValue(ctx, factsKey{}, f)
}

// FactsFrom is the facts a call carries, nil when it carries none.
func FactsFrom(ctx context.Context) *DirectoryFacts {
	f, _ := ctx.Value(factsKey{}).(*DirectoryFacts)
	return f
}
