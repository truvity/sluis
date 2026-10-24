package server

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/modcall"
	outlineconsole "github.com/truvity/sluis/internal/outlineroster/console"
	"github.com/truvity/sluis/policy"
)

// Without `outline.remote` the issuer runs no Outline module, and says so.
func TestOutlinePagesNeedTheModule(t *testing.T) {
	t.Parallel()
	c := &Console{}
	if _, err := c.GetOutlineStatus(asRole(access.RoleViewer), &directoryrosterv1.GetOutlineStatusRequest{}); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("no module: %v, want failed precondition", err)
	}
	if _, err := c.GetOutlineStatus(context.Background(), &directoryrosterv1.GetOutlineStatusRequest{}); code(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous: %v, want unauthenticated", err)
	}
}

// The console calls the module as `console` with who the person is, and puts
// the module's refusals back as Connect errors.
func TestOutlinePagesCallTheModule(t *testing.T) {
	t.Parallel()
	declared, err := policy.Parse([]byte("version: 1\ngroups: { g: { members: [g@example.com] } }\n" +
		"outline: { workspaces: { wiki: { collections: { General: { read: [g] } } } } }\n"))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}
	svc := outlineconsole.NewService(outlineconsole.Deps{Policy: set})
	server := outlineconsole.NewServer(nil, func(context.Context) (*outlineconsole.Service, error) { return svc, nil })
	c := &Console{}
	c.deps.Modules.Outline = modcall.Local{outlineconsole.Module: server}.As(consolecall.Class)

	wiki := &directoryrosterv1.RequestOutlinePassRequest{Workspace: "wiki"}
	if _, err = c.RequestOutlinePass(asRole(access.RoleViewer), wiki); code(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer asking for a pass: %v, want permission denied", err)
	}
	other := &directoryrosterv1.RequestOutlinePassRequest{Workspace: "other"}
	if _, err = c.RequestOutlinePass(asRole(access.RoleOperator), other); code(err) != connect.CodeInvalidArgument {
		t.Errorf("an undeclared workspace: %v, want invalid argument", err)
	}
}
