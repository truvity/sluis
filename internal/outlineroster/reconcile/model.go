// Package reconcile decides one pass over one Outline workspace: from what
// Outline holds, what the directory says of each person and what the policy
// binds, the rows of the report and the changes to make. It reads nothing and
// writes nothing; the controller observes, asks and applies.
//
// The decisions, in the order a pass makes them:
//
//  1. The workspace: the API key's own user must be an admin, or nothing in
//     the workspace changes ("the API key's user is not an admin").
//  2. People: every account is resolved as the issuer resolves it. An
//     authoritative "not found" or "suspended", or an account that reaches
//     none of the client's required groups, is a leaver and is suspended. A
//     suspended account the directory has back, and that reaches the client,
//     is reactivated. Holders of the admin role's groups are admins and every
//     other account a member. Nothing is removed, demoted or suspended on an
//     answer the directory cannot vouch for.
//  3. Managed groups: one Outline group per internal group the workspace
//     names, created when missing and filled with the people who hold it.
//  4. Collections: created or adopted, each bound group granted its level;
//     a strict collection also loses the grants and the direct members the
//     policy does not name. A collection whose `directory` its groups reach
//     outside is held.
//  5. Breakers: a set of removals, demotions or suspensions past its limit
//     is held until an operator confirms its fingerprint.
//  6. Findings: settings, single-page shares and integrations are reported
//     and never changed.
package reconcile

import (
	"github.com/truvity/sluis/internal/outlineroster/status"
	"github.com/truvity/sluis/policy"
)

// The workspace roles, in Outline's spelling.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleViewer = "viewer"
	RoleGuest  = "guest"
)

// ManagedDescription is the description every managed Outline group carries.
const ManagedDescription = "Managed by sluis from the access policy. Do not edit: changes made here are reverted."

// User is one Outline account.
type User struct {
	ID, Name string
	// Email is the address, lowercase. Empty when Outline did not show one.
	Email     string
	Role      string
	Suspended bool
}

// Group is one Outline group.
type Group struct{ ID, Name string }

// Collection is one Outline collection the key's user can see.
type Collection struct{ ID, Name string }

// Grant is one group's permission on a collection.
type Grant struct{ GroupID, Permission string }

// Membership is one account's direct permission on a collection.
type Membership struct{ UserID, Permission string }

// Settings are the workspace settings a pass reports on.
type Settings struct {
	// Sharing is whether public sharing of documents is allowed.
	Sharing bool
	// MemberCollectionCreate is whether members may create collections.
	MemberCollectionCreate bool
	// MemberInvites is whether members may invite people.
	MemberInvites bool
	// DefaultRole is the role a new account gets.
	DefaultRole string
}

// Share is one person or group a single document is shared with.
type Share struct {
	CollectionID string
	DocumentID   string
	Title        string
	// UserID or GroupID: who it is shared with.
	UserID, GroupID string
	Permission      string
}

// Integration is one integration that posts a collection's changes somewhere.
type Integration struct {
	ID           string
	Service      string
	CollectionID string
	// Team is the chat team it posts to, when Outline says.
	Team string
}

// Observed is what Outline holds, as one pass read it.
type Observed struct {
	// KeyUser is the user the API key belongs to.
	KeyUser  User
	Settings *Settings
	Users    []User
	Groups   []Group
	// Members are the members of each managed group that exists, by the
	// group's id. A managed group that exists and is absent here could not be
	// read whole this pass.
	Members map[string][]string
	// Collections are every collection the key's user can see.
	Collections []Collection
	// Grants and Direct are, by collection id, each bound collection's group
	// grants and direct members. A bound collection absent from either could
	// not be read whole.
	Grants map[string][]Grant
	Direct map[string][]Membership
	// Shares are the single-document shares in the collections that name a
	// directory, when SharesScanned; otherwise the last scan's findings stand.
	Shares        []Share
	SharesScanned bool
	// Integrations are the integrations Outline lists, when it lets the key
	// see them.
	Integrations []Integration
}

// Person is what the directory said of one address, resolved as the issuer
// resolves it.
type Person struct {
	// Asked is false when no answer could be had this pass (an error, or a
	// console under another policy): nothing changes for the person.
	Asked bool
	// Authoritative is whether the directory vouched for the answer. Nothing
	// is removed, demoted or suspended on anything else.
	Authoritative, Found, Suspended bool
	// Groups are the internal groups the address holds now.
	Groups []string
	// Admitted is whether those groups reach the workspace's client.
	Admitted bool
}

// Gone is an address the directory authoritatively no longer has, or has
// suspended.
func (p Person) Gone() bool { return p.Asked && p.Authoritative && (!p.Found || p.Suspended) }

// Directory is one connected directory and the domains it serves.
type Directory struct {
	ID      string
	Primary string
	Domains []string
}

// DirGroup is what the console said of one directory group.
type DirGroup struct {
	Found bool
	// Owner is the workspace id of the directory it belongs to.
	Owner string
}

// Input is everything one pass decides from.
type Input struct {
	Workspace string
	Policy    policy.OutlineWorkspace
	// Groups are the policy's groups, for the `directory` check.
	Groups map[string]policy.Group
	// Client is the sluis client the workspace signs in through; empty
	// checks no client's requirements.
	Client   string
	Observed Observed
	// People are the answers, by lowercase address.
	People map[string]Person
	// Directories are the connected directories, and DirGroups what the
	// console said of each directory group a bound group names, by
	// lowercase address. DirectoryErr is why they could not be read; a
	// collection that names a directory is then held.
	Directories  []Directory
	DirGroups    map[string]DirGroup
	DirectoryErr error
	// Confirmed are the fingerprints confirmed and still current, by
	// breaker scope.
	Confirmed map[string]string
	// Findings are the last scan's share findings, kept when this pass did
	// not scan.
	Findings []status.Finding
}

// Action is one change to Outline.
type Action struct {
	Kind status.Action
	// What it is done to: a group or a collection by name (an id empty when
	// it is created in this pass), and a user.
	Group, GroupID           string
	Collection, CollectionID string
	UserID, Email            string
	// Permission is a grant's level, or a direct membership's.
	Permission string
	// Role is a role change's new role, and From the role before.
	Role, From string
	// Reason says why.
	Reason string
	// Groups are the internal groups the change rests on.
	Groups []string
}

// Decision is one pass's outcome over a workspace: the report and the
// changes. Actions are in the order they must be made: groups created before
// they are filled or granted, collections before they are granted.
type Decision struct {
	Report  status.Workspace
	Actions []Action
}
