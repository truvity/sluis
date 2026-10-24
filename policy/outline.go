package policy

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Outline binds internal groups to an Outline wiki's workspace role and to
// its collections. Like [Slack] it grants nothing here and appears in no
// token: the Outline controller reads it and makes each workspace match.
//
// Outline's own group sync reads groups from Google's or Microsoft's API and
// never from an OIDC claim, so sluis keeps the groups instead: one Outline
// group per internal group a workspace names, filled with the people who hold
// it, and granted to collections at the level the policy says. The
// controller is then the only writer of permissions in that Outline.
//
// Only a workspace's key and what is bound in it are policy. Where the
// workspace is (its URL) and the credential the controller acts with are a
// connection record on the console (docs/decisions/0018).
type Outline struct {
	// Workspaces are the Outline installations, keyed by a name WE choose,
	// with the same rules as a Slack workspace key.
	Workspaces map[string]OutlineWorkspace `yaml:"workspaces,omitempty"`
}

// OutlineWorkspace is one Outline workspace and what is bound in it.
type OutlineWorkspace struct {
	// Client is the id of the sluis client the workspace signs in through.
	// Optional: when set, an account that holds none of the groups the
	// client requires is suspended (the issuer would refuse it a session
	// anyway), and a suspended one that holds one again is reactivated.
	// Unset, the workspace's own bound groups are what an account must hold.
	Client string `yaml:"client,omitempty"`
	// Roles are the workspace roles the policy gives. Every other managed
	// user is a member.
	Roles OutlineRoles `yaml:"roles,omitempty"`
	// Ignore lists addresses the controller never demotes, removes or
	// suspends: the owner of the API key among them, since Outline has no
	// service accounts and the key belongs to a person's admin account.
	// Merged by union across files.
	Ignore []string `yaml:"ignore,omitempty"`
	// Collections are the collections bound in this workspace, keyed by
	// NAME as Outline spells it. The controller creates a missing one and
	// otherwise adopts the existing one by name; [OutlineCollection.Adopt]
	// is only a disambiguation.
	Collections map[string]OutlineCollection `yaml:"collections,omitempty"`
}

// OutlineRoles are a workspace's roles.
type OutlineRoles struct {
	// Admin are the internal groups whose holders are workspace admins.
	Admin []string `yaml:"admin,omitempty"`
}

// OutlineCollection is one collection's binding: the internal groups granted
// each of Outline's three collection permissions.
type OutlineCollection struct {
	// Mode is `extend` (the default), which only adds grants, or `strict`,
	// which also removes the direct user members and the group grants the
	// policy does not name.
	Mode string `yaml:"mode,omitempty"`
	// Directory, when set, is the connected directory every bound group's
	// members must come from, by its primary domain (`example.com`). It is
	// checked every pass, and a collection whose groups reach outside it is
	// held, so one misplaced line cannot open a company's collection to
	// another company.
	Directory string `yaml:"directory,omitempty"`
	// Adopt is the id of an existing collection to adopt: for a renamed
	// collection, or when two collections carry the declared name.
	Adopt string `yaml:"adopt,omitempty"`
	// Read, ReadWrite and Admin are the internal groups granted Outline's
	// `read`, `read_write` and `admin` (the UI's "Manage") on the
	// collection. A group at two levels takes the higher.
	Read      []string `yaml:"read,omitempty"`
	ReadWrite []string `yaml:"read_write,omitempty"`
	Admin     []string `yaml:"admin,omitempty"`
}

// The collection modes.
const (
	// OutlineModeExtend only adds grants. It is the default.
	OutlineModeExtend = "extend"
	// OutlineModeStrict adds and removes.
	OutlineModeStrict = "strict"
)

// The collection permissions, in Outline's spelling, lowest first.
const (
	OutlineRead      = "read"
	OutlineReadWrite = "read_write"
	OutlineAdmin     = "admin"
)

