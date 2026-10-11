package consolecall_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/modcall"
)

func server(t *testing.T) modcall.Local {
	t.Helper()
	s := modcall.NewServer("github")
	consolecall.Handle(s, "console.echo", func(_ context.Context, c consolecall.Caller, r *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return wrapperspb.String(c.Actor + ":" + r.Value), nil
	})
	consolecall.Handle(s, "console.refuse", func(context.Context, consolecall.Caller, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return nil, &consolecall.Problem{Summary: "no", Detail: "not yours", Hints: []string{"ask the owner"}}
	})
	consolecall.Handle(s, "console.boom", func(context.Context, consolecall.Caller, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return nil, errors.New("secret internal detail")
	})
	consolecall.Handle(s, "console.list", func(_ context.Context, _ consolecall.Caller, r *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return wrapperspb.String(strings.Repeat("x", int(r.Value[0])<<12)), nil
	}, consolecall.List())
	consolecall.Handle(s, "console.biglist", func(_ context.Context, _ consolecall.Caller, _ *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return wrapperspb.String(strings.Repeat("x", 300<<10)), nil
	})
	return modcall.Local{"github": s}
}

func TestTheAllowTable(t *testing.T) {
	l := server(t)
	for _, tc := range []struct {
		class  string
		method string
		code   string
	}{
		{consolecall.Class, "console.echo", ""},
		{"issuer", "console.echo", modcall.CodeForbidden},
		{"admin", "console.echo", modcall.CodeForbidden},
		{"breakglass", "console.echo", modcall.CodeForbidden},
		{"", "console.echo", modcall.CodeForbidden},
	} {
		_, err := proxyStr(context.Background(), l.As(tc.class), "github", tc.method,
			consolecall.Caller{Actor: "a"}, wrapperspb.String("hi"))
		var coded *modcall.Error
		switch {
		case tc.code == "" && err != nil:
			t.Errorf("class %q: %v", tc.class, err)
		case tc.code != "" && (!errors.As(err, &coded) || coded.Code != tc.code):
			t.Errorf("class %q: got %v, want %s", tc.class, err, tc.code)
		}
	}
}

func TestTheEnvelopeRoundTrips(t *testing.T) {
	l := server(t)
	got, err := proxyStr(context.Background(), l.As(consolecall.Class), "github", "console.echo",
		consolecall.Caller{Actor: "alice@acme.test", Roles: []string{"viewer"}, Workspaces: map[string]string{"w1": "operator"}}, wrapperspb.String("hi"))
	if err != nil || got.Value != "alice@acme.test:hi" {
		t.Fatalf("got %v, %v", got, err)
	}

	env, err := consolecall.Wrap(consolecall.Caller{Actor: "a", Roles: []string{"operator"}, Workspaces: map[string]string{"w": "viewer"}}, wrapperspb.String("m"))
	if err != nil {
		t.Fatal(err)
	}
	var back wrapperspb.StringValue
	if err := env.Unwrap(&back); err != nil || back.Value != "m" {
		t.Fatalf("unwrap: %v %v", &back, err)
	}
	for _, key := range []string{`"actor"`, `"roles"`, `"workspaces"`} {
		raw, _ := jsonOf(env)
		if !strings.Contains(raw, key) {
			t.Errorf("the envelope has no %s: %s", key, raw)
		}
	}
}

func TestAProblemIsDataAndAnErrorHasNoDetail(t *testing.T) {
	l := server(t).As(consolecall.Class)
	_, err := proxyStr(context.Background(), l, "github", "console.refuse", consolecall.Caller{Actor: "a"}, wrapperspb.String(""))
	var p *consolecall.Problem
	if !errors.As(err, &p) || p.Summary != "no" || p.Detail != "not yours" || !slices.Equal(p.Hints, []string{"ask the owner"}) {
		t.Fatalf("got %v", err)
	}
	_, err = proxyStr(context.Background(), l, "github", "console.boom", consolecall.Caller{Actor: "a"}, wrapperspb.String(""))
	var coded *modcall.Error
	if !errors.As(err, &coded) || coded.Code != modcall.CodeInternal || strings.Contains(err.Error(), "secret") {
		t.Fatalf("got %v", err)
	}
}

