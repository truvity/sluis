//nolint:lll // messages and fixtures are prose and one-line tables
package minter_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/config"
)

var ctx = context.Background()

func goodSection() *config.Cloudflare {
	return &config.Cloudflare{
		Accounts: map[string]config.CloudflareAccount{"main": {ID: acct, Minter: "internal/cloudflare/main/minter"}},
		Presets: map[string]config.CloudflarePreset{"dns": {Account: "main", Prototype: dnsProto, Description: "d",
			Lifetime: config.Duration(15 * time.Minute), Rotation: config.Duration(5 * time.Minute)}},
	}
}

// A clone has the prototype's policies and condition exactly, a name of the
// right shape and expires_on = now + lifetime.
func TestTheTickClonesThePrototypeAndStoresTheToken(t *testing.T) {
	e := setup(t)
	res := e.m.TickPreset(ctx, "dns")
	if res.Err != nil || res.Outcome != minter.OutcomeRotated {
		t.Fatalf("tick = %+v", res)
	}
	if e.api.gotMinter != "minter-secret" {
		t.Errorf("the account was opened with %q", e.api.gotMinter)
	}
	doc, _, err := e.external("dns").Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := e.clock.Add(15 * time.Minute)
	if doc.Token != "value-tok001" || doc.ExpiresOn != want.Format(time.RFC3339) {
		t.Fatalf("stored = %+v", doc)
	}
	got, _ := e.api.GetToken(ctx, "tok001")
	if got.Name != "sluis/example/dns/2026-10-08T12:00:00Z" {
		t.Errorf("name = %q", got.Name)
	}
	if !got.ExpiresOn.Equal(want) {
		t.Errorf("expires_on = %v, want %v", got.ExpiresOn, want)
	}
	// Exactly the prototype's rights: effect, resources, permission group ids;
	// what Cloudflare generated (policy id, group name and meta) is left behind.
	wantPolicies := `[{"effect":"allow","resources":{"com.cloudflare.api.account.zone.z1":"*"},"permission_groups":[{"id":"g-dns"}]}]`
	if string(got.Policies) != wantPolicies {
		t.Errorf("policies = %s\nwant      %s", got.Policies, wantPolicies)
	}
	var cond map[string]json.RawMessage
	if err = json.Unmarshal(got.Condition, &cond); err != nil || string(cond[cloudflare.ConditionIPKey]) != `{"in":["203.0.113.0/24"],"not_in":[]}` || len(cond) != 1 {
		t.Errorf("condition = %s (%v)", got.Condition, err)
	}
	if rec := e.rec.Find("roster.cloudflare.token.minted"); len(rec) != 1 {
		t.Errorf("minted records = %d", len(rec))
	}
}

func TestAnR2PresetStoresAnAccessKeyAndTheHashOfTheValue(t *testing.T) {
	e := setup(t)
	if res := e.m.TickPreset(ctx, "r2"); res.Err != nil {
		t.Fatal(res.Err)
	}
	doc, _, err := e.external("r2").Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if doc.AccessKeyID != "tok001" || doc.SecretAccessKey != cloudflare.R2Secret("value-tok001") || doc.Token != "" ||
		doc.Endpoint != "https://"+acct+".r2.cloudflarestorage.com" {
		t.Fatalf("stored = %+v", doc)
	}
}

func TestATickOnlyRotatesWhenTheStoredTokenIsOlderThanRotation(t *testing.T) {
	e := setup(t)
	e.m.TickPreset(ctx, "dns")
	lists, deletes := e.api.lists, e.api.deletes
	e.advance(4 * time.Minute)
	if res := e.m.TickPreset(ctx, "dns"); res.Outcome != minter.OutcomeFresh || e.api.creates != 1 {
		t.Fatalf("early tick = %+v, creates %d", res, e.api.creates)
	}
	if e.api.lists != lists || e.api.deletes != deletes {
		t.Errorf("a fresh tick spent API calls: lists %d deletes %d", e.api.lists, e.api.deletes)
	}
	e.advance(1 * time.Minute)
	if res := e.m.TickPreset(ctx, "dns"); res.Outcome != minter.OutcomeRotated || e.api.creates != 2 {
		t.Fatalf("due tick = %+v, creates %d", res, e.api.creates)
	}
	doc, _, _ := e.external("dns").Get(ctx)
	if doc.Token != "value-tok002" {
		t.Errorf("stored = %+v", doc)
	}
}