// OutlineRank orders the collection permissions: 1 for read, 2 for
// read_write, 3 for admin, and 0 for anything else.
func OutlineRank(permission string) int {
	switch permission {
	case OutlineRead:
		return 1
	case OutlineReadWrite:
		return 2
	case OutlineAdmin:
		return 3
	}
	return 0
}

// Strict reports whether the collection's grants are exact.
func (c OutlineCollection) Strict() bool { return c.Mode == OutlineModeStrict }

// Grants are the bound groups and the permission each is granted: the
// highest level that lists it.
func (c OutlineCollection) Grants() map[string]string {
	out := map[string]string{}
	for _, level := range []struct {
		permission string
		groups     []string
	}{{OutlineRead, c.Read}, {OutlineReadWrite, c.ReadWrite}, {OutlineAdmin, c.Admin}} {
		for _, group := range level.groups {
			if OutlineRank(level.permission) > OutlineRank(out[group]) {
				out[group] = level.permission
			}
		}
	}
	return out
}

// Groups are every internal group the workspace names: its admin role's and
// every collection's, sorted, each once. Each is one managed Outline group.
func (w OutlineWorkspace) Groups() []string {
	var out []string
	out = append(out, w.Roles.Admin...)
	for _, c := range w.Collections {
		out = append(out, slices.Collect(maps.Keys(c.Grants()))...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Ignored reports whether an address is on the workspace's ignore list.
func (w OutlineWorkspace) Ignored(address string) bool {
	address, ok := normaliseAddress(address)
	if !ok {
		return false
	}
	for _, entry := range w.Ignore {
		if norm, ok := normaliseAddress(entry); ok && norm == address {
			return true
		}
	}
	return false
}

var (
	// outlineCollectionID is the shape of an Outline collection id: a UUID.
	outlineCollectionID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// outlineDirectory is what a collection's `directory` may be: a
	// directory's primary domain, lowercase, with at least one dot.
	outlineDirectory = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

// outlineMaxName is Outline's own limit on a collection's name.
const outlineMaxName = 100

// ValidOutlineCollectionID reports whether a string has the shape of an
// Outline collection id.
func ValidOutlineCollectionID(id string) bool { return outlineCollectionID.MatchString(id) }

// validOutlineCollectionName is Outline's rule for a name, and ours: not
// empty, at most 100 characters, no surrounding space and no control
// character, since the name is how the collection is found.
func validOutlineCollectionName(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || !utf8.ValidString(name) || utf8.RuneCountInString(name) > outlineMaxName {
		return false
	}
	return !strings.ContainsFunc(name, unicode.IsControl)
}

// validateOutline checks the whole `outline` block.
func (p Policy) validateOutline() error {
	for _, key := range slices.Sorted(maps.Keys(p.Outline.Workspaces)) {
		if err := p.validateOutlineWorkspace(key); err != nil {
			return err
		}
	}
	return nil
}

func (p Policy) validateOutlineWorkspace(key string) error {
	if !slackSlug.MatchString(key) {
		return fmt.Errorf("outline: %q is not a usable workspace key (lowercase letters, digits and '-', at most 40)", key)
	}
	ws := p.Outline.Workspaces[key]
	if ws.Client != "" {
		if _, declared := p.Clients[ws.Client]; !declared {
			return fmt.Errorf("outline: %s client: %q is not a declared client", key, ws.Client)
		}
	}
	if err := p.checkBoundGroups(fmt.Sprintf("outline: %s roles admin", key), ws.Roles.Admin); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, entry := range ws.Ignore {
		address, ok := normaliseAddress(entry)
		if !ok {
			return fmt.Errorf("outline: %s ignore: %q is not an address", key, entry)
		}
		if seen[address] {
			return fmt.Errorf("outline: %s ignore lists %s twice", key, address)
		}
		seen[address] = true
	}
	adopted := map[string]string{}
	folded := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(ws.Collections)) {
		where := fmt.Sprintf("outline: %s/%s", key, name)
		if !validOutlineCollectionName(name) {
			return fmt.Errorf("outline: %s: %q is not a collection name (1 to %d characters, no surrounding space or control characters)",
				key, name, outlineMaxName)
		}
		// Outline does not keep names unique, and two names that differ only
		// in case are one name to a reader: refused, so "which one" never
		// depends on how Outline happens to sort.
		if prev, dup := folded[strings.ToLower(name)]; dup {
			return fmt.Errorf("outline: %s: collection %s is declared twice (as %q and %q)", key, name, prev, name)
		}
		folded[strings.ToLower(name)] = name
		c := ws.Collections[name]
		grants := c.Grants()
		if len(grants) == 0 {
			return fmt.Errorf("%s names no group at any level (read, read_write or admin)", where)
		}
		for _, level := range []struct {
			name   string
			groups []string
		}{{"read", c.Read}, {"read_write", c.ReadWrite}, {"admin", c.Admin}} {
			if err := p.checkBoundGroups(where+" "+level.name, level.groups); err != nil {
				return err
			}
		}
		switch c.Mode {
		case "", OutlineModeExtend, OutlineModeStrict:
		default:
			return fmt.Errorf("%s: mode %q is neither extend nor strict", where, c.Mode)
		}
		if c.Directory != "" && !outlineDirectory.MatchString(c.Directory) {
			return fmt.Errorf("%s: directory %q is not a primary domain (lowercase, like example.com)", where, c.Directory)
		}
		if c.Adopt == "" {
			continue
		}
		if !ValidOutlineCollectionID(c.Adopt) {
			return fmt.Errorf("%s adopt: %q is not an Outline collection id (a UUID)", where, c.Adopt)
		}
		if prev, dup := adopted[c.Adopt]; dup {
			return fmt.Errorf("outline: %s adopts %s for both %s and %s", key, c.Adopt, prev, name)
		}
		adopted[c.Adopt] = name
	}
	return nil
}

// mergeOutline folds another file's `outline` block into p's, per workspace
// and field by field: the admin role is declared once, a collection is
// declared by one file and a repeat is a clash, and the ignore lists are
// joined, since leaving two people alone leaves both alone.
func (p *Policy) mergeOutline(other Outline, from string) error {
	for _, key := range slices.Sorted(maps.Keys(other.Workspaces)) {
		incoming, into := other.Workspaces[key], p.Outline.Workspaces[key]
		if incoming.Client != "" {
			if into.Client != "" {
				return fmt.Errorf("%s: outline workspace %s declares client twice", from, key)
			}
			into.Client = incoming.Client
		}
		if len(incoming.Roles.Admin) > 0 {
			if len(into.Roles.Admin) > 0 {
				return fmt.Errorf("%s: outline workspace %s declares roles.admin twice", from, key)
			}
			into.Roles.Admin = slices.Clone(incoming.Roles.Admin)
		}
		for _, entry := range incoming.Ignore {
			if !slices.Contains(into.Ignore, entry) {
				into.Ignore = append(into.Ignore, entry)
			}
		}
		if err := mergeTable(&into.Collections, incoming.Collections, func(name string) error {
			return fmt.Errorf("%s: outline collection %s/%s is declared twice", from, key, name)
		}); err != nil {
			return err
		}
		if p.Outline.Workspaces == nil {
			p.Outline.Workspaces = map[string]OutlineWorkspace{}
		}
		p.Outline.Workspaces[key] = into
	}
	return nil
}

// OutlineWorkspaceDeclared reports whether the policy names an Outline
// workspace key.
func (s *Set) OutlineWorkspaceDeclared(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, declared := s.declared.Outline.Workspaces[key]
	return declared
}

// OutlineWorkspaceKeys returns every declared Outline workspace key, sorted.
func (s *Set) OutlineWorkspaceKeys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Sorted(maps.Keys(s.declared.Outline.Workspaces))
}
