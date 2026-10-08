package cloudflare_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/cloudflare"
)

func TestForbiddenGroups(t *testing.T) {
	for _, name := range []string{
		"Account API Tokens Write", "Account API Tokens Edit", "API Tokens Write", "account  api tokens  write",
		"Billing Read", "Billing Write", "Account Settings Read", "Account Settings Write",
		"Memberships Read", "Memberships Write", "Access: Organizations, Identity Providers, and Groups Write",
		"Access: Organizations, Identity Providers, and Groups Read",
	} {
		if !cloudflare.ForbiddenGroup(name) {
			t.Errorf("%q is allowed", name)
		}
	}
	for _, name := range []string{
		"DNS Write", "Workers R2 Storage Write", "Account API Tokens Read", "Zone Read", "Access: Apps and Policies Write", "Workers Scripts Write",
	} {
		if cloudflare.ForbiddenGroup(name) {
			t.Errorf("%q is forbidden", name)
		}
	}
}

func proto(status string, groups ...string) cloudflare.Token {
	var gs []string
	for _, g := range groups {
		gs = append(gs, `{"id":"`+g+`"}`)
	}
	return cloudflare.Token{ID: "proto-0001", Status: status,
		Policies: json.RawMessage(`[{"effect":"allow","resources":{"r":"*"},"permission_groups":[` + strings.Join(gs, ",") + `]}]`)}
}

func TestCheckPrototype(t *testing.T) {
	names := map[string]string{"a": "DNS Write", "b": "Billing Write"}
	reason := func(tok cloudflare.Token) string {
		err := cloudflare.CheckPrototype(tok, names)
		if err == nil {
			return ""
		}
		pe, ok := cloudflare.IsPrototypeError(err)
		if !ok {
			t.Fatalf("not a PrototypeError: %v", err)
		}
		return pe.Reason
	}
	if r := reason(proto("disabled", "a")); r != "" {
		t.Errorf("good: %s", r)
	}
	if r := reason(proto("active", "a")); r != cloudflare.ReasonPrototypeActive {
		t.Errorf("active: %s", r)
	}
	if r := reason(proto("expired", "a")); r != cloudflare.ReasonPrototypeActive {
		t.Errorf("expired: %s", r)
	}
	if r := reason(proto("disabled", "a", "b")); r != cloudflare.ReasonPrototypeForbidden {
		t.Errorf("forbidden: %s", r)
	}
	if r := reason(proto("disabled", "zzz")); r != cloudflare.ReasonPrototypeForbidden {
		t.Errorf("unresolvable: %s", r)
	}
	if r := reason(cloudflare.Token{}); r != cloudflare.ReasonPrototypeMissing {
		t.Errorf("missing: %s", r)
	}
	if r := reason(cloudflare.Token{ID: "x", Status: "disabled", Policies: json.RawMessage(`[]`)}); r != cloudflare.ReasonPrototypeMissing {
		t.Errorf("no policies: %s", r)
	}
}

func TestCloneConditionSpellsTheIPKeyOnce(t *testing.T) {
	for _, in := range []string{
		`{"request_ip":{"in":["203.0.113.0/24"]}}`, `{"request.ip":{"in":["203.0.113.0/24"]}}`,
	} {
		out, err := cloudflare.CloneCondition(json.RawMessage(in))
		if err != nil || string(out) != `{"`+cloudflare.ConditionIPKey+`":{"in":["203.0.113.0/24"]}}` {
			t.Errorf("%s -> %s %v", in, out, err)
		}
	}
	for _, in := range []string{``, `null`, `{}`, `{"request_ip":{}}`, `{"request.ip":null}`} {
		if out, err := cloudflare.CloneCondition(json.RawMessage(in)); err != nil || out != nil {
			t.Errorf("%q -> %s %v, want none", in, out, err)
		}
	}
}

func TestNaming(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	if got := cloudflare.StoredName("i", "dns", at); got != "sluis/i/dns/2026-10-08T12:00:00Z" {
		t.Error(got)
	}
	od := cloudflare.OnDemandName("i", "dns", "github:org/repo:job", at)
	if od != "sluis/i/dns/github:org_repo:job/2026-10-08T12:00:00Z" {
		t.Error(od)
	}
	if !cloudflare.IsOwn(od, "i", "dns") || cloudflare.IsOwn(od, "i", "dn") || cloudflare.IsOwn(od, "j", "dns") || cloudflare.IsOwn("sluis/i/dns/", "i", "dns") {
		t.Error("IsOwn")
	}
	if cloudflare.IsStored(od, "i", "dns") || !cloudflare.IsStored(cloudflare.StoredName("i", "dns", at), "i", "dns") {
		t.Error("IsStored")
	}
	if got := cloudflare.Caller(strings.Repeat("a", 200)); len(got) > 70 {
		t.Error(len(got))
	}
}

func TestClonePoliciesKeepsTheRightsAndDropsWhatCloudflareGenerated(t *testing.T) {
	in := `[{"id":"p1","effect":"deny","resources":{"a":"*","b":{"c":"*"}},"permission_groups":[{"id":"g1","name":"n","meta":{"x":1}},{"id":"g2"}]}]`
	out, err := cloudflare.ClonePolicies(json.RawMessage(in))
	want := `[{"effect":"deny","resources":{"a":"*","b":{"c":"*"}},"permission_groups":[{"id":"g1"},{"id":"g2"}]}]`
	if err != nil || string(out) != want {
		t.Errorf("%s %v", out, err)
	}
	if got := cloudflare.R2Secret("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Error(got)
	}
}
