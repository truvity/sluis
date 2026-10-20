package console

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"connectrpc.com/connect/v2"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/consoleauthz"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/rpcmount"
	"github.com/truvity/sluis/internal/slackroster/connection"
	"github.com/truvity/sluis/internal/slackroster/reconcile"
	"github.com/truvity/sluis/internal/slackroster/status"
	"github.com/truvity/sluis/policy"
)

// The states a shared channel is shown in.
const (
	sharedNotReported = "not_reported"
	sharedPending     = "pending"
	sharedWaiting     = "waiting"
	sharedActive      = "active"
	sharedHeld        = "held"
	sharedInvalid     = "invalid"
)

// errSharedImmutable is what a change of host or name is answered with.
func errSharedImmutable(what, was, got string) error {
	return rpcmount.NewError(connect.CodeInvalidArgument, fmt.Errorf(
		"a shared channel's %s cannot change (it is %q, the request says %q): create a new channel with the %s you want, "+
			"and delete this record if the old channel should no longer be managed", what, was, got, what))
}

func (c *Service) slackSharedStore() (SharedRecords, error) {
	if c.d().SlackShared == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			"this deployment keeps no state in Kubernetes, so a shared channel's record would not survive a restart")
	}
	return c.d().SlackShared, nil
}

// ListSlackSharedChannels is every record the caller may see, with where
// the controller has each.
func (c *Service) ListSlackSharedChannels(
	ctx context.Context, caller consolecall.Caller, _ *directoryrosterv1.ListSlackSharedChannelsRequest,
) (*directoryrosterv1.ListSlackSharedChannelsResponse, error) {
	id, book, err := c.requireAnySlack(ctx, caller, access.RoleViewer)
	if err != nil {
		return nil, err
	}
	set := c.d().Authorizer.Policy()
	p := set.Declared()
	out := &directoryrosterv1.ListSlackSharedChannelsResponse{Available: c.d().SlackShared != nil}
	anyOperate := false
	for _, key := range set.SlackWorkspaceKeys() {
		if !book.May(id, access.RoleViewer, key) {
			continue
		}
		operate := book.MayAct(id, key)
		anyOperate = anyOperate || operate
		out.Workspaces = append(out.Workspaces, &directoryrosterv1.SlackConnectWorkspace{Key: key, CanOperate: operate, Owner: book.Owner(key)})
	}
	if anyOperate {
		// Any connected directory's groups: a Slack Connect channel is not
		// bound to one directory.
		out.SourceDirectories, out.SourceDirectoriesError = c.sourceDirectories(ctx)
	}
	if c.d().SlackShared == nil {
		return out, nil
	}
	records, err := c.d().SlackShared.List(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	reports := c.slackReports(ctx)
	for i := range records {
		rec := &records[i]
		if rec.Err != nil && !errors.Is(rec.Err, connection.ErrLegacySources) {
			// Whose it is cannot be read: only the installation-wide role
			// sees it, to fix or delete it.
			if id.Can(access.RoleViewer) {
				out.Channels = append(out.Channels, &directoryrosterv1.SlackSharedChannel{
					Channel: &directoryrosterv1.SlackSharedChannelDefinition{Name: rec.Name},
					State:   sharedInvalid, Reason: rec.Err.Error(), CanOperate: id.Can(access.RoleOperator),
				})
			}
			continue
		}
		if !maySeeShared(id, book, rec.Channel) {
			continue
		}
		view := sharedView(rec.Channel)
		view.State, view.Reason = sharedState(rec.Channel, p, reports)
		if rec.Err != nil {
			// Fed by internal groups, as an old record was: it says whose it
			// is, so it is shown to them and can be edited, and acts on nothing.
			view.State, view.Reason = sharedInvalid, rec.Err.Error()
		}
		view.CanOperate = book.MayAct(id, rec.Channel.Host)
		out.Channels = append(out.Channels, view)
	}
	out.Discovered = discoveredChannels(id, book, p, reports, records, true)
	return out, nil
}

// maySeeShared is whether the caller may view the host workspace or any
// workspace the channel is shared with.
func maySeeShared(id access.Identity, book Book, ch reconcile.SharedChannel) bool {
	return slices.ContainsFunc(append([]string{ch.Host}, ch.With...), func(w string) bool {
		return book.May(id, access.RoleViewer, w)
	})
}

// slackReports are the controller's documents by workspace. Unreadable or
// absent is an empty answer: the list then says "not reported".
func (c *Service) slackReports(ctx context.Context) map[string]status.Workspace {
	out := map[string]status.Workspace{}
	if c.d().SlackStatus == nil {
		return out
	}
	raw, err := c.d().SlackStatus.Reports(ctx)
	if err != nil {
		return out
	}
	for key, doc := range raw {
		workspace, ok := status.WorkspaceOfKey(key)
		if !ok {
			continue
		}
		if report, err := status.Decode(doc); err == nil {
			out[workspace] = report
		}
	}
	return out
}

// slackReport is one workspace's latest report, and whether there is one that
// reads: unlike slackReports, an absent or unreadable report is told apart, for
// a caller that must refuse on it.
func (c *Service) slackReport(ctx context.Context, workspace string) (status.Workspace, bool) {
	if c.d().SlackStatus == nil {
		return status.Workspace{}, false
	}
	raw, err := c.d().SlackStatus.Reports(ctx)
	if err != nil {
		return status.Workspace{}, false
	}
	doc, ok := raw[status.Key(workspace)]
	if !ok {
		return status.Workspace{}, false
	}
	report, err := status.Decode(doc)
	if err != nil {
		return status.Workspace{}, false
	}
	return report, true
}

// sharedState is where a record stands: first whether the policy in force
// still accepts it, then what the host's controller reported for it.
func sharedState(ch reconcile.SharedChannel, p policy.Policy, reports map[string]status.Workspace) (state, reason string) {
	if err := ch.Validate(p); err != nil {
		return sharedInvalid, err.Error()
	}
	report, ok := reports[ch.Host]
	if !ok {
		return sharedNotReported, "the host workspace's controller has reported nothing yet"
	}
	for i := range report.Channels {
		rep := &report.Channels[i]
		if rep.Name != ch.Name || !rep.Shared || rep.Host != ch.Host {
			continue
		}
		switch rep.State {
		case status.ChannelOK:
			return sharedActive, ""
		case status.ChannelWaiting:
			return sharedWaiting, orDefault(rep.Reason, "a guest workspace has not accepted the invitation yet")
		case status.ChannelHeld:
			if strings.Contains(rep.Reason, "definition is refused") {
				return sharedInvalid, rep.Reason
			}
			return sharedHeld, rep.Reason
		default:
			return sharedPending, orDefault(rep.Reason, "the controller will act on it on its next pass")
		}
	}
	return sharedNotReported, "the host workspace's report does not list this channel yet"
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func sharedView(ch reconcile.SharedChannel) *directoryrosterv1.SlackSharedChannel {
	def := &directoryrosterv1.SlackSharedChannelDefinition{
		Name: ch.Name, Host: ch.Host, With: slices.Clone(ch.With), From: slices.Clone(ch.Sources), Members: slices.Clone(ch.Members),
		Private: ch.Private.All, ChannelId: ch.ChannelID,
	}
	if len(ch.Private.PerSide) > 0 {
		def.PrivatePerSide = maps.Clone(ch.Private.PerSide)
	}
	return &directoryrosterv1.SlackSharedChannel{Channel: def}
}

// sharedOf is a request's definition as the reconciler's: trimmed, sources
// lowercased and without repeats.
func sharedOf(def *directoryrosterv1.SlackSharedChannelDefinition) reconcile.SharedChannel {
	ch := reconcile.SharedChannel{
		Name: strings.TrimSpace(def.GetName()), Host: strings.TrimSpace(def.GetHost()),
		With: trimmed(def.GetWith(), false), Sources: trimmed(def.GetFrom(), true), Members: addresses(def.GetMembers()),
		ChannelID: strings.TrimSpace(def.GetChannelId()),
	}
	if per := def.GetPrivatePerSide(); len(per) > 0 {
		ch.Private.PerSide = maps.Clone(per)
	} else {
		ch.Private.All = def.GetPrivate()
	}
	return ch
}

// trimmed is a request's list without blanks and repeats; lower folds case,
// for addresses.
func trimmed(in []string, lower bool) []string {
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if lower {
			s = strings.ToLower(s)
		}
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func auditShared(ch reconcile.SharedChannel) audit.SlackSharedChannel {
	return audit.SlackSharedChannel{
		Name: ch.Name, Host: ch.Host, With: ch.With, Sources: ch.Sources, Members: ch.Members, Private: ch.Private.All, PerSide: ch.Private.PerSide,
	}
}

// sharedError is a store's failure as the caller sees it.
func sharedError(err error) error {
	var connectErr *connect.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &connectErr):
		return err
	case errors.Is(err, connection.ErrSharedConflict):
		return connect.NewError(connect.CodeAborted,
			"the shared channel records were changed by someone else while this was written: reload and try again")
	default:
		return connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
}

// CreateSlackSharedChannel defines a channel: operator of the host
// workspace's owner, or of the installation.
func (c *Service) CreateSlackSharedChannel(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.CreateSlackSharedChannelRequest,
) (*directoryrosterv1.CreateSlackSharedChannelResponse, error) {
	id, err := consoleauthz.RequireAnywhere(caller, access.RoleOperator)
	if err != nil {
		return nil, err
	}
	store, err := c.slackSharedStore()
	if err != nil {
		return nil, err
	}
	want := sharedOf(req.GetChannel())
	if _, err = c.requireSlack(ctx, caller, access.RoleOperator, want.Host); err != nil {
		return nil, err
	}
	if err = want.Validate(c.d().Authorizer.Policy().Declared()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	}
	if err = c.checkSources(ctx, want.Sources, want.Members, "", false); err != nil {
		return nil, err
	}
	if err = c.sharedClashesWithConsole(ctx, want); err != nil {
		return nil, err
	}
	if want.ChannelID != "" {
		reports := c.slackReports(ctx)
		seen, found := discoveredIn(reports, want.Host, want.ChannelID)
		switch {
		case !found:
			return nil, rpcmount.NewError(connect.CodeInvalidArgument, fmt.Errorf(
				"the channel %s has not been discovered in the host workspace %s: only a channel its bot can see is taken under management; "+
					"if it is private there, invite the bot and refresh", want.ChannelID, want.Host))
		case seen.HostTeam != "" && reports[want.Host].Team != "" && seen.HostTeam != reports[want.Host].Team:
			return nil, connect.Errorf(connect.CodeInvalidArgument,
				"the channel %s is hosted by another Slack team, not by %s: it can be managed only from a connected workspace that hosts it", want.ChannelID, want.Host)
		}
	}
	err = store.Apply(ctx, want.Name, func(current *reconcile.SharedChannel) (*reconcile.SharedChannel, error) {
		if current != nil {
			// Another host's record is named to a caller only when the caller may see it.
			if book, berr := c.book(ctx); berr == nil && maySeeShared(id, book, *current) {
				return nil, connect.Errorf(connect.CodeAlreadyExists,
					"a shared channel named %s already exists, hosted by %s: edit it, or pick another name", want.Name, current.Host)
			}
			return nil, connect.Errorf(connect.CodeAlreadyExists, "a shared channel named %s already exists: pick another name", want.Name)
		}
		return &want, nil
	})
	if err != nil {
		return nil, sharedError(err)
	}
	c.record(ctx, audit.SlackSharedChannelCreated(consoleauthz.ActorOf(caller), auditShared(want)))
	c.notifyShared(ctx, want)
	view := sharedView(want)
	view.State, view.Reason = sharedState(want, c.d().Authorizer.Policy().Declared(), c.slackReports(ctx))
	view.CanOperate = true
	return &directoryrosterv1.CreateSlackSharedChannelResponse{Channel: view}, nil
}

// UpdateSlackSharedChannel changes with, from (directory groups) and private. The caller's
// role is asked about the STORED host, which a request cannot change.
func (c *Service) UpdateSlackSharedChannel(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.UpdateSlackSharedChannelRequest,
) (*directoryrosterv1.UpdateSlackSharedChannelResponse, error) {
	id, err := consoleauthz.RequireAnywhere(caller, access.RoleOperator)
	if err != nil {
		return nil, err
	}
	store, err := c.slackSharedStore()
	if err != nil {
		return nil, err
	}
	want := sharedOf(req.GetChannel())
	var changes string
	err = store.Apply(ctx, want.Name, func(current *reconcile.SharedChannel) (*reconcile.SharedChannel, error) {
		if err := c.requireStoredShared(ctx, caller, id, want.Name, current); err != nil {
			return nil, err
		}
		if want.Host != current.Host {
			return nil, errSharedImmutable("host", current.Host, want.Host)
		}
		if want.ChannelID != current.ChannelID {
			return nil, errSharedImmutable("channel id", current.ChannelID, want.ChannelID)
		}
		if err := want.Validate(c.d().Authorizer.Policy().Declared()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
		}
		if err := c.checkSources(ctx, want.Sources, want.Members, "", false); err != nil {
			return nil, err
		}
		changes = sharedChanges(*current, want)
		return &want, nil
	})
	if err != nil {
		return nil, sharedError(err)
	}
	if changes != "" {
		c.record(ctx, audit.SlackSharedChannelUpdated(consoleauthz.ActorOf(caller), auditShared(want), changes))
		c.notifyShared(ctx, want)
	}
	view := sharedView(want)
	view.State, view.Reason = sharedState(want, c.d().Authorizer.Policy().Declared(), c.slackReports(ctx))
	view.CanOperate = true
	return &directoryrosterv1.UpdateSlackSharedChannelResponse{Channel: view}, nil
}

// requireStoredShared is the gate on changing a stored record: the role over
// its STORED host, asked before the record's existence is admitted. A caller
// who may not even see the record (a viewer of neither its host nor any guest)
// is answered "no such channel", exactly as for a name nobody holds, so the
// answer cannot be used to find out which names another directory's workspace
// hosts. A caller who may see it but may not operate it is refused plainly.
func (c *Service) requireStoredShared(ctx context.Context, caller consolecall.Caller, id access.Identity, name string, current *reconcile.SharedChannel) error {
	notFound := connect.Errorf(connect.CodeNotFound, "there is no shared channel named %s", name)
	if current == nil {
		return notFound
	}
	book, err := c.book(ctx)
	if err != nil {
		return err
	}
	if !maySeeShared(id, book, *current) {
		return notFound
	}
	_, err = c.requireSlack(ctx, caller, access.RoleOperator, current.Host)
	return err
}

// sourcesChange says how a channel's directory groups changed, by count: the
// groups themselves are the audit record's targets, and an address is never
// carried as data. Empty when they did not change.
func sourcesChange(before, after []string) string {
	if slices.Equal(slices.Sorted(slices.Values(before)), slices.Sorted(slices.Values(after))) {
		return ""
	}
	added, removed := 0, 0
	for _, s := range after {
		if !slices.Contains(before, s) {
			added++
		}
	}
	for _, s := range before {
		if !slices.Contains(after, s) {
			removed++
		}
	}
	return fmt.Sprintf("sources: %d -> %d groups (%d added, %d removed)", len(before), len(after), added, removed)
}

// membersChange says how a channel's individual addresses changed, by count:
// the addresses are the audit record's targets, never data. Empty when they
// did not change.
func membersChange(before, after []string) string {
	if slices.Equal(slices.Sorted(slices.Values(before)), slices.Sorted(slices.Values(after))) {
		return ""
	}
	added, removed := 0, 0
	for _, s := range after {
		if !slices.Contains(before, s) {
			added++
		}
	}
	for _, s := range before {
		if !slices.Contains(after, s) {
			removed++
		}
	}
	return fmt.Sprintf("members: %d -> %d people (%d added, %d removed)", len(before), len(after), added, removed)
}

// addresses is a request's individual addresses, trimmed and lowercased with
// blanks dropped, and with repeats KEPT so that validation refuses them.
func addresses(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// sharedChanges says what an edit changed, as 'field: before -> after'
// parts in a fixed order; empty when nothing did.
func sharedChanges(before, after reconcile.SharedChannel) string {
	var parts []string
	if !slices.Equal(before.With, after.With) {
		parts = append(parts, "with: "+strings.Join(before.With, ",")+" -> "+strings.Join(after.With, ","))
	}
	if part := sourcesChange(before.Sources, after.Sources); part != "" {
		parts = append(parts, part)
	}
	if part := membersChange(before.Members, after.Members); part != "" {
		parts = append(parts, part)
	}
	was, is := auditShared(before).Privacy(), auditShared(after).Privacy()
	if was != is {
		parts = append(parts, "private: "+was+" -> "+is)
	}
	return strings.Join(parts, "; ")
}

// DeleteSlackSharedChannel forgets the record. The channel stays in Slack.
func (c *Service) DeleteSlackSharedChannel(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.DeleteSlackSharedChannelRequest,
) (*directoryrosterv1.DeleteSlackSharedChannelResponse, error) {
	id, err := consoleauthz.RequireAnywhere(caller, access.RoleOperator)
	if err != nil {
		return nil, err
	}
	store, err := c.slackSharedStore()
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	var gone reconcile.SharedChannel
	err = store.Apply(ctx, name, func(current *reconcile.SharedChannel) (*reconcile.SharedChannel, error) {
		if err := c.requireStoredShared(ctx, caller, id, name, current); err != nil {
			return nil, err
		}
		gone = *current
		return nil, nil
	})
	if err != nil {
		return nil, sharedError(err)
	}
	c.record(ctx, audit.SlackSharedChannelDeleted(consoleauthz.ActorOf(caller), auditShared(gone)))
	c.notifyShared(ctx, gone)
	return &directoryrosterv1.DeleteSlackSharedChannelResponse{Note: fmt.Sprintf(
		"The record of %s is deleted. The channel itself stays in Slack, archived by nobody, and the reconciler no longer manages its members: "+
			"people who were added stay until someone removes them in Slack.", name)}, nil
}

// notifyShared notifies every workspace a shared channel's record names: the
// host, which invites, and each guest, which accepts.
func (c *Service) notifyShared(ctx context.Context, ch reconcile.SharedChannel) {
	c.notify(ctx, port.ControllerSlack, ch.Host)
	for _, guest := range ch.With {
		c.notify(ctx, port.ControllerSlack, guest)
	}
}
