package console

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect/v2"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/consoleauthz"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/rpcmount"
	"github.com/truvity/sluis/internal/slackapp"
	"github.com/truvity/sluis/internal/slackroster/connection"
	"github.com/truvity/sluis/internal/slackroster/status"
)

// GetSlackStatus is every Slack workspace the caller may view: the ones the
// policy declares, and any the controller reports on or a connection
// exists for that it no longer does (which only the installation-wide role
// sees, for want of an owner). A workspace's owner and team are the ones its
// connection recorded.
func (c *Service) GetSlackStatus(
	ctx context.Context, caller consolecall.Caller, _ *directoryrosterv1.GetSlackStatusRequest,
) (*directoryrosterv1.GetSlackStatusResponse, error) {
	id, book, err := c.requireAnySlack(ctx, caller, access.RoleViewer)
	if err != nil {
		return nil, err
	}
	dirs, err := c.directories(ctx)
	if err != nil {
		return nil, err
	}
	domains := consoleauthz.OwnerDomains(dirs)
	out := &directoryrosterv1.GetSlackStatusResponse{
		ReportsAvailable:    c.d().SlackStatus != nil,
		ConnectingAvailable: c.d().SlackWorkspaces != nil,
		BotScopes:           slices.Clone(connection.BotScopes),
		RedirectUrl:         c.slackWorkspaceRedirect(caller),
	}
	out.OwnerChoices, out.MayConnectWithoutOwner = consoleauthz.OwnerChoices(id, dirs)

	reports := map[string]string{}
	if c.d().SlackStatus != nil {
		read, err := c.d().SlackStatus.Reports(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
		}
		for key, document := range read {
			if workspace, ok := status.WorkspaceOfKey(key); ok {
				reports[workspace] = document
			}
		}
	}
	connections := map[string]connection.Record(book)
	confirmations := map[string]connection.Confirmation{}
	if c.d().SlackWorkspaces != nil {
		if confirmations, err = c.d().SlackWorkspaces.Confirmations(ctx); err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
		}
	}

	passes := map[string]connection.PassRequest{}
	if c.d().SlackWorkspaces != nil {
		if passes, err = c.d().SlackWorkspaces.PassRequests(ctx); err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
		}
	}

	seen, declared := map[string]bool{}, map[string]bool{}
	for _, workspace := range c.d().Authorizer.Policy().SlackWorkspaceKeys() {
		seen[workspace], declared[workspace] = true, true
	}
	for workspace := range reports {
		seen[workspace] = true
	}
	for workspace := range connections {
		seen[workspace] = true
	}
	now := time.Now()
	for _, workspace := range slices.Sorted(maps.Keys(seen)) {
		// A key nothing declares and nothing is connected for (a stale
		// report) is nobody's to connect: the installation-wide role sees it.
		_, connected := connections[workspace]
		mayView, mayOperate := book.May(id, access.RoleViewer, workspace), book.May(id, access.RoleOperator, workspace)
		if !connected && !declared[workspace] {
			mayView, mayOperate = id.Can(access.RoleViewer), id.Can(access.RoleOperator)
		}
		if !mayView {
			continue
		}
		row := c.slackWorkspaceView(workspace, reports[workspace], confirmations, now)
		row.Declared = declared[workspace]
		if record, connected := connections[workspace]; connected {
			c.slackConnectionView(row, record)
			row.Owner, row.TeamId = record.Owner, record.TeamID
			row.OwnerDomain = domains[record.Owner]
			row.CanChangeOwner = id.Can(access.RoleOperator)
		}
		row.CanOperate = mayOperate
		row.PassRequestedAt = timestampOf(passes[workspace].At)
		out.Workspaces = append(out.Workspaces, row)
	}
	return out, nil
}

// slackConnectionView says where a connection stands, from its record.
func (c *Service) slackConnectionView(row *directoryrosterv1.SlackWorkspaceStatus, record connection.Record) {
	row.ConnectionState = slackCreated
	row.Connection = &directoryrosterv1.SlackConnection{
		AppId: record.AppID, AppSettingsUrl: AppSettingsURL(record.AppID),
		ConnectedAt: timestampOf(record.ConnectedAt), ConnectedBy: record.ConnectedBy,
	}
	row.NeedsConfigurationToken = len(slackapp.MissingScopes(strings.Join(record.ManifestScopes, ","), connection.BotScopes)) > 0
	if !record.Installed() {
		return
	}
	row.ConnectionState = slackInstalled
	row.Connection.BotUserId = record.BotUserID
	row.Connection.GrantedScopes = slices.Clone(record.Scopes)
	if missing := slackapp.MissingScopes(strings.Join(record.Scopes, ","), connection.BotScopes); len(missing) > 0 {
		row.ConnectionState, row.MissingScopes = slackAppScopesMissing, missing
	}
}

