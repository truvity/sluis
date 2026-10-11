package consoleauthz_test

import (
	"errors"
	"testing"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consoleauthz"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/modcall"
)

func TestRequireOwner(t *testing.T) {
	global := consolecall.Caller{Actor: "g", Roles: []string{"operator"}}
	viewer := consolecall.Caller{Actor: "v", Roles: []string{"viewer"}}
	scoped := consolecall.Caller{Actor: "s", Workspaces: map[string]string{"w1": "operator"}}
	for _, tc := range []struct {
		name  string
		c     consolecall.Caller
		owner string
		code  string
	}{
		{"global operator, owned", global, "w1", ""},
		{"global operator, unowned", global, "", ""},
		{"scoped operator, own directory", scoped, "w1", ""},
		{"scoped operator, other directory", scoped, "w2", modcall.CodeForbidden},
		{"scoped operator, unowned", scoped, "", modcall.CodeForbidden},
		{"viewer is not an operator", viewer, "w1", modcall.CodeForbidden},
		{"no actor", consolecall.Caller{Roles: []string{"operator"}}, "w1", consoleauthz.CodeUnauthenticated},
		{"invented role", consolecall.Caller{Actor: "x", Roles: []string{"root"}}, "", modcall.CodeForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for name, fn := range map[string]func() error{
				"owner": func() error {
					_, err := consoleauthz.RequireOwner(tc.c, access.RoleOperator, tc.owner, "acme")
					return err
				},
				"org": func() error {
					_, err := consoleauthz.RequireOrg(tc.c, access.RoleOperator, tc.owner, "acme")
					return err
				},
			} {
				err := fn()
				var coded *modcall.Error
				switch {
				case tc.code == "" && err != nil:
					t.Errorf("%s: %v", name, err)
				case tc.code != "" && (!errors.As(err, &coded) || coded.Code != tc.code):
					t.Errorf("%s: got %v, want %s", name, err, tc.code)
				}
			}
		})
	}
}

func TestRequireRoleWorkspaceAnywhere(t *testing.T) {
	scoped := consolecall.Caller{Actor: "s", Workspaces: map[string]string{"w1": "viewer"}}
	if _, err := consoleauthz.RequireRole(scoped, access.RoleViewer); err == nil {
		t.Error("a scope passed the installation-wide gate")
	}
	if _, err := consoleauthz.RequireWorkspace(scoped, access.RoleViewer, "w1"); err != nil {
		t.Error(err)
	}
	if _, err := consoleauthz.RequireWorkspace(scoped, access.RoleOperator, "w1"); err == nil {
		t.Error("a viewer scope passed as operator")
	}
	if _, err := consoleauthz.RequireAnywhere(scoped, access.RoleViewer); err != nil {
		t.Error(err)
	}
	if _, err := consoleauthz.RequireAnywhere(consolecall.Caller{Actor: "n"}, access.RoleViewer); err == nil {
		t.Error("no role passed")
	}
}
