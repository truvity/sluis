//nolint:lll // messages and fixtures are prose and one-line tables
package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/cloudflare/minter"
)

const secretValue = "cf-secret-value-that-must-not-be-listed"

// fakeSTS is a minter that never reaches Cloudflare. Its only secret is the
// value of a token it mints on demand.
type fakeSTS struct {
	checks   map[string]error
	live     map[string][]minter.LiveToken
	stored   map[string]minter.StoredInfo
	granted  map[string][]string // preset -> groups
	rotated  []string
	revoked  []string
	actors   []audit.Actor
	rotateOf error
	expires  time.Time
}

func newFakeSTS() *fakeSTS {
	exp := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	return &fakeSTS{
		checks: map[string]error{},
		live: map[string][]minter.LiveToken{
			"dns": {
				{ID: "stored1", Stored: true, ExpiresOn: exp},
				{ID: "demand1", Caller: "ada@example.com", MintedAt: exp.Add(-time.Hour), ExpiresOn: exp.Add(-30 * time.Minute)},
			},
		},
		stored:  map[string]minter.StoredInfo{"dns": {Present: true, MintedAt: exp.Add(-time.Hour), ExpiresOn: exp}},
		granted: map[string][]string{"dns": {"platform"}},
		expires: exp,
	}
}

func (f *fakeSTS) Accounts() []minter.AccountInfo {
	return []minter.AccountInfo{{Name: "main", ID: "0123456789abcdef0123456789abcdef"}}
}

func (f *fakeSTS) info(name string) minter.PresetInfo {
	return minter.PresetInfo{Name: name, Description: name + " records", Account: "main", Lifetime: time.Hour, Rotation: 20 * time.Minute}
}

func (f *fakeSTS) Presets() []minter.PresetInfo {
	return []minter.PresetInfo{f.info("dns"), f.info("backup")}
}

