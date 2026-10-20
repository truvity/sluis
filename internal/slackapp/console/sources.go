package console

import (
	"context"
	"fmt"
	"slices"

	"connectrpc.com/connect/v2"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/rpcmount"
	"github.com/truvity/sluis/internal/slackroster/reconcile"
)

// checkSources is the console's side of the rule on a channel's sources and
// individual members: every source is a group of a connected directory the
// hub holds a snapshot of and, for an ordinary channel, of the directory that
// OWNS the workspace; every member is an active user of such a directory. A
// Slack Connect channel takes groups and users of any connected directory.
// The controller asks the same again at every pass, because a directory can
// be disconnected and an owner changed after the record is written.
func (c *Service) checkSources(ctx context.Context, sources, members []string, owner string, ordinary bool) error {
	facts := consolecall.FactsFrom(ctx)
	if facts == nil {
		return connect.Errorf(connect.CodeFailedPrecondition, "this deployment reads no directory, so no source can be checked")
	}
	if ordinary && owner == "" {
		return connect.NewError(connect.CodeFailedPrecondition,
			reconcile.NoOwner)
	}
	for _, source := range sources {
		group, ok := facts.Groups[source]
		if !ok {
			return uncovered(source)
		}
		if group.Err != "" {
			return connect.Errorf(connect.CodeInvalidArgument, "source %q is not a group address: %s", source, group.Err)
		}
		switch {
		case !group.Found && facts.Users[source].Found && facts.Users[source].Err == "" && facts.Users[source].Workspace != "":
			return connect.Errorf(connect.CodeInvalidArgument,
				"%s is a person's address, not a group: enter it under Individual addresses", source)
		case !group.Found:
			return connect.Errorf(connect.CodeInvalidArgument,
				"source %s is not a group of a connected directory: pick it from the list", source)
		case ordinary && group.Workspace != owner:
			return rpcmount.NewError(connect.CodeInvalidArgument, fmt.Errorf(
				"source %s belongs to another directory than the one that owns this workspace (%s): "+
					"an ordinary channel is fed by its own directory's groups only; a Slack Connect channel takes any", source, owner))
		}
	}
	return c.checkMembers(facts, members, owner, ordinary)
}

// uncovered is the refusal of an address the facts say nothing of: the console
// sends a fact for every address of the call, so a field without one is not the
// console's, and nothing is taken on trust.
func uncovered(address string) error {
	return connect.Errorf(connect.CodeFailedPrecondition, "the directory facts of this call do not cover %s", address)
}

// checkMembers is the rule on individual addresses: each is an active user
// of the directory that owns the workspace (an ordinary channel) or of any
// connected directory (a Slack Connect channel), and none is a group.
func (c *Service) checkMembers(facts *consolecall.DirectoryFacts, members []string, owner string, ordinary bool) error {
	const where = "individual addresses come from the directories this channel draws from"
	for _, member := range members {
		group, ok := facts.Groups[member]
		if !ok {
			return uncovered(member)
		}
		if group.Err != "" {
			return connect.Errorf(connect.CodeInvalidArgument, "member %q is not an email address: %s", member, group.Err)
		}
		if group.Found {
			return connect.Errorf(connect.CodeInvalidArgument,
				"%s is a group, not a person: enter it under Directory groups", member)
		}
		user, ok := facts.Users[member]
		if !ok {
			return uncovered(member)
		}
		if user.Err != "" {
			return connect.Errorf(connect.CodeInvalidArgument, "member %q is not an email address: %s", member, user.Err)
		}
		dir := "a connected directory"
		if ordinary {
			dir = "the directory that owns this workspace (" + owner + ")"
		}
		switch {
		case user.Workspace == "" || (ordinary && user.Workspace != owner):
			return connect.Errorf(connect.CodeInvalidArgument, "%s is not a user of %s — %s", member, dir, where)
		case !user.Found:
			return connect.Errorf(connect.CodeInvalidArgument,
				"%s is not a user of %s: the directory does not know that address — %s", member, dir, where)
		case user.Suspended:
			return connect.Errorf(connect.CodeInvalidArgument,
				"%s is suspended in %s: only an active user can be listed", member, dir)
		}
	}
	return nil
}

// sourceDirectories are the connected directories and their groups, as a
// picker lists them, sorted. only, when set, keeps the one directory. When the
// groups cannot be read the second result says so, so that the picker shows
// the failure rather than an empty list that reads as "no group exists".
func (c *Service) sourceDirectories(ctx context.Context, only ...string) ([]*directoryrosterv1.SlackSourceDirectory, string) {
	facts := consolecall.FactsFrom(ctx)
	if facts == nil {
		return nil, ""
	}
	if facts.SourcesError != "" {
		return nil, facts.SourcesError
	}
	var out []*directoryrosterv1.SlackSourceDirectory
	for i := range facts.Sources {
		dir := &facts.Sources[i]
		if len(only) > 0 && !slices.Contains(only, dir.Workspace) {
			continue
		}
		view := &directoryrosterv1.SlackSourceDirectory{WorkspaceId: dir.Workspace, Domains: slices.Sorted(slices.Values(dir.Domains))}
		for _, g := range dir.Groups {
			view.Groups = append(view.Groups, &directoryrosterv1.SlackSourceGroup{Email: g.Email, Members: int32(g.Members)}) //nolint:gosec // a membership count
		}
		slices.SortFunc(view.Groups, func(a, b *directoryrosterv1.SlackSourceGroup) int { return compareStrings(a.Email, b.Email) })
		out = append(out, view)
	}
	slices.SortFunc(out, func(a, b *directoryrosterv1.SlackSourceDirectory) int {
		return compareStrings(a.WorkspaceId, b.WorkspaceId)
	})
	return out, ""
}
