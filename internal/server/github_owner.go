package server

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"github.com/truvity/sluis/internal/access"
)

// githubOwners are the connected organisations, by login, with the owner
// each record names. An organisation whose record names none (every record
// written before owners were recorded) is connected all the same: its entry
// is "" and only the installation-wide roles operate it. An organisation
// that is not connected has no entry at all, so presence is what "connected"
// means and the value alone is the owner.
func (c *Console) githubOwners(ctx context.Context) (map[string]string, error) {
	if c.deps.GitHubOrgs == nil {
		return nil, nil
	}
	records, err := c.deps.GitHubOrgs.List(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	out := make(map[string]string, len(records))
	for i := range records {
		out[records[i].Org] = records[i].Owner
	}
	return out, nil
}

// githubOwner is the directory workspace recorded as an organisation's
// owner ("" when it has none, or is not connected).
func (c *Console) githubOwner(ctx context.Context, org string) (string, error) {
	owners, err := c.githubOwners(ctx)
	return owners[org], err
}

// requireOrg is [requireOwner] for one GitHub organisation: the operator
// of its recorded owning directory, or the installation-wide role.
func (c *Console) requireOrg(ctx context.Context, want access.Role, org string) (access.Identity, error) {
	owner, err := c.githubOwner(ctx, org)
	if err != nil {
		return access.Identity{}, err
	}
	return requireOwner(ctx, want, owner, org)
}

// ownerOfBind is the owner of what a signed connect state binds, so the
// callback can ask the role question again, now, rather than trust the one
// asked when the flow began. An organisation already recorded keeps its
// recorded owner; one the flow is about to record is owned by whom the
// state, signed when the flow began, names. The link App is
// installation-wide.
func (c *Console) ownerOfBind(ctx context.Context, binding access.Binding) (owner, subject string, err error) {
	bind := binding.Bind
	switch {
	case strings.HasPrefix(bind, githubBind):
		subject = strings.TrimPrefix(bind, githubBind)
	case strings.HasPrefix(bind, githubRunnerBind):
		_, subject, _ = strings.Cut(strings.TrimPrefix(bind, githubRunnerBind), ":")
	case strings.HasPrefix(bind, githubCatalogueBind):
		if c.deps.GitHubCatalogue != nil {
			entry, _ := c.deps.GitHubCatalogue.Get(strings.TrimPrefix(bind, githubCatalogueBind))
			subject = entry.Org
		}
	default:
		return "", "the link App", nil
	}
	if c.deps.GitHubOrgs != nil {
		records, err := c.deps.GitHubOrgs.List(ctx)
		if err != nil {
			return "", subject, connect.NewError(connect.CodeUnavailable, err)
		}
		if record, connected := recordOf(records, subject); connected {
			return record.Owner, subject, nil
		}
	}
	if strings.HasPrefix(bind, githubBind) {
		return binding.Owner, subject, nil
	}
	return "", subject, nil
}
