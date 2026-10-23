package controller_test

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/outlineroster/connection"
	"github.com/truvity/sluis/internal/outlineroster/controller"
	"github.com/truvity/sluis/internal/outlineroster/outlinefake"
	"github.com/truvity/sluis/internal/outlineroster/reconcile"
	"github.com/truvity/sluis/internal/outlineroster/status"
	"github.com/truvity/sluis/policy"
)

// A workspace nobody has touched: the managed groups and the collections are
// created, the people who hold the groups are put in them, the groups are
// granted, and everything is audited. The next pass has nothing to do.
func TestAFreshWorkspaceIsBuiltAndThenInSync(t *testing.T) {
	t.Parallel()
	h := newHarness(t, basePolicy, enabled())
	h.o.AddUser("Ann", "ann@example.com", "member")
	h.dir.set("ann@example.com", "all:wiki:acme")

	first := h.pass()
	if first.Tick.Outcome != status.OutcomeApplied {
		t.Fatalf("outcome = %s (%s), want applied", first.Tick.Outcome, first.Tick.Error)
	}
	for _, name := range []string{"all:wiki:admin", "all:wiki:acme", "all:wiki:globex"} {
		g := h.o.GroupByName(name)
		if g == nil || g.Description != reconcile.ManagedDescription {
			t.Errorf("group %s = %+v, want it created with the managed description", name, g)
		}
	}
	ann := h.o.UserByEmail("ann@example.com")
	if !slices.Contains(h.o.GroupByName("all:wiki:acme").Members, ann.ID) {
		t.Error("ann holds all:wiki:acme and is not in its Outline group")
	}
	general, acme := h.o.CollectionByName("General"), h.o.CollectionByName("Acme")
	if general == nil || acme == nil || general.Permission != "" || acme.Permission != "" {
		t.Fatalf("collections = %+v, %+v; want both created private", general, acme)
	}
	if got := acme.Groups[h.o.GroupByName("all:wiki:acme").ID]; got != "read_write" {
		t.Errorf("Acme grants all:wiki:acme %q, want read_write", got)
	}
	if got := general.Groups[h.o.GroupByName("all:wiki:admin").ID]; got != "admin" {
		t.Errorf("General grants all:wiki:admin %q, want admin", got)
	}
	for _, action := range []string{"roster.outline_group.created", "roster.outline_collection.created", "roster.outline_member.added",
		"roster.outline_collection.granted"} {
		if !has(h.audit.Actions(), action) {
			t.Errorf("%s was not recorded; recorded %v", action, h.audit.Actions())
		}
	}

	before := len(h.o.Mutations())
	second := h.pass()
	if second.Tick.Outcome != status.OutcomeInSync || len(h.o.Mutations()) != before {
		t.Errorf("second pass: outcome %s, %d more changes; want in-sync and none", second.Tick.Outcome, len(h.o.Mutations())-before)
	}
	if has(h.audit.Actions(), "roster.outline_collection.adopted") {
		t.Error("a collection the controller created was recorded as adopted")
	}
}

// Disabled, a workspace is derived and reported, and nothing in Outline
// changes.
func TestADryRunChangesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t, basePolicy)
	h.o.AddUser("Ann", "ann@example.com", "member")
	h.dir.set("ann@example.com", "all:wiki:acme")
	report := h.pass()
	if report.Tick.Outcome != status.OutcomeDryRun || report.Enabled {
		t.Errorf("outcome = %s, enabled %v; want a dry run", report.Tick.Outcome, report.Enabled)
	}
	if m := h.o.Mutations(); len(m) != 0 {
		t.Errorf("a dry run changed Outline: %v", h.methods())
	}
	if m := groupMember(report, "all:wiki:acme", "ann@example.com"); m.State != status.StateWillAdd {
		t.Errorf("ann's row = %+v, want will-add", m)
	}
	if c := collectionRow(report, "Acme"); c.State != status.StateWillCreate {
		t.Errorf("Acme = %+v, want will-create", c)
	}
}