func (f *fakeSTS) Granted(c minter.Caller) []minter.PresetInfo {
	var out []minter.PresetInfo
	for _, p := range f.Presets() {
		for _, g := range f.granted[p.Name] {
			for _, held := range c.Groups {
				if g == held {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

func (f *fakeSTS) PrototypeID(preset string) (string, bool) { return "proto-" + preset, true }

func (f *fakeSTS) CheckPreset(_ context.Context, preset string) error { return f.checks[preset] }

func (f *fakeSTS) Stored(_ context.Context, preset string) (minter.StoredInfo, error) {
	return f.stored[preset], nil
}

func (f *fakeSTS) Live(_ context.Context, preset string) ([]minter.LiveToken, error) {
	return f.live[preset], nil
}

func (f *fakeSTS) RotateNow(_ context.Context, preset string, actor audit.Actor) (*minter.Minted, error) {
	if f.rotateOf != nil {
		return nil, f.rotateOf
	}
	f.rotated, f.actors = append(f.rotated, preset), append(f.actors, actor)
	return &minter.Minted{Preset: preset, TokenID: "new1", ExpiresOn: f.expires, Token: secretValue}, nil
}

func (f *fakeSTS) Revoke(_ context.Context, preset, tokenID string, actor audit.Actor) (minter.RevokeResult, error) {
	if tokenID == "foreign" {
		return minter.RevokeResult{}, minter.ErrNotOurs
	}
	f.revoked, f.actors = append(f.revoked, preset+"/"+tokenID), append(f.actors, actor)
	return minter.RevokeResult{Rotated: tokenID == "stored1"}, nil
}

func (f *fakeSTS) MintFor(_ context.Context, preset string, c minter.Caller, lifetime time.Duration) (*minter.Minted, error) {
	if len(f.Granted(c)) == 0 || f.Granted(c)[0].Name != preset {
		return nil, minter.ErrNotGranted
	}
	if lifetime > time.Hour {
		return nil, minter.ErrLifetime
	}
	f.actors = append(f.actors, c.Actor)
	return &minter.Minted{Preset: preset, TokenID: "od1", ExpiresOn: f.expires, Token: secretValue}, nil
}

func cloudflareConsole(sts CloudflareSTS) *Console {
	return &Console{deps: ConsoleDeps{Cloudflare: sts}}
}

func asRole(role access.Role, groups ...string) context.Context {
	return WithIdentity(context.Background(), access.Identity{Email: "ada@example.com", Role: role, Groups: groups})
}

func code(err error) connect.Code { return connect.CodeOf(err) }

func TestCloudflareListIsTheAdministratorsAndRedactsTheAccount(t *testing.T) {
	sts := newFakeSTS()
	sts.checks["backup"] = &cloudflare.PrototypeError{Reason: cloudflare.ReasonPrototypeActive, Detail: "the prototype is active"}
	c := cloudflareConsole(sts)

	if _, err := c.ListCloudflare(context.Background(), connect.NewRequest(&directoryrosterv1.ListCloudflareRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous list: %v", err)
	}
	if _, err := c.ListCloudflare(asRole(access.RoleNone), connect.NewRequest(&directoryrosterv1.ListCloudflareRequest{})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("a person with no role may not list: %v", err)
	}
	resp, err := c.ListCloudflare(asRole(access.RoleViewer), connect.NewRequest(&directoryrosterv1.ListCloudflareRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	m := resp.Msg
	if !m.Available || m.CanOperate {
		t.Fatalf("available=%v canOperate=%v, want a viewer to see it and not operate", m.Available, m.CanOperate)
	}
	if got := m.Accounts[0].IdLast4; got != "cdef" {
		t.Fatalf("account id = %q, want its last four", got)
	}
	if strings.Contains(m.String(), "0123456789abcdef0123456789abcdef") {
		t.Fatal("the full account id is in the response")
	}
	byName := map[string]*directoryrosterv1.CloudflarePreset{}
	for _, p := range m.Presets {
		byName[p.Name] = p
	}
	if got := byName["dns"].Prototype.Status; got != "ok" {
		t.Fatalf("dns prototype = %q", got)
	}
	if got := byName["backup"].Prototype; got.Status != "active" || got.Detail == "" {
		t.Fatalf("backup prototype = %v, want active with a reason", got)
	}
	dns := byName["dns"]
	if dns.Stored.TokenId != "stored1" {
		t.Fatalf("stored token id = %q, want the live stored token matched by expiry", dns.Stored.TokenId)
	}
	if len(dns.Live) != 1 || dns.Live[0].Id != "demand1" || dns.Live[0].Caller != "ada@example.com" {
		t.Fatalf("live = %v, want only the on-demand token, the stored one shown as stored", dns.Live)
	}
	if dns.LifetimeSeconds != 3600 || dns.RotationSeconds != 1200 {
		t.Fatalf("lifetime/rotation = %d/%d", dns.LifetimeSeconds, dns.RotationSeconds)
	}
	op, err := c.ListCloudflare(asRole(access.RoleOperator), connect.NewRequest(&directoryrosterv1.ListCloudflareRequest{}))
	if err != nil || !op.Msg.CanOperate {
		t.Fatalf("an operator can operate: %v %v", op, err)
	}
}

// The list response has no field a token's value could travel in. A guard on the
// contract rather than on one fixture: adding such a field fails here.
func TestCloudflareListResponseHasNoFieldForAValue(t *testing.T) {
	var walk func(protoreflect.MessageDescriptor, map[string]bool)
	walk = func(md protoreflect.MessageDescriptor, seen map[string]bool) {
		if seen[string(md.FullName())] {
			return
		}
		seen[string(md.FullName())] = true
		for i := 0; i < md.Fields().Len(); i++ {
			f := md.Fields().Get(i)
			switch name := string(f.Name()); name {
			case "token", "secret_access_key", "value", "secret", "password":
				t.Errorf("%s.%s can carry a value", md.FullName(), name)
			}
			if f.Message() != nil {
				walk(f.Message(), seen)
			}
		}
	}
	walk((&directoryrosterv1.ListCloudflareResponse{}).ProtoReflect().Descriptor(), map[string]bool{})
	walk((&directoryrosterv1.ListMyCloudflarePresetsResponse{}).ProtoReflect().Descriptor(), map[string]bool{})
}

func TestCloudflareActionsNeedTheOperatorAndAreAttributed(t *testing.T) {
	sts := newFakeSTS()
	c := cloudflareConsole(sts)
	rotate := connect.NewRequest(&directoryrosterv1.RotateCloudflarePresetRequest{Preset: "dns"})
	revoke := connect.NewRequest(&directoryrosterv1.RevokeCloudflareTokenRequest{Preset: "dns", TokenId: "demand1"})

	if _, err := c.RotateCloudflarePreset(asRole(access.RoleViewer), rotate); code(err) != connect.CodePermissionDenied {
		t.Fatalf("a viewer rotated: %v", err)
	}
	if _, err := c.RevokeCloudflareToken(asRole(access.RoleViewer), revoke); code(err) != connect.CodePermissionDenied {
		t.Fatalf("a viewer revoked: %v", err)
	}
	if _, err := c.RotateCloudflarePreset(context.Background(), rotate); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous rotated: %v", err)
	}
	if len(sts.rotated)+len(sts.revoked) != 0 {
		t.Fatal("a refused call reached the minter")
	}

	got, err := c.RotateCloudflarePreset(asRole(access.RoleOperator), rotate)
	if err != nil || got.Msg.TokenId != "new1" {
		t.Fatalf("rotate: %v %v", got, err)
	}
	if strings.Contains(got.Msg.String(), secretValue) {
		t.Fatal("the rotate response carries the token's value")
	}
	if _, err = c.RevokeCloudflareToken(asRole(access.RoleOperator), revoke); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	replaced, err := c.RevokeCloudflareToken(asRole(access.RoleOperator),
		connect.NewRequest(&directoryrosterv1.RevokeCloudflareTokenRequest{Preset: "dns", TokenId: "stored1"}))
	if err != nil || !replaced.Msg.Replaced {
		t.Fatalf("revoking the stored token reports its replacement: %v %v", replaced, err)
	}
	if _, err = c.RevokeCloudflareToken(asRole(access.RoleOperator),
		connect.NewRequest(&directoryrosterv1.RevokeCloudflareTokenRequest{Preset: "dns", TokenId: "foreign"})); code(err) != connect.CodeNotFound {
		t.Fatalf("a token that is not ours: %v", err)
	}
	for _, a := range sts.actors {
		if a.ID != "ada@example.com" {
			t.Fatalf("an action was attributed to %q, want the signed-in operator", a.ID)
		}
	}
}

func TestCloudflareRotateReportsAContendedLeaseAsAborted(t *testing.T) {
	sts := newFakeSTS()
	sts.rotateOf = minter.ErrContended
	_, err := cloudflareConsole(sts).RotateCloudflarePreset(asRole(access.RoleOperator),
		connect.NewRequest(&directoryrosterv1.RotateCloudflarePresetRequest{Preset: "dns"}))
	if code(err) != connect.CodeAborted {
		t.Fatalf("got %v", err)
	}
}

func TestCloudflareInternalFailuresDoNotLeakDetail(t *testing.T) {
	sts := newFakeSTS()
	sts.rotateOf = errors.New("PUT https://api.example/accounts/0123/tokens: 403 token=" + secretValue)
	_, err := cloudflareConsole(sts).RotateCloudflarePreset(asRole(access.RoleOperator),
		connect.NewRequest(&directoryrosterv1.RotateCloudflarePresetRequest{Preset: "dns"}))
	if code(err) != connect.CodeInternal || strings.Contains(err.Error(), secretValue) {
		t.Fatalf("got %v", err)
	}
}

func TestCloudflarePersonSeesOnlyGrantedPresets(t *testing.T) {
	c := cloudflareConsole(newFakeSTS())
	list := func(ctx context.Context) []*directoryrosterv1.MyCloudflarePreset {
		t.Helper()
		resp, err := c.ListMyCloudflarePresets(ctx, connect.NewRequest(&directoryrosterv1.ListMyCloudflarePresetsRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.Presets
	}
	if got := list(asRole(access.RoleNone, "platform")); len(got) != 1 || got[0].Name != "dns" {
		t.Fatalf("a member of platform sees %v", got)
	}
	if got := list(asRole(access.RoleOperator, "other")); len(got) != 0 {
		t.Fatalf("an operator in no granting group sees %v: the role grants nothing here", got)
	}
	if _, err := c.ListMyCloudflarePresets(context.Background(), connect.NewRequest(&directoryrosterv1.ListMyCloudflarePresetsRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous: %v", err)
	}
}

func TestCloudflareCredentialIsShownToAGrantedPersonOnce(t *testing.T) {
	sts := newFakeSTS()
	c := cloudflareConsole(sts)
	ask := func(ctx context.Context, preset string, seconds int64) (*connect.Response[directoryrosterv1.GetCloudflareCredentialResponse], error) {
		return c.GetCloudflareCredential(ctx, connect.NewRequest(&directoryrosterv1.GetCloudflareCredentialRequest{Preset: preset, LifetimeSeconds: seconds}))
	}
	resp, err := ask(asRole(access.RoleNone, "platform"), "dns", 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.Token != secretValue || resp.Msg.TokenId != "od1" {
		t.Fatalf("got %v", resp.Msg)
	}
	if resp.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("a credential must not be cached")
	}
	if _, err = ask(asRole(access.RoleOperator, "other"), "dns", 0); code(err) != connect.CodePermissionDenied {
		t.Fatalf("not granted: %v", err)
	}
	if _, err = ask(asRole(access.RoleNone, "platform"), "dns", 7200); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("longer than the preset: %v", err)
	}
	if _, err = ask(asRole(access.RoleNone, "platform"), "dns", -1); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("negative: %v", err)
	}
	if _, err = ask(context.Background(), "dns", 0); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous: %v", err)
	}
}

func TestCloudflareOffWithoutTheSection(t *testing.T) {
	c := cloudflareConsole(nil)
	resp, err := c.ListCloudflare(asRole(access.RoleViewer), connect.NewRequest(&directoryrosterv1.ListCloudflareRequest{}))
	if err != nil || resp.Msg.Available {
		t.Fatalf("got %v %v", resp, err)
	}
	if _, err = c.RotateCloudflarePreset(asRole(access.RoleOperator), connect.NewRequest(&directoryrosterv1.RotateCloudflarePresetRequest{Preset: "dns"})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("got %v", err)
	}
	if my, err := c.ListMyCloudflarePresets(asRole(access.RoleNone), connect.NewRequest(&directoryrosterv1.ListMyCloudflarePresetsRequest{})); err != nil || my.Msg.Available {
		t.Fatalf("got %v %v", my, err)
	}
}

func TestWhoamiSaysWhetherTheConsoleHasACloudflarePage(t *testing.T) {
	for _, on := range []bool{false, true} {
		console := cloudflareConsole(nil)
		if on {
			console.deps.Cloudflare = newFakeSTS()
		}
		s := &ConsoleServer{console: console, log: slog.New(slog.DiscardHandler)}
		r := httptest.NewRequest(http.MethodGet, "/.access/whoami", nil).WithContext(asRole(access.RoleNone))
		w := httptest.NewRecorder()
		s.whoami(w, r)
		if got := strings.Contains(w.Body.String(), `"cloudflare":true`); got != on {
			t.Errorf("cloudflare page = %v with the minter %v: %s", got, on, w.Body.String())
		}
	}
}
