package console

import (
	"context"
	"maps"
	"slices"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/githubroster/link"
	"github.com/truvity/sluis/internal/githubroster/runnerapp"
	"github.com/truvity/sluis/internal/githubroster/status"
)

// GetGitHubStatus implements the GitHub page: every organisation the
// policy binds or the controller reports on, its bindings beside its
// report.
//
// The join happens here rather than in the browser so that the page has
// one answer to render. Bindings come from the policy in force and
// reports from the controller's ConfigMap; neither is authoritative for
// the other, and the page shows where they disagree — a bound team the
// controller has not reported, a report for a team the policy no longer
// binds — rather than hiding either side.
//
// A viewer of some directory sees the organisations that directory owns
// (the owner recorded when each was connected) and no others; the installation-wide
// viewer sees all. A report names the members of every bound team, which
// is more than a scoped viewer may see of another company. The link App
// and every link are people's across all organisations, so they are the
// installation-wide viewer's alone.
func (c *Service) GetGitHubStatus(
	ctx context.Context, _ *connect.Request[directoryrosterv1.GetGitHubStatusRequest],
) (*connect.Response[directoryrosterv1.GetGitHubStatusResponse], error) {
	id, err := c.requireAnyOrg(ctx, access.RoleViewer)
	if err != nil {
		return nil, err
	}
	out := &directoryrosterv1.GetGitHubStatusResponse{
		ReportsAvailable:    c.deps.Reports != nil,
		ConnectingAvailable: c.deps.Orgs != nil,
	}

	reports := map[string]string{}
	if c.deps.Reports != nil {
		read, err := c.deps.Reports.Reports(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		for key, document := range read {
			if org, ok := status.OrgOfKey(key); ok {
				reports[org] = document
			}
		}
	}

	connections := map[string]connection.Record{}
	if c.deps.Orgs != nil {
		records, err := c.deps.Orgs.List(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		for i := range records {
			connections[records[i].Org] = records[i]
		}
	}

	passes := map[string]connection.PassRequest{}
	if c.deps.Orgs != nil {
		read, err := c.deps.Orgs.PassRequests(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		passes = read
	}

	confirmations := map[string]connection.Confirmation{}
	if c.deps.Confirmations != nil {
		read, err := c.deps.Confirmations.Confirmations(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		confirmations = read
	}

	set := c.deps.Policy()
	bound := boundOrganisations(set)
	seen := map[string]bool{}
	for org := range bound {
		seen[org] = true
	}
	for org := range reports {
		seen[org] = true
	}
	for org := range connections {
		seen[org] = true
	}

	for _, org := range slices.Sorted(maps.Keys(seen)) {
		row := organisationProto(org, bound[org], reports[org])
		if record, connected := connections[org]; connected {
			row.Connection = connectionProto(record)
		}
		row.PassRequestedAt = timestampOf(passes[org].At)
		if confirmation, ok := confirmations[org]; ok && confirmation.Current(time.Now()) {
			row.RemovalConfirmation = &directoryrosterv1.GitHubRemovalConfirmation{
				Fingerprint: confirmation.Fingerprint, ConfirmedBy: confirmation.By, ConfirmedAt: timestampOf(confirmation.At),
			}
		}
		out.Organisations = append(out.Organisations, row)
	}
	owners := make(map[string]string, len(connections))
	for org := range connections {
		owners[org] = connections[org].Owner
	}
	dirs, err := c.directories(ctx)
	if err != nil {
		return nil, err
	}
	out.OwnerChoices, out.MayConnectWithoutOwner = ownerChoices(id, dirs)
	out.Organisations = visibleOrganisations(id, owners, ownerDomains(dirs), out.Organisations)
	if id.Can(access.RoleViewer) {
		if err = c.linkStatus(ctx, out); err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
	}
	if err = c.runnerStatus(ctx, out); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if err = c.catalogueStatus(ctx, out); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	out.RunnerApps = slices.DeleteFunc(out.RunnerApps, func(app *directoryrosterv1.GitHubRunnerApp) bool {
		return !mayOwned(id, access.RoleViewer, owners[app.GetOrg()])
	})
	out.CatalogueApps = slices.DeleteFunc(out.CatalogueApps, func(app *directoryrosterv1.GitHubCatalogueApp) bool {
		return !mayOwned(id, access.RoleViewer, owners[app.GetOrg()])
	})
	return connect.NewResponse(out), nil
}

// linkStatus adds the link App and every link, never with a token.
func (c *Service) linkStatus(ctx context.Context, out *directoryrosterv1.GetGitHubStatusResponse) error {
	if c.deps.LinkApp == nil || c.deps.Links == nil {
		return nil
	}
	out.LinkingAvailable = true
	out.LinkUrl = c.githubRoot(ctx) + consolewire.LinkPath
	app, connected, err := c.deps.LinkApp.LinkApp(ctx)
	if err != nil {
		return err
	}
	if connected {
		out.LinkApp = &directoryrosterv1.GitHubLinkApp{
			AppId: app.AppID, AppSlug: app.AppSlug, Owner: app.Owner, HtmlUrl: app.HTMLURL, ConnectedBy: app.ConnectedBy,
		}
		if !app.ConnectedAt.IsZero() {
			out.LinkApp.ConnectedAt = timestamppb.New(app.ConnectedAt)
		}
	}
	links, err := c.deps.Links.List(ctx)
	if err != nil {
		return err
	}
	for k := range links {
		l := links[k].Public()
		out.Links = append(out.Links, linkProto(&l))
	}
	return nil
}

// linkProto is a link as the page shows it. Pass it a Public link: this
// copies no token, and the caller never hands it one either.
func linkProto(l *link.Link) *directoryrosterv1.GitHubLink {
	source := l.Source
	if source == "" {
		source = link.SourceSelf
	}
	return &directoryrosterv1.GitHubLink{
		AccountId: l.ID, Login: l.Login, Emails: l.Emails, State: string(l.State), Reason: l.Reason,
		LinkedAt: timestampOf(l.LinkedAt), CheckedAt: timestampOf(l.CheckedAt), ChangedAt: timestampOf(l.ChangedAt),
		Source: string(source), Note: l.Note,
	}
}

// connectionProto is a record as the page shows it: never the key, which
// is not in a record to begin with.
func connectionProto(record connection.Record) *directoryrosterv1.GitHubConnection {
	out := &directoryrosterv1.GitHubConnection{
		AppId:       record.AppID,
		AppSlug:     record.AppSlug,
		Installed:   record.Installed(),
		HtmlUrl:     record.HTMLURL,
		ConnectedBy: record.ConnectedBy,
	}
	if !record.ConnectedAt.IsZero() {
		out.ConnectedAt = timestamppb.New(record.ConnectedAt)
	}
	return out
}

// organisationProto joins one organisation's binding, which may be nil,
// with its report, which may be empty.
func organisationProto(org string, bound *binding, document string) *directoryrosterv1.GitHubOrganisation {
	out := &directoryrosterv1.GitHubOrganisation{Org: org, Bound: bound != nil}
	if bound != nil {
		out.MemberGroups = bound.members
		out.Ignored = bound.ignore
	}

	var report status.Org
	if document != "" {
		out.Reported = true
		decoded, err := status.Decode(document)
		if err != nil {
			// The bindings still show. A report the console cannot read is
			// a fact about the report, not a reason to hide what the policy
			// says the organisation should look like.
			out.ReportError = err.Error()
		} else {
			report = decoded
		}
	}
	if out.GetReportError() == "" && out.GetReported() {
		out.Enabled = report.Enabled
		out.Tick = &directoryrosterv1.GitHubTick{
			Outcome:  string(report.Tick.Outcome),
			Error:    report.Tick.Error,
			Changes:  int32(report.Tick.Changes),  //nolint:gosec // a tick's action count never overflows
			Held:     int32(report.Tick.Held),     //nolint:gosec // nor its held count
			Waiting:  int32(report.Tick.Waiting),  //nolint:gosec // nor its waiting count
			Retrying: int32(report.Tick.Retrying), //nolint:gosec // nor its retrying count
		}
		if !report.Tick.At.IsZero() {
			out.Tick.At = timestamppb.New(report.Tick.At)
		}
		out.Members = membersProto(report.Members)
		for _, account := range report.Unlinked {
			out.Unlinked = append(out.Unlinked, &directoryrosterv1.GitHubAccount{
				Login: account.Login, Reason: account.Reason,
			})
		}
		for _, account := range report.OutsideCollaborators {
			out.OutsideCollaborators = append(out.OutsideCollaborators, &directoryrosterv1.GitHubAccount{
				Login: account.Login, Reason: account.Reason,
			})
		}
		if seats := report.Seats; seats != nil {
			out.Seats = &directoryrosterv1.GitHubSeats{
				Known: seats.Known, Total: int32(seats.Total), Filled: int32(seats.Filled), //nolint:gosec // seat counts are small
				Pending: int32(seats.Pending), Free: int32(seats.Free), Short: int32(seats.Short), //nolint:gosec // likewise
			}
		}
		if breaker := report.Breaker; breaker != nil {
			out.Breaker = &directoryrosterv1.GitHubBreaker{
				Affected: int32(breaker.Affected), Members: int32(breaker.Members), //nolint:gosec // member counts are small
				Fingerprint: breaker.Fingerprint, Confirmed: breaker.Confirmed,
			}
		}
	}

	reported := map[string][]status.Member{}
	for _, team := range report.Teams {
		reported[team.Team] = team.Members
	}
	teams := map[string]bool{}
	if bound != nil {
		for team := range bound.teams {
			teams[team] = true
		}
	}
	for team := range reported {
		teams[team] = true
	}
	for _, team := range slices.Sorted(maps.Keys(teams)) {
		row := &directoryrosterv1.GitHubTeamStatus{Team: team, Members: membersProto(reported[team])}
		if bound != nil {
			if view, ok := bound.teams[team]; ok {
				row.Bound = true
				row.MemberGroups = view.Members
				row.MaintainerGroups = view.Maintainers
			}
		}
		out.Teams = append(out.Teams, row)
	}
	return out
}

func membersProto(members []status.Member) []*directoryrosterv1.GitHubMember {
	out := make([]*directoryrosterv1.GitHubMember, 0, len(members))
	for _, member := range members {
		out = append(out, &directoryrosterv1.GitHubMember{
			Email:  member.Email,
			Login:  member.Login,
			Role:   string(member.Role),
			State:  string(member.State),
			Action: string(member.Action),
			Reason: member.Reason,
		})
	}
	return out
}

// The states a catalogue App is shown in.
const (
	catalogueNotCreated = "not_created"
	catalogueCreated    = "created"
	catalogueInstalled  = "installed"
	catalogueDrifted    = "drifted"
)

// catalogueStatus adds every declared App and every App created from an
// entry no longer declared, never with a key.
//
// The Apps themselves are [Console.githubAppView]'s, turned back into the
// shape GetGitHubStatus has always carried: one reading of an App, two
// ways of saying it, for as long as both calls are served.
func (c *Service) catalogueStatus(ctx context.Context, out *directoryrosterv1.GetGitHubStatusResponse) error {
	if c.deps.CatalogueApps == nil {
		return nil
	}
	out.CatalogueAvailable = true
	specs, err := c.catalogueAppSpecs(ctx, map[string]bool{})
	if err != nil {
		return err
	}
	views := c.githubAppViews(ctx, specs)
	for i := range specs {
		out.CatalogueApps = append(out.CatalogueApps, legacyCatalogueApp(specs[i], views[i]))
	}
	return nil
}

// legacyCatalogueApp is one App in the shape GetGitHubStatus and
// CheckGitHubCatalogueApp answer in.
func legacyCatalogueApp(spec githubAppSpec, app *directoryrosterv1.GitHubApp) *directoryrosterv1.GitHubCatalogueApp {
	return &directoryrosterv1.GitHubCatalogueApp{
		Id: app.GetId(), Org: app.GetOrg(), Name: spec.declaredName(), Description: app.GetDescription(), Public: app.GetPublic(),
		Installation: app.GetInstallation(), Permissions: app.GetPermissions(), Events: app.GetEvents(), Grants: app.GetGrants(),
		State: legacyState(app.GetState()), HtmlUrl: app.GetHtmlUrl(), Drift: app.GetDrift(), Reason: app.GetReason(),
		AppId: app.GetAppId(), AppSlug: app.GetAppSlug(), InstallationId: app.GetInstallationId(),
		ConnectedAt: app.GetConnectedAt(), ConnectedBy: app.GetConnectedBy(), CheckedAt: app.GetCheckedAt(),
		RepositorySelection: app.GetRepositorySelection(), Declared: app.GetDeclared(),
	}
}

func legacyState(state directoryrosterv1.AppState) string {
	switch state {
	case appCreated:
		return catalogueCreated
	case appInstalled:
		return catalogueInstalled
	case appDrifted:
		return catalogueDrifted
	default:
		return catalogueNotCreated
	}
}

// runnerApp finds one runner App's record.
func (c *Service) runnerApp(ctx context.Context, tier, org string) (runnerapp.Record, bool, error) {
	records, err := c.deps.RunnerApps.List(ctx)
	if err != nil {
		return runnerapp.Record{}, false, err
	}
	for i := range records {
		if records[i].Tier == tier && records[i].Org == org {
			return records[i], true, nil
		}
	}
	return runnerapp.Record{}, false, nil
}

// runnerStatus adds the declared tiers and every runner App, never a key.
// An App whose tier the deployment no longer declares is still listed, so
// it can be disconnected.
func (c *Service) runnerStatus(ctx context.Context, out *directoryrosterv1.GetGitHubStatusResponse) error {
	if c.deps.RunnerApps == nil {
		return nil
	}
	out.RunnerTiers = slices.Clone(c.deps.RunnerTiers)
	records, err := c.deps.RunnerApps.List(ctx)
	if err != nil {
		return err
	}
	for i := range records {
		record := &records[i]
		out.RunnerApps = append(out.RunnerApps, &directoryrosterv1.GitHubRunnerApp{
			Org: record.Org, Tier: record.Tier, AppId: record.AppID, AppSlug: record.AppSlug,
			Installed: record.Installed(), HtmlUrl: record.HTMLURL,
			ConnectedAt: timestampOf(record.ConnectedAt), ConnectedBy: record.ConnectedBy,
		})
	}
	return nil
}