// A workspace with no connection record waits, and is not a failure.
func TestAnUnconnectedWorkspaceWaits(t *testing.T) {
	t.Parallel()
	h := newHarness(t, basePolicy, enabled())
	h.rec.connected = false
	if report := h.pass(); report.Tick.Outcome != status.OutcomeWaiting || report.Tick.Error != "" {
		t.Errorf("tick = %+v, want waiting", report.Tick)
	}
}

// More people than one page: every one of them is read and added.
func TestPeoplePastAHundredAreAllManaged(t *testing.T) {
	t.Parallel()
	h := newHarness(t, basePolicy, enabled())
	for i := range 150 {
		email := fmt.Sprintf("person%03d@example.org", i)
		h.o.AddUser(email, email, "member")
		h.dir.set(email, "all:wiki:globex")
	}
	h.pass()
	if got := len(h.o.GroupByName("all:wiki:globex").Members); got != 150 {
		t.Errorf("all:wiki:globex has %d members, want 150", got)
	}
	if n := h.o.Called("users.list"); n < 2 {
		t.Errorf("users.list was called %d times; a list past a hundred takes two pages", n)
	}
}

// A person the directory authoritatively no longer has is suspended, and a
// person who reaches no group the client requires is too; one the directory
// has back is reactivated.
func TestLeaversAreSuspendedAndReturnersReactivated(t *testing.T) {
	t.Parallel()
	h := newHarness(t, basePolicy, enabled())
	gone := h.o.AddUser("Gone", "gone@example.com", "member")
	h.dir.setPerson("gone@example.com", person{authoritative: true, found: false})
	outside := h.o.AddUser("Outside", "outside@example.com", "member")
	h.dir.set("outside@example.com") // found, but holds nothing
	back := h.o.AddUser("Back", "back@example.com", "member")
	back.Suspended = true
	h.dir.set("back@example.com", "all:wiki:acme")

	report := h.pass()
	if !gone.Suspended || !outside.Suspended {
		t.Errorf("suspended: gone %v, outside %v; want both", gone.Suspended, outside.Suspended)
	}
	if back.Suspended {
		t.Error("an account the directory has back, holding a required group, stays suspended")
	}
	if u := userRow(report, "outside@example.com"); !contains(u.Reason, "the wiki client requires") {
		t.Errorf("outside's reason = %q", u.Reason)
	}
	if !has(h.audit.Actions(), "roster.outline_user.suspended") || !has(h.audit.Actions(), "roster.outline_user.activated") {
		t.Errorf("recorded %v, want a suspension and an activation", h.audit.Actions())
	}
}

// Within the hold window, a directory that cannot vouch changes nothing for
// the person: no removal, no demotion, no suspension; the row retries.
func TestADirectoryThatCannotVouchChangesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t, basePolicy, enabled())
	carol := h.o.AddUser("Carol", "carol@example.com", "admin")
	dave := h.o.AddUser("Dave", "dave@example.com", "member")
	h.dir.set("carol@example.com", "all:wiki:admin", "all:wiki:acme")
	h.dir.set("dave@example.com", "all:wiki:acme")
	h.pass()
	if !slices.Contains(h.o.GroupByName("all:wiki:acme").Members, dave.ID) {
		t.Fatal("setup: dave was not added")
	}

	// The directory goes away: dave's answer is not authoritative and holds
	// nothing, carol's cannot be had at all.
	h.dir.setPerson("dave@example.com", person{authoritative: false})
	h.dir.setPerson("carol@example.com", person{err: errUnreachable})
	before := len(h.o.Mutations())
	report := h.pass()
	if len(h.o.Mutations()) != before {
		t.Errorf("an unreachable directory changed Outline: %v", h.methods()[before:])
	}
	if dave.Suspended || carol.Suspended || carol.Role != "admin" {
		t.Error("an answer the directory could not vouch for suspended or demoted somebody")
	}
	if u := userRow(report, "dave@example.com"); u.State != status.StateRetrying {
		t.Errorf("dave = %+v, want retrying", u)
	}
	if u := userRow(report, "carol@example.com"); u.State != status.StateRetrying {
		t.Errorf("carol = %+v, want retrying", u)
	}
	if m := groupMember(report, "all:wiki:acme", "dave@example.com"); m.State != status.StateRetrying {
		t.Errorf("dave's membership = %+v, want retrying", m)
	}
}

