package console

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect/v2"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/githubroster/link"
	"github.com/truvity/sluis/internal/githubroster/status"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/rpcmount"
)

// importLimit bounds one import. The records this exists for number in the
// tens.
const importLimit = 500

// ConfirmGitHubRemovals lets exactly the removal set the operator saw go
// ahead. The fingerprint must be the one the organisation's latest report
// shows: confirming a set that has since changed would confirm people
// nobody looked at.
func (c *Service) ConfirmGitHubRemovals(
	ctx context.Context, req *directoryrosterv1.ConfirmGitHubRemovalsRequest,
) (*directoryrosterv1.ConfirmGitHubRemovalsResponse, error) {
	org, fingerprint := strings.TrimSpace(req.GetOrg()), strings.TrimSpace(req.GetFingerprint())
	id, err := c.requireOrg(ctx, access.RoleOperator, org)
	if err != nil {
		return nil, err
	}
	switch {
	case !status.ValidOrg(org) || fingerprint == "":
		return nil, connect.NewError(connect.CodeInvalidArgument, "an organisation and a fingerprint are required")
	case c.deps.Reports == nil || c.deps.Confirmations == nil:
		return nil, connect.NewError(connect.CodeFailedPrecondition, "this deployment keeps no reports or confirmations")
	}
	reports, err := c.deps.Reports.Reports(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	report, err := status.Decode(reports[status.Key(org)])
	if err != nil {
		return nil, rpcmount.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s has no readable report: %w", org, err))
	}
	if report.Breaker == nil || report.Breaker.Fingerprint != fingerprint {
		return nil, connect.Errorf(connect.CodeFailedPrecondition,
			"the removals in %s have changed since that page was loaded: reload and look again", org)
	}
	if err = c.deps.Confirmations.PutConfirmation(ctx, connection.Confirmation{
		Org: org, Fingerprint: fingerprint, By: id.Who(), At: time.Now().UTC(),
	}); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	c.record(ctx, audit.GitHubRemovalsConfirmed(identityActor(id), org, fingerprint,
		report.Breaker.Affected, report.Breaker.Members))
	c.notify(ctx, port.ControllerGitHub, org)
	return &directoryrosterv1.ConfirmGitHubRemovalsResponse{}, nil
}

// ImportGitHubLinks adopts approved pairings as links, after three checks
// each: somebody approved the pairing, an address is a live account the
// directory vouches for, and the account is a member of a connected
// organisation. A link the person made is never displaced.
func (c *Service) ImportGitHubLinks(
	ctx context.Context, req *directoryrosterv1.ImportGitHubLinksRequest,
) (*directoryrosterv1.ImportGitHubLinksResponse, error) {
	// Links are people's, not an organisation's, so the call is open to an
	// operator of any directory — but it adopts an account only on the
	// evidence of an organisation the caller may operate, so one company's
	// operator cannot pair accounts against another's membership.
	id, err := requireAnywhere(ctx, access.RoleOperator)
	if err != nil {
		return nil, err
	}
	records := req.GetRecords()
	origin := strings.TrimSpace(req.GetOrigin())
	switch {
	case len(records) == 0 || len(records) > importLimit:
		return nil, connect.Errorf(connect.CodeInvalidArgument, "between 1 and %d records, please", importLimit)
	case origin == "":
		return nil, connect.NewError(connect.CodeInvalidArgument, "say where the records come from")
	case c.deps.Orgs == nil || c.deps.Links == nil:
		return nil, connect.NewError(connect.CodeFailedPrecondition, "this deployment keeps no links")
	}
	members, err := c.memberCheck(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}

	out := &directoryrosterv1.ImportGitHubLinksResponse{}
	skip := func(login, reason string) {
		out.Skipped = append(out.Skipped, &directoryrosterv1.GitHubAccount{Login: login, Reason: reason})
	}
	now := time.Now().UTC()
	var candidates []link.Link
	logins := map[int64]string{}
	for _, record := range records {
		login := strings.TrimSpace(record.GetLogin())
		switch {
		case login == "":
			skip("", "a record with no login")
			continue
		case strings.TrimSpace(record.GetApprovedBy()) == "":
			skip(login, "nobody approved the pairing")
			continue
		}
		accepted, refused := c.linkableAddresses(ctx, record.GetEmails())
		if len(accepted) == 0 {
			skip(login, "no address is a live account the directory vouches for"+reasons(refused))
			continue
		}
		account, token, member, err := members(ctx, login)
		switch {
		case err != nil:
			skip(login, "GitHub could not be asked: "+err.Error())
			continue
		case !member:
			skip(login, "not a member of any connected organisation")
			continue
		}
		accountID, err := githubapp.UserID(ctx, c.githubHTTP(), token, account)
		if err != nil {
			skip(login, err.Error())
			continue
		}
		note := origin + ": approved by " + record.GetApprovedBy()
		if at := record.GetApprovedAt(); at != nil {
			note += " on " + at.AsTime().UTC().Format("2006-01-02")
		}
		logins[accountID] = login
		candidates = append(candidates, link.Link{
			ID: accountID, Login: account, Emails: accepted, State: link.StateLinked,
			Source: link.SourceImported, Note: note, LinkedAt: now, CheckedAt: now, ChangedAt: now,
		})
	}
	if len(candidates) == 0 {
		return out, nil
	}
	adopted, skipped, err := c.deps.Links.Adopt(ctx, candidates)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	for accountID, reason := range skipped {
		skip(logins[accountID], reason)
	}
	for i := range adopted {
		l := adopted[i].Public()
		out.Imported = append(out.Imported, linkProto(&l))
		c.record(ctx, audit.GitHubLinkImported(identityActor(id), l.Emails[0], l.Login, l.Note))
	}
	return out, nil
}

// memberCheck returns a function answering whether a login is a member of
// any connected organisation the caller may operate, with the canonical login and a token to
// read the account with. Installation tokens are minted once per call.
func (c *Service) memberCheck(ctx context.Context, id access.Identity) (func(context.Context, string) (string, string, bool, error), error) {
	records, err := c.deps.Orgs.List(ctx)
	if err != nil {
		return nil, err
	}
	type installed struct {
		org   githubapp.Org
		token string
	}
	var orgs []installed
	for i := range records {
		record := &records[i]
		if !record.Installed() || !mayOwned(id, access.RoleOperator, record.Owner) {
			continue
		}
		credential, found, err := c.deps.Orgs.Credential(ctx, record.Org)
		if err != nil || !found {
			continue
		}
		appToken, err := githubapp.AppToken(credential.AppID, credential.PrivateKey, time.Now())
		if err != nil {
			continue
		}
		token, _, err := githubapp.InstallationToken(ctx, c.githubHTTP(), appToken, credential.InstallationID)
		if err != nil {
			continue
		}
		orgs = append(orgs, installed{org: githubapp.Org{HTTP: c.githubHTTP(), Login: record.Org}, token: token})
	}
	if len(orgs) == 0 {
		return nil, errors.New("no connected organisation can be asked who its members are")
	}
	return func(ctx context.Context, login string) (string, string, bool, error) {
		for _, o := range orgs {
			member, err := o.org.IsMember(ctx, o.token, login)
			if err != nil {
				return "", "", false, err
			}
			if member {
				return login, o.token, true, nil
			}
		}
		return "", "", false, nil
	}, nil
}

func reasons(refused []string) string {
	if len(refused) == 0 {
		return ""
	}
	return " (" + strings.Join(refused, "; ") + ")"
}
