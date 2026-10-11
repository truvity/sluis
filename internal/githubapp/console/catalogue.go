package console

import (
	"context"
	"fmt"
	"strings"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"

	"connectrpc.com/connect"

	"github.com/truvity/sluis/internal/access"
)

// CheckGitHubCatalogueApp asks GitHub again about one App.
func (c *Service) CheckGitHubCatalogueApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.CheckGitHubCatalogueAppRequest],
) (*connect.Response[directoryrosterv1.CheckGitHubCatalogueAppResponse], error) {
	id := strings.TrimSpace(req.Msg.GetId())
	if _, err := c.requireCatalogueApp(ctx, access.RoleOperator, id); err != nil {
		return nil, err
	}
	if _, err := c.catalogueStore(); err != nil {
		return nil, err
	}
	specs, err := c.catalogueAppSpecs(ctx, map[string]bool{})
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	for i := range specs {
		if specs[i].id != id {
			continue
		}
		app := legacyCatalogueApp(specs[i], c.githubAppView(ctx, specs[i], true))
		return connect.NewResponse(&directoryrosterv1.CheckGitHubCatalogueAppResponse{App: app}), nil
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("the catalogue declares no App %q and none was created", id))
}

// requireCatalogueApp is [Service.requireOrg] for a catalogue App, by the
// organisation its entry declares — or, for one created from an entry since
// dropped, the organisation it was created under. An id nothing names is
// the installation-wide operator's to be told so: nobody owns it.
func (c *Service) requireCatalogueApp(ctx context.Context, want access.Role, id string) (access.Identity, error) {
	org := ""
	if c.deps.Catalogue != nil {
		if entry, declared := c.deps.Catalogue.Get(id); declared {
			org = entry.Org
		}
	}
	if org == "" && c.deps.CatalogueApps != nil {
		if record, _, created, err := c.deps.CatalogueApps.Get(ctx, id); err == nil && created {
			org = record.Org
		}
	}
	return c.requireOrg(ctx, want, org)
}