// A collection manager adds a person of another company to a strict
// collection by hand, and a group the policy does not name: the next pass
// takes both out and records it.
func TestAStrictCollectionRevertsAShareByHand(t *testing.T) {
	t.Parallel()
	h := newHarness(t, basePolicy, enabled())
	for i := range 3 {
		email := fmt.Sprintf("a%d@example.com", i)
		h.o.AddUser(email, email, "member")
		h.dir.set(email, "all:wiki:acme")
	}
	h.pass()
	acme := h.o.CollectionByName("Acme")
	stranger := h.o.AddUser("Stranger", "stranger@example.org", "member")
	h.dir.set("stranger@example.org", "all:wiki:globex")
	acme.Users[stranger.ID] = "read_write"

	report := h.pass()
	if _, still := acme.Users[stranger.ID]; still {
		t.Error("the person added by hand is still a direct member of a strict collection")
	}
	if !has(h.audit.Actions(), "roster.outline_collection.member_removed") {
		t.Errorf("recorded %v, want the removal", h.audit.Actions())
	}
	if _, kept := acme.Users[h.o.UserByEmail("key.owner@example.com").ID]; !kept {
		t.Error("the API key's own user was removed from a collection it created")
	}
	if c := collectionRow(report, "Acme"); c.State != status.StateOK {
		t.Errorf("Acme = %+v", c)
	}
}

// One line binding another company's group to a collection that names a
// directory holds the collection, and nobody is added to that group.
func TestTheDirectoryCheckHoldsACollection(t *testing.T) {
	t.Parallel()
	text := strings.Replace(basePolicy, "Acme: { mode: strict, directory: example.com, read_write: [all:wiki:acme] }",
		"Acme: { mode: strict, directory: example.com, read_write: [all:wiki:acme, all:wiki:globex] }", 1)
	h := newHarness(t, text, enabled())
	h.o.AddUser("Gina", "gina@example.org", "member")
	h.dir.set("gina@example.org", "all:wiki:globex")
	report := h.pass()
	c := collectionRow(report, "Acme")
	if c.State != status.StateHeld || !contains(c.Reason, "example.org") {
		t.Errorf("Acme = %+v, want held for the domain example.com does not serve", c)
	}
	if h.o.CollectionByName("Acme") != nil {
		t.Error("a held collection was created")
	}
	if m := groupMember(report, "all:wiki:globex", "gina@example.org"); m.State != status.StateHeld {
		t.Errorf("gina's addition = %+v, want held", m)
	}
	if g := h.o.GroupByName("all:wiki:globex"); g != nil && len(g.Members) > 0 {
		t.Error("somebody was added to a group a held collection is bound to")
	}
}

// The API key's own user stops being an admin: the whole workspace is held,
// and nothing is changed.
func TestAKeyUserWhoIsNotAnAdminHoldsTheWorkspace(t *testing.T) {
	t.Parallel()
	h := newHarness(t, basePolicy, enabled())
	other := h.o.AddUser("Other", "other@example.com", "admin")
	h.dir.set("other@example.com", "all:wiki:admin", "all:wiki:acme")
	h.o.UserByEmail("key.owner@example.com").Role = "member"
	_ = other
	report := h.pass()
	if report.Held != reconcile.KeyUserNotAdmin || report.Tick.Outcome != status.OutcomeHeld {
		t.Errorf("held %q, outcome %s; want the workspace held", report.Held, report.Tick.Outcome)
	}
	if m := h.o.Mutations(); len(m) != 0 {
		t.Errorf("a held workspace changed Outline: %v", h.methods())
	}
	if !has(h.audit.Actions(), "roster.outline_action.held") {
		t.Errorf("the hold was not recorded: %v", h.audit.Actions())
	}
}

