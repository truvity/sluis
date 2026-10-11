//nolint:lll // messages and fixtures are prose and one-line tables
package server

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/cloudflare"
	cfrpc "github.com/truvity/sluis/internal/cloudflare/rpc"
	"github.com/truvity/sluis/internal/modcall"
)

// remoteConsole is a console whose module is another process: a module server
// holding the fake, called through the modcall boundary as `class`.
func remoteConsole(sts cfrpc.Source, class string) *Console {
	s := modcall.NewServer(cfrpc.Module)
	cfrpc.Register(s, sts)
	cfrpc.RegisterConsole(s, sts)
	client := cfrpc.NewClient(modcall.Local{cfrpc.Module: s}.As(class), nil)
	return &Console{deps: ConsoleDeps{Cloudflare: client}}
}

func TestTheCloudflarePageRendersFromARemoteModule(t *testing.T) {
	sts := newFakeSTS()
	sts.checks["backup"] = &cloudflare.PrototypeError{Reason: cloudflare.ReasonPrototypeActive, Detail: "the prototype is active"}
	c := remoteConsole(sts, cfrpc.CallerConsole)

	resp, err := c.ListCloudflare(asRole(access.RoleViewer), connect.NewRequest(&directoryrosterv1.ListCloudflareRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	m := resp.Msg
	if !m.Available || len(m.Accounts) != 1 || m.Accounts[0].IdLast4 != "cdef" || len(m.Presets) != 2 {
		t.Fatalf("%+v", m)
	}
	local, err := cloudflareConsole(sts).ListCloudflare(asRole(access.RoleViewer), connect.NewRequest(&directoryrosterv1.ListCloudflareRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	// The page is the same whether the module is here or there.
	if got, want := resp.Msg.String(), local.Msg.String(); got != want {
		t.Errorf("remote and local pages differ\n got: %s\nwant: %s", got, want)
	}
	var backup *directoryrosterv1.CloudflarePreset
	for _, p := range m.Presets {
		if p.Name == "backup" {
			backup = p
		}
	}
	if backup == nil || backup.Prototype.Status != "active" || backup.Prototype.Detail == "" {
		t.Errorf("the refused prototype is not shown: %+v", backup)
	}
	if strings.Contains(m.String(), secretValue) {
		t.Error("the page carries a value")
	}
}

func TestARemoteModuleActionsCarryTheOperatorAndItsRefusals(t *testing.T) {
	sts := newFakeSTS()
	c := remoteConsole(sts, cfrpc.CallerConsole)

	if _, err := c.RotateCloudflarePreset(asRole(access.RoleOperator), connect.NewRequest(&directoryrosterv1.RotateCloudflarePresetRequest{Preset: "dns"})); err != nil {
		t.Fatal(err)
	}
	if len(sts.rotated) != 1 || sts.rotated[0] != "dns" || len(sts.actors) != 1 || sts.actors[0].ID != "ada@example.com" {
		t.Errorf("rotated %v by %+v", sts.rotated, sts.actors)
	}
	res, err := c.RevokeCloudflareToken(asRole(access.RoleOperator), connect.NewRequest(&directoryrosterv1.RevokeCloudflareTokenRequest{Preset: "dns", TokenId: "stored1"}))
	if err != nil || !res.Msg.Replaced {
		t.Fatalf("%v %v", res, err)
	}
	if _, err = c.RevokeCloudflareToken(asRole(access.RoleOperator), connect.NewRequest(&directoryrosterv1.RevokeCloudflareTokenRequest{Preset: "dns", TokenId: "foreign"})); code(err) != connect.CodeNotFound {
		t.Errorf("a token that is not ours: %v", err)
	}
	sts.rotateOf = &cloudflare.PrototypeError{Reason: cloudflare.ReasonPrototypeForbidden, Detail: "forbidden group"}
	_, err = c.RotateCloudflarePreset(asRole(access.RoleOperator), connect.NewRequest(&directoryrosterv1.RotateCloudflarePresetRequest{Preset: "dns"}))
	if code(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "forbidden group") {
		t.Errorf("a refused prototype: %v", err)
	}
	sts.rotateOf = errors.New("cloudflare said token " + secretValue)
	_, err = c.RotateCloudflarePreset(asRole(access.RoleOperator), connect.NewRequest(&directoryrosterv1.RotateCloudflarePresetRequest{Preset: "dns"}))
	if code(err) != connect.CodeInternal || strings.Contains(err.Error(), secretValue) {
		t.Errorf("an internal failure must tell nothing: %v", err)
	}
	// The viewer is refused before the module is asked.
	if _, err = c.RotateCloudflarePreset(asRole(access.RoleViewer), connect.NewRequest(&directoryrosterv1.RotateCloudflarePresetRequest{Preset: "dns"})); code(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer rotated: %v", err)
	}
}

func TestARemoteCredentialIsMintedForTheGrantedPersonOnly(t *testing.T) {
	c := remoteConsole(newFakeSTS(), cfrpc.CallerConsole)
	resp, err := c.GetCloudflareCredential(asRole(access.RoleNone, "platform"), connect.NewRequest(&directoryrosterv1.GetCloudflareCredentialRequest{Preset: "dns"}))
	if err != nil || resp.Msg.Token != secretValue || resp.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%v %v", resp, err)
	}
	if _, err = c.GetCloudflareCredential(asRole(access.RoleNone, "other"), connect.NewRequest(&directoryrosterv1.GetCloudflareCredentialRequest{Preset: "dns"})); code(err) != connect.CodePermissionDenied {
		t.Errorf("an ungranted person: %v", err)
	}
	my, err := c.ListMyCloudflarePresets(asRole(access.RoleNone, "platform"), connect.NewRequest(&directoryrosterv1.ListMyCloudflarePresetsRequest{}))
	if err != nil || len(my.Msg.Presets) == 0 {
		t.Errorf("%v %v", my, err)
	}
}

// A class other than `console` reaches none of the console's methods.
func TestAForbiddenCallerClassIsRefusedTheConsolesMethods(t *testing.T) {
	for _, class := range []string{cfrpc.CallerIssuer, "github", ""} {
		sts := newFakeSTS()
		c := remoteConsole(sts, class)
		if _, err := c.ListCloudflare(asRole(access.RoleViewer), connect.NewRequest(&directoryrosterv1.ListCloudflareRequest{})); err == nil {
			t.Errorf("class %q read the page", class)
		}
		if _, err := c.RotateCloudflarePreset(asRole(access.RoleOperator), connect.NewRequest(&directoryrosterv1.RotateCloudflarePresetRequest{Preset: "dns"})); err == nil || len(sts.rotated) != 0 {
			t.Errorf("class %q rotated a preset", class)
		}
		if _, err := c.GetCloudflareCredential(asRole(access.RoleNone, "platform"), connect.NewRequest(&directoryrosterv1.GetCloudflareCredentialRequest{Preset: "dns"})); err == nil {
			t.Errorf("class %q minted a credential through the console's method", class)
		}
	}
}