// slackWorkspaceView is one workspace's row: its key, and the
// controller's report. A report that cannot be read is shown as a failed
// pass with the reason, not as no report: a page that says nothing about a
// workspace looks like one that has nothing to say.
func (c *Service) slackWorkspaceView(
	workspace, document string, confirmations map[string]connection.Confirmation, now time.Time,
) *directoryrosterv1.SlackWorkspaceStatus {
	row := &directoryrosterv1.SlackWorkspaceStatus{
		Workspace: workspace, ConnectionState: slackNotConnected,
	}
	if document == "" {
		return row
	}
	report, err := status.Decode(document)
	if err != nil {
		row.Reported = true
		row.Tick = &directoryrosterv1.SlackTick{Outcome: string(status.OutcomeFailed), Error: "the controller's report could not be read: " + err.Error()}
		return row
	}
	row.Reported, row.Acting = true, report.Enabled
	row.Tick = &directoryrosterv1.SlackTick{
		At: timestampOf(report.Tick.At), Outcome: string(report.Tick.Outcome), Error: report.Tick.Error,
		Changes: int32(report.Tick.Changes), Held: int32(report.Tick.Held),
		Retrying: int32(report.Tick.Retrying), Waiting: int32(report.Tick.Waiting),
	}
	row.Breaker = slackBreakerView(report.Breaker)
	row.RemovalConfirmation = slackConfirmationView(confirmations, workspace, "", now)
	for i := range report.Leavers {
		leaver := &report.Leavers[i]
		row.Leavers = append(row.Leavers, &directoryrosterv1.SlackLeaver{
			Email: leaver.Email, UserId: leaver.UserID, Channels: slices.Clone(leaver.Channels), Reason: leaver.Reason,
		})
	}
	bound := c.d().Authorizer.Policy().Declared().Slack.Workspaces[workspace].Channels
	for i := range report.Channels {
		ch := &report.Channels[i]
		view := &directoryrosterv1.SlackChannelStatus{
			Name: ch.Name, Id: ch.ID, Private: ch.Private, Mode: ch.Mode, Shared: ch.Shared, Host: ch.Host, Console: ch.Console,
			State: string(ch.State), Reason: ch.Reason, Breaker: slackBreakerView(ch.Breaker),
			RemovalConfirmation: slackConfirmationView(confirmations, workspace, ch.Name, now),
		}
		if binding, ok := bound[ch.Name]; ok && !ch.Console && !ch.Shared {
			view.Sources = slices.Clone(binding.From)
		}
		for j := range ch.Members {
			m := &ch.Members[j]
			view.Members = append(view.Members, &directoryrosterv1.SlackMemberStatus{
				Person: m.Person, Email: m.Email, UserId: m.UserID, State: string(m.State), Action: string(m.Action), Reason: m.Reason,
			})
		}
		row.Channels = append(row.Channels, view)
	}
	return row
}

func slackBreakerView(b *status.Breaker) *directoryrosterv1.SlackBreaker {
	if b == nil {
		return nil
	}
	return &directoryrosterv1.SlackBreaker{
		Affected: int32(b.Affected), Total: int32(b.Total), Fingerprint: b.Fingerprint, Confirmed: b.Confirmed,
	}
}

// slackConfirmationView is the confirmation for a gate, while it has not
// lapsed.
func slackConfirmationView(
	all map[string]connection.Confirmation, workspace, channel string, now time.Time,
) *directoryrosterv1.SlackRemovalConfirmation {
	confirmation, ok := all[connection.ConfirmationKey(workspace, channel)]
	if !ok || !confirmation.Current(now) {
		return nil
	}
	return &directoryrosterv1.SlackRemovalConfirmation{
		Fingerprint: confirmation.Fingerprint, ConfirmedBy: confirmation.By, ConfirmedAt: timestampOf(confirmation.At),
	}
}

// ConfirmSlackRemovals lets exactly the removal set the operator saw go
// ahead. The fingerprint must be the one the latest report shows for that
// gate — the workspace's, or the named channel's: confirming a set that has
// since changed would confirm people nobody looked at.
func (c *Service) ConfirmSlackRemovals(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.ConfirmSlackRemovalsRequest,
) (*directoryrosterv1.ConfirmSlackRemovalsResponse, error) {
	workspace, channel := strings.TrimSpace(req.GetWorkspace()), strings.TrimSpace(req.GetChannel())
	fingerprint := strings.TrimSpace(req.GetFingerprint())
	if _, err := consoleauthz.RequireAnywhere(caller, access.RoleOperator); err != nil {
		return nil, err
	}
	id, err := c.requireSlack(ctx, caller, access.RoleOperator, workspace)
	if err != nil {
		return nil, err
	}
	switch {
	case !status.ValidWorkspace(workspace) || fingerprint == "" || strings.Contains(channel, "."):
		return nil, connect.NewError(connect.CodeInvalidArgument, "a workspace and a fingerprint are required, and a channel name has no dot")
	case c.d().SlackStatus == nil || c.d().SlackWorkspaces == nil:
		return nil, connect.NewError(connect.CodeFailedPrecondition, "this deployment keeps no reports or confirmations")
	}
	reports, err := c.d().SlackStatus.Reports(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	report, err := status.Decode(reports[status.Key(workspace)])
	if err != nil {
		return nil, rpcmount.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s has no readable report: %w", workspace, err))
	}
	gate := report.Breaker
	if channel != "" {
		gate = nil
		for i := range report.Channels {
			if report.Channels[i].Name == channel && report.Channels[i].Breaker != nil {
				gate = report.Channels[i].Breaker
				break
			}
		}
	}
	if gate == nil || gate.Fingerprint != fingerprint {
		return nil, connect.Errorf(connect.CodeFailedPrecondition,
			"the removals in %s have changed since that page was loaded: reload and look again", slackGate(workspace, channel))
	}
	if err = c.d().SlackWorkspaces.PutConfirmation(ctx, connection.Confirmation{
		Workspace: workspace, Channel: channel, Fingerprint: fingerprint, By: id.Who(), At: time.Now().UTC(),
	}); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	c.record(ctx, audit.SlackRemovalsConfirmed(consoleauthz.Actor(id), workspace, channel, fingerprint, gate.Affected))
	c.notify(ctx, port.ControllerSlack, workspace)
	return &directoryrosterv1.ConfirmSlackRemovalsResponse{}, nil
}

func slackGate(workspace, channel string) string {
	if channel == "" {
		return workspace
	}
	return workspace + "/" + channel
}