// A 429 fails the tick, which changes nothing; the next pass goes ahead.
func TestARateLimitIsTriedAgainNextPass(t *testing.T) {
	t.Parallel()
	h := newHarness(t, basePolicy, enabled())
	h.o.Fail("users.list", outlinefake.Failure{Status: http.StatusTooManyRequests, RetryAfter: 30})
	report := h.pass()
	if report.Tick.Outcome != status.OutcomeFailed || !contains(report.Tick.Error, "rate_limit_exceeded") {
		t.Errorf("tick = %+v, want failed on the rate limit", report.Tick)
	}
	if len(h.o.Mutations()) != 0 {
		t.Error("a tick that met a 429 changed Outline")
	}
	if report = h.pass(); report.Tick.Outcome != status.OutcomeApplied {
		t.Errorf("the next pass = %+v, want applied", report.Tick)
	}
}

// A 429 in the middle of the changes stops them; the rest retry.
func TestARateLimitStopsTheChanges(t *testing.T) {
	t.Parallel()
	h := newHarness(t, basePolicy, enabled())
	h.o.Fail("collections.create", outlinefake.Failure{Status: http.StatusTooManyRequests})
	report := h.pass()
	if report.Tick.Retrying == 0 || h.o.Called("collections.add_group") != 0 {
		t.Errorf("retrying %d, add_group called %d times; want the changes after the 429 left for the next pass",
			report.Tick.Retrying, h.o.Called("collections.add_group"))
	}
	if report = h.pass(); h.o.CollectionByName("General") == nil {
		t.Errorf("the next pass did not finish the work: %+v", report.Tick)
	}
}

// The breakers: each holds its set until an operator confirms its
// fingerprint, and the confirmed set goes ahead.
func TestTheBreakers(t *testing.T) {
	t.Parallel()
	t.Run("a group's removals", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, basePolicy, enabled())
		var users []string
		for i := range 3 {
			email := fmt.Sprintf("g%d@example.org", i)
			users = append(users, h.o.AddUser(email, email, "member").ID)
			h.dir.set(email, "all:wiki:globex")
		}
		h.pass()
		// Two of three leave the group, but stay in the workspace.
		h.dir.set("g0@example.org", "all:wiki:acme")
		h.dir.set("g1@example.org", "all:wiki:acme")
		report := h.pass()
		assertHeldThenConfirmed(t, h, report, connection.GroupScope("all:wiki:globex"), func() bool {
			return len(h.o.GroupByName("all:wiki:globex").Members) == 1
		})
	})
	t.Run("the admins", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, basePolicy, enabled())
		for _, email := range []string{"x@example.com", "y@example.com"} {
			h.o.AddUser(email, email, "admin")
			h.dir.set(email, "all:wiki:acme")
		}
		report := h.pass()
		assertHeldThenConfirmed(t, h, report, connection.ScopeAdmins, func() bool {
			return h.o.UserByEmail("x@example.com").Role == "member" && h.o.UserByEmail("y@example.com").Role == "member"
		})
	})
	t.Run("the suspensions", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, basePolicy, enabled())
		for i := range 4 {
			email := fmt.Sprintf("l%d@example.com", i)
			h.o.AddUser(email, email, "member")
			h.dir.setPerson(email, person{authoritative: true})
		}
		report := h.pass()
		assertHeldThenConfirmed(t, h, report, connection.ScopeSuspensions, func() bool {
			return h.o.UserByEmail("l3@example.com").Suspended
		})
	})
	t.Run("a collection's grants", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, basePolicy, enabled())
		h.pass()
		acme := h.o.CollectionByName("Acme")
		for _, name := range []string{"by-hand-1", "by-hand-2", "by-hand-3"} {
			acme.Groups[h.o.AddGroup(name).ID] = "read"
		}
		report := h.pass()
		assertHeldThenConfirmed(t, h, report, connection.CollectionScope("Acme"), func() bool { return len(acme.Groups) == 1 })
	})
}