func TestAListMayExceedTheDefaultBound(t *testing.T) {
	l := server(t).As(consolecall.Class)
	// 80 << 12 = 320K: over the default 256K, inside the list bound.
	got, err := proxyStr(context.Background(), l, "github", "console.list", consolecall.Caller{Actor: "a"}, wrapperspb.String("P"))
	if err != nil || len(got.Value) <= modcall.DefaultMaxBytes {
		t.Fatalf("a list of %d bytes: %v", len(got.GetValue()), err)
	}
	// The same size without List is refused, and not echoed.
	_, err = proxyStr(context.Background(), l, "github", "console.biglist", consolecall.Caller{Actor: "a"}, wrapperspb.String(""))
	var coded *modcall.Error
	if !errors.As(err, &coded) || coded.Code != modcall.CodeInternal {
		t.Fatalf("got %v", err)
	}
	// A request over the default bound is refused by a method without List.
	_, err = proxyStr(context.Background(), l, "github", "console.echo", consolecall.Caller{Actor: "a"}, wrapperspb.String(strings.Repeat("y", 300<<10)))
	if !errors.As(err, &coded) || coded.Code != modcall.CodeBadRequest {
		t.Fatalf("got %v", err)
	}
	for _, m := range mustServer(t).Methods() {
		if m.Name == "console.list" && m.MaxBytes <= modcall.DefaultMaxBytes {
			t.Errorf("list bound is %d", m.MaxBytes)
		}
	}
}

func TestHandleOverridesAnyAllow(t *testing.T) {
	s := modcall.NewServer("slack")
	consolecall.Handle(s, "console.x", func(context.Context, consolecall.Caller, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return wrapperspb.String(""), nil
	}, modcall.Allow("issuer", "console"))
	if v := consolecall.Violations(s); len(v) != 0 {
		t.Fatal(v)
	}
	defer func() {
		if recover() == nil {
			t.Error("a console method without the prefix registered")
		}
	}()
	consolecall.Handle(s, "x", func(context.Context, consolecall.Caller, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return nil, nil
	})
}

// The lint: every registered console method is Allow("console") only.
func TestViolations(t *testing.T) {
	s := modcall.NewServer("google")
	h := func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) { return nil, nil }
	modcall.Handle(s, "console.open", h)                                    // any caller
	modcall.Handle(s, "console.two", h, modcall.Allow("console", "issuer")) // shared
	modcall.Handle(s, "console.other", h, modcall.Allow("issuer"))
	modcall.Handle(s, "console.none", h, modcall.Allow())
	modcall.Handle(s, "console.ok", h, modcall.Allow("console"))
	modcall.Handle(s, "mint", h, modcall.Allow("issuer"))
	modcall.Handle(s, "leak", h, modcall.Allow("console")) // console-only without the prefix
	got := consolecall.Violations(s)
	if len(got) != 5 {
		t.Fatalf("got %d: %q", len(got), got)
	}
	for _, name := range []string{"console.open", "console.two", "console.other", "console.none", "leak"} {
		if !slices.ContainsFunc(got, func(v string) bool { return strings.Contains(v, "."+name+" ") }) {
			t.Errorf("%s is not reported: %q", name, got)
		}
	}
	if v := consolecall.Violations(mustServer(t)); len(v) != 0 {
		t.Errorf("the plumbing's own server: %q", v)
	}
}

func TestSnapshotRoundTripsAnIdentity(t *testing.T) {
	id := access.Identity{Email: "a@acme.test", Role: access.RoleViewer, Scopes: map[string]access.Role{"w": access.RoleOperator, "none": access.RoleNone}}
	c := consolecall.Snapshot(id)
	if c.Actor != "a@acme.test" || !slices.Equal(c.Roles, []string{"viewer"}) || len(c.Workspaces) != 1 {
		t.Fatalf("%+v", c)
	}
	back := c.Identity()
	if !back.Can(access.RoleViewer) || back.Can(access.RoleOperator) || !back.CanFor(access.RoleOperator, "w") || back.CanFor(access.RoleOperator, "x") {
		t.Errorf("%+v", back)
	}
	// A role the snapshot invents grants nothing.
	if forged := (consolecall.Caller{Actor: "a", Roles: []string{"root"}}).Identity(); forged.CanAnywhere(access.RoleViewer) {
		t.Errorf("%+v", forged)
	}
}

func TestCallersDefaultToLocal(t *testing.T) {
	l := server(t)
	c := consolecall.Callers{}.OrLocal(l)
	for _, caller := range []modcall.Caller{c.GitHub, c.Slack, c.Google} {
		if _, err := proxyStr(context.Background(), caller, "github", "console.echo", consolecall.Caller{Actor: "a"}, wrapperspb.String("")); err != nil {
			t.Error(err)
		}
	}
	own := modcall.Local{}.As("x")
	if got := (consolecall.Callers{Slack: own}).OrLocal(l); got.Slack == nil || got.GitHub == nil {
		t.Error("a caller was lost")
	}
}

// proxyStr is consolecall.Proxy for the string messages these tests send.
func proxyStr(
	ctx context.Context, l modcall.Caller, module, method string, c consolecall.Caller, req *wrapperspb.StringValue,
) (*wrapperspb.StringValue, error) {
	return consolecall.Proxy[*wrapperspb.StringValue, *wrapperspb.StringValue](ctx, l, module, method, c, req)
}
