package server

import (
	"context"
	"time"

	"github.com/truvity/sluis/internal/githubroster/runnerapp"
	"github.com/truvity/sluis/policy"

	"google.golang.org/protobuf/types/known/timestamppb"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/githubroster/link"
)

// timestampOf is a time for the page, absent when it never happened.
func timestampOf(at time.Time) *timestamppb.Timestamp {
	if at.IsZero() {
		return nil
	}
	return timestamppb.New(at)
}

// linkProto is a link as the contract carries it.
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

// binding is one organisation's side of the policy.
type binding struct {
	members []string
	ignore  []string
	teams   map[string]policy.TeamView
}

// boundOrganisations collects the policy's GitHub table by organisation.
// An organisation binding only teams has no org-level row, so both views
// are read.
func boundOrganisations(set *policy.Set) map[string]*binding {
	out := map[string]*binding{}
	get := func(org string) *binding {
		if out[org] == nil {
			out[org] = &binding{teams: map[string]policy.TeamView{}}
		}
		return out[org]
	}
	for _, team := range set.GitHubTeams() {
		get(team.Org).teams[team.Team] = team
	}
	for _, org := range set.GitHubOrgs() {
		get(org.Org).members = org.Members
		get(org.Org).ignore = org.Ignore
	}
	return out
}

// runnerApp finds one runner App's record.
func (c *Console) runnerApp(ctx context.Context, tier, org string) (runnerapp.Record, bool, error) {
	records, err := c.deps.GitHubRunnerApps.List(ctx)
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