func assertHeldThenConfirmed(t *testing.T, h *harness, report status.Workspace, scope string, done func() bool) {
	t.Helper()
	var breaker *status.Breaker
	for i := range report.Breakers {
		if report.Breakers[i].Scope == scope {
			breaker = &report.Breakers[i]
		}
	}
	if breaker == nil || breaker.Confirmed {
		t.Fatalf("breakers = %+v, want %s tripped", report.Breakers, scope)
	}
	if done() {
		t.Fatal("a tripped breaker let its set go ahead")
	}
	if !has(h.audit.Actions(), "roster.outline_breaker.tripped") {
		t.Errorf("the trip was not recorded: %v", h.audit.Actions())
	}
	h.rec.confirmations = append(h.rec.confirmations, connection.Confirmation{
		Version: 1, Workspace: "wiki", Scope: scope, Fingerprint: breaker.Fingerprint, By: "op@example.com", At: h.now,
	})
	h.pass()
	if !done() {
		t.Error("a confirmed set did not go ahead")
	}
}

// Findings are reported and never acted on: drifted settings, a document of a
// company collection shared with someone of another company, and a Slack
// integration posting a company collection's changes.
func TestFindingsAreReportedAndNotActedOn(t *testing.T) {
	t.Parallel()
	h := newHarness(t, basePolicy, enabled(), func(c *controller.Config, _ *policy.Policy) { c.ShareScan = time.Hour })
	h.o.Team.Sharing, h.o.Team.MemberCollectionCreate, h.o.Team.DefaultUserRole = true, true, "viewer"
	h.pass()
	acme := h.o.CollectionByName("Acme")
	outsider := h.o.AddUser("Outsider", "outsider@example.org", "member")
	h.dir.set("outsider@example.org", "all:wiki:globex")
	doc := h.o.AddDocument(acme.ID, "Plans")
	doc.Users[outsider.ID] = "read"
	h.o.Integrations = append(h.o.Integrations, outlinefake.Integration{ID: "int-1", Service: "slack", CollectionID: acme.ID, Channel: "acme-news"})
	h.now = h.now.Add(time.Hour)

	before := len(h.o.Mutations())
	report := h.pass()
	kinds := map[string]int{}
	for _, f := range report.Reported {
		kinds[f.Kind]++
	}
	if kinds[status.FindingSetting] != 3 || kinds[status.FindingShare] != 1 || kinds[status.FindingIntegration] != 1 {
		t.Errorf("reported = %+v, want three settings, one share and one integration", report.Reported)
	}
	if _, still := doc.Users[outsider.ID]; !still {
		t.Error("a reported share was changed")
	}
	for _, c := range h.o.Mutations()[before:] {
		if strings.HasPrefix(c.Method, "documents.") {
			t.Errorf("a finding was acted on: %s", c.Method)
		}
	}
	// The pass after, inside the hour, does not scan and keeps the findings.
	scans := h.o.Called("documents.list")
	if again := h.pass(); h.o.Called("documents.list") != scans || len(again.Reported) != len(report.Reported) {
		t.Errorf("a pass inside the scan interval scanned again or lost the findings: %+v", again.Reported)
	}
}

// An existing collection is adopted by name, recorded once, and an extend
// collection keeps what it was given by hand.
func TestAnExistingCollectionIsAdoptedAndExtendKeeps(t *testing.T) {
	t.Parallel()
	text := strings.Replace(basePolicy, "General: { mode: strict,", "General: { mode: extend,", 1)
	h := newHarness(t, text, enabled())
	general := h.o.AddCollection("General", "")
	byHand := h.o.AddGroup("by-hand")
	general.Groups[byHand.ID] = "read"
	h.pass()
	h.pass()
	if n := len(h.audit.Find("roster.outline_collection.adopted")); n != 1 {
		t.Errorf("adopted was recorded %d times, want once", n)
	}
	if _, kept := general.Groups[byHand.ID]; !kept {
		t.Error("an extend collection lost a grant given by hand")
	}
	if got := len(h.o.Collections); got != 2 {
		t.Errorf("%d collections, want General adopted and Acme created", got)
	}
}
