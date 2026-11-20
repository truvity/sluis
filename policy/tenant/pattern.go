package tenant

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/truvity/sluis/policy"
)

const wildcard = "*"

var awsAccountID = regexp.MustCompile(`^\d{12}$`)

// MatchGroup reports whether a group matches a ceiling pattern. Both are
// `scope:thing:role`, where a thing is one word or three
// (`<vendor>:<project>:<customer>`), see [policy.SplitGroup]. A `*` in the
// pattern stands for one whole segment, never part of one, and never for the
// installation-wide scope ([policy.ScopeAll]): reaching it takes a pattern
// that names it. A pattern's thing may be a single `*`, which stands for any
// thing, one word or three; a pattern that writes the three words
// (`finttech:*:ie`) matches the groups of that shape only. A pattern that is
// malformed, or a group that is not a group, matches nothing.
func MatchGroup(pattern, group string) bool {
	if strings.Contains(group, wildcard) {
		return false
	}

	p, ok := splitPattern(pattern)
	if !ok {
		return false
	}

	scope, thing, role, ok := policy.SplitGroup(group)
	if !ok {
		return false
	}

	if !matchSegment(p.scope, scope) || !matchSegment(p.role, role) {
		return false
	}

	if p.scope == wildcard && scope == policy.ScopeAll {
		return false
	}

	if len(p.thing) == 1 && p.thing[0] == wildcard {
		return true
	}

	words := strings.Split(thing, policy.Separator)
	if len(words) != len(p.thing) {
		return false
	}

	for i := range words {
		if !matchSegment(p.thing[i], words[i]) {
			return false
		}
	}

	return true
}

// matchSegment reports whether one segment of a pattern admits one segment of
// a group: the same word, or a `*`.
func matchSegment(pattern, segment string) bool { return pattern == wildcard || pattern == segment }

// groupPattern is a well-formed ceiling pattern taken apart: the scope and role
// are one segment each, the thing one segment or three.
type groupPattern struct {
	scope, role string
	thing       []string
}

// splitPattern reads a well-formed pattern: three segments, or five, none
// empty, a `*` only as a whole segment.
func splitPattern(text string) (groupPattern, bool) {
	segments := strings.Split(text, policy.Separator)
	if len(segments) != 3 && len(segments) != 5 {
		return groupPattern{}, false
	}

	for _, seg := range segments {
		if seg == "" || (seg != wildcard && strings.Contains(seg, wildcard)) {
			return groupPattern{}, false
		}
	}

	last := len(segments) - 1

	return groupPattern{scope: segments[0], thing: segments[1:last], role: segments[last]}, true
}

// matchesAny reports whether a group matches one of the patterns.
func matchesAny(patterns []string, group string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool { return MatchGroup(p, group) })
}

// Validate refuses a ceiling a loader should not accept: a group pattern
// that is not `scope:thing:role` (three segments or five) with whole-segment wildcards, a directory
// or zone that is not a bare domain, an AWS account that is not 12 digits,
// a client pattern that path.Match cannot read, an App permission that is
// not read, write or admin, or an empty entry. The zero ceiling is valid
// and allows nothing. [Check] needs no call to Validate: a malformed
// pattern matches nothing there.
func (c Ceiling) Validate() error {
	var errs []error

	for _, p := range c.Groups {
		if _, ok := splitPattern(p); !ok {
			errs = append(errs, fmt.Errorf("groups: %q is not scope:thing:role, three segments or five, with * for a whole segment", p))
		}
	}

	for _, d := range c.Directories {
		if d == "" || strings.ContainsAny(d, "@* \t") {
			errs = append(errs, fmt.Errorf("directories: %q is not a bare email domain", d))
		}
	}

	for _, a := range c.AWSAccounts {
		if !awsAccountID.MatchString(a) {
			errs = append(errs, fmt.Errorf("aws_accounts: %q is not a 12-digit account id", a))
		}
	}

	for _, z := range c.Zones {
		if !bareDomain(z) {
			errs = append(errs, fmt.Errorf("zones: %q is not a bare DNS name", z))
		}
	}

	for _, p := range c.Clients {
		if _, err := path.Match(p, ""); err != nil || p == "" {
			errs = append(errs, fmt.Errorf("clients: %q is not a path.Match pattern", p))
		}
	}

	for _, app := range sortedKeys(c.GitHubApps) {
		if app == "" {
			errs = append(errs, errors.New("github_apps: an App id is empty"))
		}

		for _, perm := range sortedKeys(c.GitHubApps[app]) {
			if _, ok := permissionLevels[c.GitHubApps[app][perm]]; !ok || perm == "" {
				errs = append(errs, fmt.Errorf("github_apps[%s]: %q: %q is not read, write or admin", app, perm, c.GitHubApps[app][perm]))
			}
		}
	}

	for name, list := range map[string][]string{
		"rungs": c.Rungs, "github_orgs": c.GitHubOrgs, "slack_workspaces": c.SlackWorkspaces, "clusters": c.Clusters,
	} {
		if slices.Contains(list, "") {
			errs = append(errs, fmt.Errorf("%s: an entry is empty", name))
		}
	}

	if err := c.Org.Validate(); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// Scopes lists, sorted and without repeats, the scopes the group patterns
// name. A pattern with a wildcard scope names none, so the list is what a
// network-level destination ceiling can be compared against: a scope in it
// is one the owner is expected to reach.
func (c Ceiling) Scopes() []string {
	var out []string

	for _, pattern := range c.Groups {
		p, ok := splitPattern(pattern)
		if !ok || p.scope == wildcard {
			continue
		}

		out = append(out, p.scope)
	}

	slices.Sort(out)

	return slices.Compact(out)
}

// permissionLevels orders GitHub's permission levels: each includes the ones
// before it.
var permissionLevels = map[string]int{"read": 1, "write": 2, "admin": 3}

// covers reports whether a permission held at level have includes one asked
// for at level want. An unknown level covers and is covered by nothing.
func covers(have, want string) bool {
	h, w := permissionLevels[have], permissionLevels[want]

	return h > 0 && w > 0 && h >= w
}

// bareDomain reports whether name is a DNS name with no scheme, port, path,
// wildcard or trailing dot: `acme.example`.
func bareDomain(name string) bool {
	if name == "" || strings.HasSuffix(name, ".") || strings.HasPrefix(name, ".") {
		return false
	}

	return !strings.ContainsAny(name, "@*/: \t")
}

// inZone reports whether host is one of the zones or a name under one.
func inZone(host string, zones []string) bool {
	host = strings.ToLower(host)

	for _, z := range zones {
		z = strings.ToLower(z)
		if host == z || strings.HasSuffix(host, "."+z) {
			return true
		}
	}

	return false
}