func TestSweepDeletesByRecordedIdAndOnlyThePresetsOwnExpiredTokens(t *testing.T) {
	e := setup(t, config.CloudflareGrant{Group: "g", Presets: []string{"dns", "dns-edge"}})
	person := minter.Caller{Actor: audit.Person("u@example.com"), Groups: []string{"g"}}
	short, _ := e.m.MintFor(ctx, "dns", person, time.Minute)
	long, _ := e.m.MintFor(ctx, "dns", person, 10*time.Minute)
	sibling, _ := e.m.MintFor(ctx, "dns-edge", person, time.Minute)
	recorded, err := e.m.Recorded(ctx, "dns")
	if err != nil || len(recorded) != 2 {
		t.Fatalf("recorded = %+v %v", recorded, err)
	}
	// Someone's hand puts foreign ids into the record: the name check stops them.
	e.api.put(cloudflare.Token{ID: "foreign", Name: "ci-deploy-key", ExpiresOn: e.clock.Add(time.Minute)})
	if err = e.m.TestOnlyTrack(ctx, "dns", "foreign", e.clock.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = e.m.TestOnlyTrack(ctx, "dns", sibling.TokenID, e.clock.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	e.advance(2 * time.Minute)
	// The account no longer lists the expired tokens: only the record finds them.
	live, _ := e.api.ListTokens(ctx)
	for _, l := range live {
		if l.ID == short.TokenID {
			t.Fatal("the fake lists an expired token")
		}
	}
	swept, err := e.m.Sweep(ctx, "dns")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(swept, ",") != short.TokenID {
		t.Fatalf("swept = %v, want only %s", swept, short.TokenID)
	}
	names := e.api.names()
	for _, keep := range []string{long.TokenID, sibling.TokenID, "foreign", dnsProto, r2Proto} {
		if _, ok := names[keep]; !ok {
			t.Errorf("%s was deleted", keep)
		}
	}
	if _, ok := names[short.TokenID]; ok {
		t.Error("the expired token is still in the account")
	}
	// What was swept, and what turned out not to be ours, left the record.
	rest, _ := e.m.Recorded(ctx, "dns")
	if len(rest) != 1 || rest[0].ID != long.TokenID {
		t.Errorf("record after the sweep = %+v", rest)
	}
	if rec := e.rec.Find("roster.cloudflare.tokens.swept"); len(rec) != 1 {
		t.Errorf("swept records = %d", len(rec))
	}
	// A second sweep has nothing to do and makes no call.
	deletes := e.api.deletes
	if swept, _ = e.m.Sweep(ctx, "dns"); len(swept) != 0 || e.api.deletes != deletes {
		t.Errorf("second sweep = %v", swept)
	}
}

func TestARotationSweepsAfterItStoresTheNewToken(t *testing.T) {
	e := setup(t)
	e.m.TickPreset(ctx, "dns") // tok001, expires in 15m
	e.advance(16 * time.Minute)
	res := e.m.TickPreset(ctx, "dns")
	if res.Err != nil || len(res.Swept) != 1 || res.Swept[0] != "tok001" {
		t.Fatalf("tick = %+v", res)
	}
	if _, ok := e.api.names()["tok002"]; !ok {
		t.Error("the new token was swept")
	}
	if rest, _ := e.m.Recorded(ctx, "dns"); len(rest) != 1 || rest[0].ID != "tok002" {
		t.Errorf("record = %+v", rest)
	}
}

func TestAnActivePrototypeIsRefused(t *testing.T) {
	e := setup(t)
	p, _ := e.api.GetToken(ctx, dnsProto)
	p.Status = cloudflare.StatusActive
	e.api.put(p)
	res := e.m.TickPreset(ctx, "dns")
	pe, ok := cloudflare.IsPrototypeError(res.Err)
	if !ok || pe.Reason != cloudflare.ReasonPrototypeActive || e.api.creates != 0 {
		t.Fatalf("tick = %+v, creates %d", res, e.api.creates)
	}
	if _, _, err := e.external("dns").Get(ctx); err == nil {
		t.Error("a document was stored")
	}
	if rec := e.rec.Find("roster.cloudflare.token.refused"); len(rec) != 1 {
		t.Errorf("refused records = %d", len(rec))
	}
}

func TestAPrototypeGrantingAForbiddenGroupIsRefused(t *testing.T) {
	for _, group := range []string{"g-tok", "g-bill"} {
		e := setup(t)
		p, _ := e.api.GetToken(ctx, dnsProto)
		p.Policies = policiesJSON(group)
		e.api.put(p)
		res := e.m.TickPreset(ctx, "dns")
		pe, ok := cloudflare.IsPrototypeError(res.Err)
		if !ok || pe.Reason != cloudflare.ReasonPrototypeForbidden || e.api.creates != 0 {
			t.Errorf("%s: tick = %+v", group, res)
		}
	}
}

func TestARefusalFoundByCheckIsAnAuditEventAndAMetricNotJustAReturn(t *testing.T) {
	e := setup(t)
	p, _ := e.api.GetToken(ctx, dnsProto)
	p.Policies = policiesJSON("g-bill")
	e.api.put(p)
	if got := e.m.Check(ctx); got["dns"] == nil {
		t.Fatalf("check = %v", got)
	}
	if n := len(e.rec.Find("roster.cloudflare.token.refused")); n != 2 { // dns and dns-edge share the prototype
		t.Errorf("refused records = %d, want 2", n)
	}
}

func TestAMissingPrototypeAndAMissingMinterAreRefusedPlainly(t *testing.T) {
	e := setup(t)
	e.api.mu.Lock()
	delete(e.api.tokens, dnsProto)
	e.api.mu.Unlock()
	pe, ok := cloudflare.IsPrototypeError(e.m.TickPreset(ctx, "dns").Err)
	if !ok || pe.Reason != cloudflare.ReasonPrototypeMissing {
		t.Fatalf("missing prototype: %v", pe)
	}
	e = setup(t)
	if _, err := e.minterStore().Get(ctx, "minter"); err != nil {
		t.Fatal(err)
	}
	if err := e.minterStore().Delete(ctx, "minter"); err != nil {
		t.Fatal(err)
	}
	if res := e.m.TickPreset(ctx, "dns"); !errors.Is(res.Err, minter.ErrNoMinter) {
		t.Fatalf("missing minter: %+v", res)
	}
}

func TestOnDemandMintingChecksTheGrantsAndTheLifetime(t *testing.T) {
	e := setup(t,
		config.CloudflareGrant{Group: "all:infra:dns", Presets: []string{"dns"}},
		config.CloudflareGrant{Group: "all:ci:release", Presets: []string{"dns"}})
	person := minter.Caller{Actor: audit.Person("user@example.com"), Groups: []string{"all:infra:dns"}}

	got, err := e.m.MintFor(ctx, "dns", person, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "sluis/example/dns/user@example.com/2026-10-08T12:00:00Z" || !got.ExpiresOn.Equal(e.clock.Add(5*time.Minute)) || got.Token != "value-tok001" {
		t.Fatalf("minted = %+v", got)
	}
	// The default is the preset's lifetime.
	if got, err = e.m.MintFor(ctx, "dns", person, 0); err != nil || !got.ExpiresOn.Equal(e.clock.Add(15*time.Minute)) {
		t.Fatalf("default lifetime: %+v %v", got, err)
	}
	// A job.
	job := minter.Caller{Actor: audit.CI("github:example-org/example-repo"), Groups: []string{"all:ci:release"}}
	if got, err = e.m.MintFor(ctx, "dns", job, 0); err != nil || !strings.Contains(got.Name, "github:example-org_example-repo") {
		t.Fatalf("job: %+v %v", got, err)
	}
	// Refusals.
	if _, err = e.m.MintFor(ctx, "dns", person, time.Hour); !errors.Is(err, minter.ErrLifetime) {
		t.Errorf("long lifetime: %v", err)
	}
	if _, err = e.m.MintFor(ctx, "dns", person, time.Second); !errors.Is(err, minter.ErrLifetime) {
		t.Errorf("short lifetime: %v", err)
	}
	stranger := minter.Caller{Actor: audit.Person("x@example.com"), Groups: []string{"other"}}
	if _, err = e.m.MintFor(ctx, "dns", stranger, 0); !errors.Is(err, minter.ErrNotGranted) {
		t.Errorf("stranger: %v", err)
	}
	if _, err = e.m.MintFor(ctx, "r2", person, 0); !errors.Is(err, minter.ErrNotGranted) {
		t.Errorf("other preset: %v", err)
	}
	if _, err = e.m.MintFor(ctx, "nope", person, 0); !errors.Is(err, minter.ErrUnknownPreset) {
		t.Errorf("unknown: %v", err)
	}
	if e.api.creates != 3 {
		t.Errorf("creates = %d, want 3 (refusals create nothing)", e.api.creates)
	}
	if n := len(e.rec.Find("roster.cloudflare.token.minted")); n != 3 {
		t.Errorf("minted records = %d", n)
	}
	if n := len(e.rec.Find("roster.cloudflare.token.refused")); n != 5 {
		t.Errorf("refused records = %d", n)
	}
	// Nothing was stored for an on-demand mint, and no record carries a value.
	if _, _, err = e.external("dns").Get(ctx); err == nil {
		t.Error("an on-demand mint wrote the stored document")
	}
	for _, r := range e.rec.Records() {
		if strings.Contains(r.String(), "value-tok") {
			t.Errorf("a record carries a token value: %v", r)
		}
	}
}

func TestGrantedListsThePresetsACallerMayHave(t *testing.T) {
	e := setup(t, config.CloudflareGrant{Group: "g", Presets: []string{"r2", "dns"}})
	got := e.m.Granted(minter.Caller{Groups: []string{"g"}})
	if len(got) != 2 || got[0].Name != "dns" || got[1].Name != "r2" || !got[1].R2 || got[1].Endpoint == "" {
		t.Fatalf("granted = %+v", got)
	}
	if len(e.m.Granted(minter.Caller{Groups: []string{"x"}})) != 0 {
		t.Error("a stranger is granted presets")
	}
}

func TestRevokeDeletesOnlyOwnTokensAndReplacesTheStoredOne(t *testing.T) {
	e := setup(t)
	e.m.TickPreset(ctx, "dns") // tok001 is stored
	actor := audit.Person("admin@example.com")

	e.api.put(cloudflare.Token{ID: "foreign", Name: "something else", ExpiresOn: e.clock.Add(time.Hour)})
	if _, err := e.m.Revoke(ctx, "dns", "foreign", actor); !errors.Is(err, minter.ErrNotOurs) {
		t.Errorf("foreign: %v", err)
	}
	if _, err := e.m.Revoke(ctx, "dns", dnsProto, actor); !errors.Is(err, minter.ErrNotOurs) {
		t.Errorf("prototype: %v", err)
	}
	if _, err := e.m.Revoke(ctx, "dns", "absent", actor); !errors.Is(err, minter.ErrNotOurs) {
		t.Errorf("absent: %v", err)
	}
	live, err := e.m.Live(ctx, "dns")
	if err != nil || len(live) != 1 || live[0].ID != "tok001" || !live[0].Stored {
		t.Fatalf("live = %+v %v", live, err)
	}
	res, err := e.m.Revoke(ctx, "dns", "tok001", actor)
	if err != nil || !res.Rotated {
		t.Fatalf("revoke = %+v %v", res, err)
	}
	if _, ok := e.api.names()["tok001"]; ok {
		t.Error("tok001 is still live")
	}
	if doc, _, _ := e.external("dns").Get(ctx); doc.Token != "value-tok002" {
		t.Errorf("stored = %+v", doc)
	}
	if n := len(e.rec.Find("roster.cloudflare.token.revoked")); n != 1 {
		t.Errorf("revoked records = %d", n)
	}
	// An on-demand token is revoked without touching the stored one.
	person := minter.Caller{Actor: audit.Person("u@example.com"), Groups: []string{"g"}}
	e2 := setup(t, config.CloudflareGrant{Group: "g", Presets: []string{"dns"}})
	e2.m.TickPreset(ctx, "dns")
	od, _ := e2.m.MintFor(ctx, "dns", person, 0)
	if res, err = e2.m.Revoke(ctx, "dns", od.TokenID, actor); err != nil || res.Rotated || e2.api.creates != 2 {
		t.Errorf("on-demand revoke = %+v %v creates %d", res, err, e2.api.creates)
	}
}

func TestACreateFailureIsReportedAndNothingIsStored(t *testing.T) {
	e := setup(t)
	e.api.createErr = errors.New("cloudflare says no")
	res := e.m.TickPreset(ctx, "dns")
	if res.Err == nil || res.Outcome != minter.OutcomeFailed {
		t.Fatalf("tick = %+v", res)
	}
	if _, _, err := e.external("dns").Get(ctx); err == nil {
		t.Error("a document was stored")
	}
	if r := e.rec.Find("roster.cloudflare.token.refused"); len(r) != 1 {
		t.Errorf("records = %d", len(r))
	}
	if got := e.m.Tick(ctx); got.Failed() == 0 || got.Err() == nil {
		t.Errorf("Tick = %+v", got)
	}
}

func TestCheckVetsEveryPrototypeWithoutMinting(t *testing.T) {
	e := setup(t)
	p, _ := e.api.GetToken(ctx, r2Proto)
	p.Status = cloudflare.StatusActive
	e.api.put(p)
	got := e.m.Check(ctx)
	if got["dns"] != nil || got["r2"] == nil || e.api.creates != 0 {
		t.Fatalf("check = %v creates %d", got, e.api.creates)
	}
}

// A lease another runner holds means the tick does nothing.
type busyLock struct{}

func (busyLock) Do(context.Context, string, string, func(context.Context)) (bool, error) {
	return false, nil
}

func TestATickWhoseLeaseIsHeldElsewhereDoesNothing(t *testing.T) {
	e := setup(t)
	mc2 := minter.Config{Instance: "example", Cloudflare: goodSection(),
		Lock: busyLock{}, Dial: func(context.Context, string, string) (minter.API, error) { return e.api, nil }}
	e.secretsConfig(&mc2)
	m2, err := minter.New(mc2)
	if err != nil {
		t.Fatal(err)
	}
	if res := m2.TickPreset(ctx, "dns"); res.Outcome != minter.OutcomeContended || e.api.creates != 0 {
		t.Fatalf("tick = %+v", res)
	}
}
