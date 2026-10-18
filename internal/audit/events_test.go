//nolint:lll // messages and fixtures are prose and one-line tables
package audit_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"
	"github.com/truvity/sluis/audit/sdk/record"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/audit/audittest"
)

// every is one record of every action, built the way the code builds them.
// No constructor puts a value in data that the record builder does not
// handle: one that did would vanish from its record.
func TestNoConstructorCarriesAValueTheBuilderDrops(t *testing.T) {
	defer audit.SetUnsupportedDataHook(func(key string, v any) { t.Errorf("data %q has a %T the builder does not handle", key, v) })()
	if len(every()) == 0 {
		t.Fatal("no records")
	}
}

func every() []*record.Record {
	person := audit.Person("A.Person@Example.com")
	app := audit.App{ID: 42, Slug: "roster-example"}
	member := audit.Member{Person: "a.person@example.com", Org: "example", Team: "platform", Login: "@APerson", Role: "member"}
	ch := audit.SlackChannel{Workspace: "acme", Name: "#platform", ID: "C0123", Private: true}
	sm := audit.SlackMember{Person: "a.person@example.com", Channel: ch, User: "U0123", Groups: []string{"platform"}, Reason: "bound to platform"}
	shared := audit.SlackShared{Host: "acme", Guest: "globex", Channel: "partners", ID: "C0456", Invite: "I0789"}
	sw := audit.SlackWorkspace{Key: "acme", Team: "T0123", App: "A0123", Owner: "C0north"}
	sharedChannel := audit.SlackSharedChannel{Name: "partners", Host: "acme", With: []string{"globex", "initech"},
		Sources: []string{"engineers@acme.example"}, Members: []string{"a.person@example.com"},
		PerSide: map[string]bool{"acme": true, "globex": false, "initech": true}}
	consoleChannel := audit.SlackConsoleChannel{Workspace: "acme", Name: "platform", Private: true, Mode: "strict",
		Sources: []string{"engineers@acme.example", "ops@acme.example"}, Members: []string{"A.Person@Example.com"}}
	sa := audit.SlackCatalogueApp{ID: "sync", App: "A0123", Workspace: "acme", Team: "T0123", Scopes: []string{"channels:read", "users:read"}}
	return []*record.Record{
		audit.SlackWorkspaceConnected(person, sw),
		audit.SlackWorkspaceDisconnected(person, sw, true, ""),
		audit.SlackWorkspaceConnectRefused(person, sw, "installed into another team"),
		audit.SlackCatalogueAppCreated(person, audit.SlackCatalogueApp{ID: "sync", App: "A0123", Workspace: "acme"}),
		audit.SlackCatalogueAppInstalled(person, sa),
		audit.SlackCatalogueAppInstallRefused(person, sa, "installed into another workspace"),
		audit.SlackSharedChannelCreated(person, sharedChannel),
		audit.SlackSharedChannelUpdated(person, sharedChannel, "with: globex -> globex,initech"),
		audit.SlackSharedChannelDeleted(person, sharedChannel),
		audit.SlackConsoleChannelCreated(person, consoleChannel),
		audit.SlackConsoleChannelUpdated(person, consoleChannel, "sources: 1 group -> 2 groups; mode: extend -> strict"),
		audit.SlackConsoleChannelDeleted(person, consoleChannel),
		audit.SlackChannelCreated(ch, audit.Succeeded()),
		audit.SlackChannelAdopted(ch, audit.Failed("Slack refused")),
		audit.SlackChannelArchived(person, ch, audit.Succeeded()),
		audit.SlackMemberInvited(sm, audit.Succeeded()),
		audit.SlackMemberRemoved(audit.SlackMember{Channel: ch, User: "U0999"}, audit.Succeeded()),
		audit.SlackSharedInvited(shared, audit.Succeeded()),
		audit.SlackSharedAccepted(shared, audit.Succeeded()),
		audit.SlackActionHeld("acme", nil, &sm, "invite", "the person has no Slack account yet"),
		audit.SlackActionHeld("acme", &ch, nil, "adopt", "the bot is not in the private channel"),
		audit.SlackActionHeld("acme", nil, nil, "remove", "the breaker tripped"),
		audit.SlackRemovalsConfirmed(person, "acme", "", "f00d", 3),
		audit.SlackRemovalsConfirmed(person, "acme", "platform", "f00d", 1),
		audit.SlackLeaverReported("acme", "gone@example.com", "U0999", "no longer in the directory"),
		audit.SignedIn(person, "console", "google", audit.Succeeded()),
		audit.SignedInSession(person, "mcp-host", "google", "agent", time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC), audit.Succeeded()),
		audit.SignedInSession(person, "grafana", "google", "interactive", time.Time{}, audit.Denied("not entitled")),
		audit.RecoverySignedIn(audit.RecoveryIdentity("system:serviceaccount:access:recovery"), "console", "recovery", audit.Succeeded()),
		audit.TokenExchanged(audit.CI("github:example/app"), "aws", "ci", audit.Denied("no group admits it")),
		audit.TokenExchangeRefused(audit.Person("ada@north.example"), "aws:1111:power", "person", audit.Denied("the proof holds no group this client requires"),
			audit.Refusal{
				Code: "not_admitted", Audience: "aws:1111:power", Missing: []string{"mgmt:k8s:admin"}, Held: []string{"devel:k8s:viewer"},
				Directory: "live",
			}),
		audit.SignInRefused(person, "grafana", "google", false, audit.Denied("not entitled"),
			audit.Refusal{Code: "not_admitted", Audience: "grafana", Missing: []string{"a"}, Held: []string{"b"}, Directory: "held"}),
		audit.SignInRefused(audit.RecoveryIdentity("ops"), "console", "recovery", true, audit.Denied("not entitled"),
			audit.Refusal{Code: "not_admitted", Audience: "console", Directory: "not_applicable"}),
		audit.GitHubTokenMinted(audit.Workload("system:serviceaccount:ci:runner"), "roster-example", audit.GitHubToken{
			Proof: "workload", Org: "example", Grant: "all:github:ci", Repositories: []string{"app", "infra"},
			Permissions: "contents:read", Installation: 7, ExpiresAt: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
		}, audit.Succeeded()),
		audit.SessionEnded(person, 2, nil),
		audit.SessionEnded(person, 1, []string{"mcp-host", "mcp-host"}),
		audit.SessionRevoked(person, "b.person@example.com", "grafana", "client", 1),
		audit.SessionsRevokedByClass(person, "a.person@example.com", audit.ScopeEveryAgent, 2, "agent", "interactive"),
		audit.SessionRevoked(person, "", "mcp-host", audit.ScopeClientEveryIdentity, 4),
		audit.SessionReuseRevoked("a.person@example.com", "grafana"),
		audit.AccessExplained(person, audit.Succeeded(), "person", "b.person@example.com", 3, 5, 2),
		audit.AccessExplained(person, audit.Denied("not authorised"), "ci", "github:example/app", 0, 0, 0),
		audit.SessionRefreshRefused("b.person@example.com", "grafana", "no longer admitted"),
		audit.ClientSecretCreated("grafana", time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)),
		audit.ClientSecretAdopted("grafana", "input", time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)),
		audit.ClientSecretRotated(person, "grafana", time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC), 24*time.Hour,
			time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), true),
		audit.ClientSecretOrphaned("grafana", time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)),
		audit.ClientSecretDeleted(person, "grafana"),
		audit.CloudflareTokenMinted(person, "dns", audit.CloudflareToken{Variant: audit.CloudflareOnDemand, Account: "main", TokenID: "id1", ExpiresOn: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}),
		audit.CloudflareTokenRefused(person, "dns", audit.CloudflareOnDemand, "not_granted", "", true),
		audit.CloudflareTokensSwept("dns", "main", []string{"id1", "id2"}),
		audit.CloudflareTokenRevoked(person, "dns", "main", "id1"),
		audit.ClientSecretDenied(person, "grafana", "rotate", "busy", nil),
		audit.BackupCompleted(audit.System(), "20261010T020000Z-a1b2c3", audit.BackupRun{Installation: "example", Trigger: "schedule", Modules: 6, Records: 120, Chunks: 9, Resumed: true}),
		audit.BackupCompleted(audit.System(), "20261010T020000Z-d4e5f6", audit.BackupRun{Installation: "example", Trigger: "schedule", Modules: 6, Records: 120, Chunks: 1,
			ObjectKey: "backup/example/20261010T020000Z-d4e5f6.tar.gz.age", ObjectSize: 4096, ObjectSHA256: strings.Repeat("a", 64),
			SidecarSHA256: strings.Repeat("b", 64), Recipients: []string{strings.Repeat("c", 64), strings.Repeat("d", 64)}}),
		audit.RestoreStarted(person, "20261010T020000Z-a1b2c3", audit.Restore{Installation: "example", Unverified: true, Verification: "unavailable"}),
		audit.RestoreUnverified(person, "20261010T020000Z-a1b2c3", audit.Restore{Installation: "example", Modules: 6, Unverified: true, Verification: "unavailable", By: "ada"}),
		audit.BackupFailed(audit.System(), "20261010T020000Z-a1b2c3", audit.BackupRun{Installation: "example", Trigger: "schedule", Reason: "archive"}, "the archive bucket refused a write"),
		audit.BackupPruned(audit.System(), []string{"20260901T020000Z-aaaaaa"}, []string{"20260902T020000Z-bbbbbb"}, 7, "keep 7, 30d"),
		audit.SecretsRekeyed(person, audit.SecretsRekey{Installation: "example", Key: "alias/example-secrets", Phase: audit.RekeyStarted,
			Considered: 9, Planned: 2, Deferred: 1, Names: []string{"/sluis/example/internal/oidc/a", "/sluis/example/external/oidc/b"}}, audit.Succeeded()),
		audit.SecretsRekeyed(person, audit.SecretsRekey{Installation: "example", Key: "alias/example-secrets", Phase: audit.RekeyCompleted,
			Considered: 9, Planned: 2, Rekeyed: 1}, audit.Failed("write refused")),
		audit.RestoreStarted(person, "20261010T020000Z-a1b2c3", audit.Restore{Installation: "example"}),
		audit.RestoreCompleted(person, "20261010T020000Z-a1b2c3", audit.Restore{Installation: "example", Modules: 6}),
		audit.RestoreFailed(person, "20261010T020000Z-a1b2c3", audit.Restore{Installation: "example", Reason: "verify"}, "a record differs after the write"),
		audit.MaintenanceChanged(person, audit.Maintenance{Installation: "example", State: "migration", Modules: 6, Reason: "layout v5 move"}),
		audit.RecoveryDisabled(person, "example", "2026-10", 2, 3, false, audit.Succeeded()),
		audit.RecoveryDisabled(person, "example", "", 0, 0, true, audit.Failed("the switch could not be kept")),
		audit.MaintenanceChanged(person, audit.Maintenance{Installation: "example", State: audit.MaintenanceCleared, Modules: 1, Forced: true}),
		audit.MaintenanceFailed(person, audit.Maintenance{Installation: "example", State: "migration", Modules: 6}, "a table refused the flag"),
		audit.SecretsPruned(person, audit.SecretsPrune{Installation: "example", From: "example-v4", Phase: audit.PhaseStarted, Plan: "abc123",
			Planned: 2, Kept: 3, Names: []string{"internal/config/a", "external/b"}}, audit.Succeeded()),
		audit.SecretsPruned(person, audit.SecretsPrune{Installation: "example", From: "example-v4", Phase: audit.PhaseCompleted, Plan: "abc123",
			Planned: 2, Deleted: 1, Kept: 3}, audit.Failed("delete refused")),
		audit.MigrationCopied(person, audit.MigrationCopy{Installation: "example", From: "example-v4", Phase: audit.PhaseStarted, Overwrite: true,
			Written: 5, Replaced: 1, ReplacedNames: []string{"oidc/secret/x"}}, audit.Succeeded()),
		audit.WorkspaceConnected(person, "ws-1", "google", "consent"),
		audit.WorkspaceReconnected(person, "ws-1", "google", "consent"),
		audit.WorkspaceDisconnected(person, "ws-1"),
		audit.WorkspaceDomainsChanged(person, "ws-1", []string{"example.com"}),
		audit.WorkspaceGroupsChanged(person, "ws-1", 3),
		audit.GitHubAppCreated(person, "example", app),
		audit.GitHubOrgConnected(person, "example", 42, 7, "C0north"),
		audit.GitHubPassRequested(person, "example"),
		audit.GitHubOrgOwnerChanged(person, "example", "", "C0north"),
		audit.SlackWorkspaceOwnerChanged(person, "acme", "C0north", ""),
		audit.GitHubOrgDisconnected(audit.System(), "example", true, "the App was uninstalled"),
		audit.GitHubRemovalsConfirmed(person, "example", "f00d", 3, 40),
		audit.LinkAppConnected(person, app, "example"),
		audit.LinkAppDisconnected(person, app, 5),
		audit.CatalogueAppCreated(person, "example", app),
		audit.CatalogueAppInstalled(person, "example", app, 7),
		audit.CatalogueAppDisconnected(person, "example", app, false, ""),
		audit.CatalogueAppWebhookChanged(person, "example", app, audit.WebhookRotated, audit.Succeeded()),
		audit.CatalogueAppWebhookChanged(person, "example", app, audit.WebhookVerified, audit.Failed("the target answered 401")),
		audit.RunnerAppCreated(person, "example", app),
		audit.RunnerAppInstalled(person, "example", app, 7),
		audit.RunnerAppDisconnected(audit.System(), "example", audit.App{Slug: "runners", Tier: "stable"}, true, ""),
		audit.GitHubLinkCreated("a.person@example.com", "@APerson"),
		audit.GitHubLinkMatched("a.person@example.com", "aperson", "the work address its profile publishes"),
		audit.GitHubLinkImported(person, "b.person@example.com", "bperson", "imported"),
		audit.GitHubLinkMoved("a.person@example.com", "aperson", "moved"),
		audit.GitHubLinkNarrowed("a.person@example.com", "aperson", "narrowed"),
		audit.GitHubLinkUnverifiable("a.person@example.com", "aperson", "the token was revoked"),
		audit.GitHubLinkLost("a.person@example.com", "aperson", "the account is gone"),
		audit.GitHubMemberInvited(member, audit.Succeeded()),
		audit.GitHubMemberAdded(member, audit.Succeeded()),
		audit.GitHubMemberRoleSet(member, audit.Failed("GitHub refused")),
		audit.GitHubMemberRemoved(audit.Member{Org: "example", Login: "gone"}, audit.Succeeded()),
		audit.GitHubMemberHeld(member, "remove", "the directory cannot vouch for example.com"),
		audit.GitHubOwnerReported(audit.Member{Org: "example", Login: "owner"}, "remove", "owners are reported, not removed"),
		audit.GitHubOwnerReported(audit.Member{Org: "example", Team: "platform", Login: "owner"}, "remove", "owners are reported, not removed"),
	}
}

// Every constructor makes a record the installation's catalogue accepts, and
// every action the catalogue declares has a constructor: the vocabulary is
// the catalogue, no more and no less.
func TestTheConstructorsAreTheCatalogue(t *testing.T) {
	rec := audittest.New(t)
	built := map[string]bool{}
	for _, r := range every() {
		rec.Record(context.Background(), r)
		built[r.GetAction()] = true
	}
	c, _, err := audit.Catalogue()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range c.ActionNames() {
		if !built[name] {
			t.Errorf("%s is declared and no constructor builds it", name)
		}
	}
	if got, want := len(rec.Records()), len(every()); got != want {
		t.Fatalf("kept %d of %d records", got, want)
	}
}

// The installation fills operation from the catalogue, and the tenant is the
// installation's own.
func TestRecordsAreTheInstallationsOwn(t *testing.T) {
	rec := audittest.New(t)
	rec.Record(context.Background(), audit.SignedIn(audit.Person("a@example.com"), "console", "google", audit.Succeeded()))
	r := rec.Records()[0]
	if r.GetTenantId() != record.TenantPlatform {
		t.Fatalf("tenant %q", r.GetTenantId())
	}
	if r.GetOperation() != auditv1.Operation_OPERATION_AUTHENTICATION {
		t.Fatalf("operation %v", r.GetOperation())
	}
	if r.GetSource() != audit.Source {
		t.Fatalf("source %q", r.GetSource())
	}
}

// Addresses are compared as the directory compares them, and a GitHub login
// is named without the @ the console shows.
func TestIdentifiersAreNormalised(t *testing.T) {
	r := audit.GitHubLinkCreated("A.Person@Example.COM", "@APerson")
	if got := r.GetActor().GetId(); got != "a.person@example.com" {
		t.Fatalf("actor %q", got)
	}
	if got := r.GetTargets()[0].GetId(); got != "aperson" {
		t.Fatalf("account %q", got)
	}
}

// A person's e-mail address is an identifier the profiles treat, never data:
// nothing a constructor writes into data looks like one.
func TestNoAddressIsCarriedAsData(t *testing.T) {
	for _, r := range every() {
		for name, v := range r.GetData().GetFields() {
			if strings.Contains(v.GetStringValue(), "@") {
				t.Errorf("%s carries an address in data.%s", r.GetAction(), name)
			}
			for _, item := range v.GetListValue().GetValues() {
				if strings.Contains(item.GetStringValue(), "@") {
					t.Errorf("%s carries an address in data.%s", r.GetAction(), name)
				}
			}
		}
	}
}

// Without an installation, recording validates and logs, and a block action
// still succeeds: refusing recovery on a deployment that keeps no trail would
// make recovery impossible by configuration.
func TestAnUnconnectedTrailLogsAndAllowsRecovery(t *testing.T) {
	trail, err := audit.Open(context.Background(), audit.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	if trail.Connected() {
		t.Fatal("no installation was configured")
	}
	err = trail.RecordDurable(context.Background(),
		audit.RecoverySignedIn(audit.RecoveryIdentity("recovery"), "console", "recovery", audit.Succeeded()))
	if err != nil {
		t.Fatal(err)
	}
}

// A connected trail is an address and a token: the installation records
// nothing from a caller it cannot name, so a connection without one is
// refused rather than started and found wanting later.
func TestAConnectionNeedsATokenToBeNamedBy(t *testing.T) {
	_, err := audit.Open(context.Background(), audit.Config{Writer: "http://w"})
	if err == nil || !strings.Contains(err.Error(), "token file") {
		t.Errorf("%v, want a refusal naming the token file", err)
	}
}

// With no address there is no installation, and that is a deployment keeping
// its trail in the log alone rather than a mistake.
func TestNoAddressIsNotAnError(t *testing.T) {
	trail, err := audit.Open(context.Background(), audit.Config{})
	if err != nil {
		t.Fatalf("an unconnected trail was refused: %v", err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	if err := trail.RecordDurable(context.Background(),
		audit.RecoverySignedIn(audit.RecoveryIdentity("recovery"), "console", "recovery", audit.Succeeded()),
	); err != nil {
		t.Errorf("an unconnected trail refused a block record: %v", err)
	}
}

// A test recorder that cannot take a block action says so, the way the
// installation would.
func TestTheTestRecorderCanRefuse(t *testing.T) {
	rec := audittest.New(t)
	rec.Fail = errors.New("the writer is down")
	err := rec.RecordDurable(context.Background(),
		audit.RecoverySignedIn(audit.RecoveryIdentity("recovery"), "console", "recovery", audit.Succeeded()))
	if err == nil || len(rec.Records()) != 0 || slices.Contains(rec.Actions(), "") {
		t.Fatalf("err %v, records %d", err, len(rec.Records()))
	}
}

// The Slack records name the place and the person the way the GitHub ones do.
func TestSlackRecordsAreNamedByPlaceAndPerson(t *testing.T) {
	ch := audit.SlackChannel{Workspace: "acme", Name: "#platform", ID: "C0123", Private: true}
	r := audit.SlackMemberInvited(audit.SlackMember{Person: "A.Person@Example.com", Channel: ch, User: "U0123"}, audit.Succeeded())
	if r.GetSubject().GetId() != "a.person@example.com" {
		t.Fatalf("subject %q", r.GetSubject().GetId())
	}
	var got []string
	for _, tg := range r.GetTargets() {
		got = append(got, tg.GetType()+":"+tg.GetId())
	}
	if want := []string{"slack_workspace:acme", "slack_channel:acme/platform", "slack_user:U0123"}; !slices.Equal(got, want) {
		t.Fatalf("targets %v", got)
	}
	if r.GetActor().GetKind() != "system" {
		t.Fatalf("actor %v", r.GetActor())
	}
	held := audit.SlackActionHeld("acme", nil, nil, "remove", "breaker")
	if held.GetOutcome().GetResult() != auditv1.Outcome_RESULT_FAILURE {
		t.Fatalf("held outcome %v", held.GetOutcome())
	}
}

// The channel records carry the same counts, and neither writes the fields
// catalogue 1.6.0 keeps declared only so older records read: `reason` on a
// console channel, `from` on a shared one.
func TestChannelRecordsCountSourcesAndMembersAndWriteNoHistoricalField(t *testing.T) {
	shared := audit.SlackSharedChannel{Name: "partners", Host: "acme", With: []string{"globex"},
		Sources: []string{"a@example.com", "b@example.com"}, Members: []string{"c@example.com"}}
	console := audit.SlackConsoleChannel{Workspace: "acme", Name: "platform", Sources: []string{"a@example.com"},
		Members: []string{"c@example.com", "d@example.com"}}
	for _, tc := range []struct {
		name            string
		r               *record.Record
		sources, member float64
		historical      string
	}{
		{"shared", audit.SlackSharedChannelCreated(audit.System(), shared), 2, 1, "from"},
		{"console", audit.SlackConsoleChannelCreated(audit.System(), console), 1, 2, "reason"},
	} {
		fields := tc.r.GetData().GetFields()
		if got := fields["sources"].GetNumberValue(); got != tc.sources {
			t.Errorf("%s: sources = %v, want %v", tc.name, got, tc.sources)
		}
		if got := fields["members"].GetNumberValue(); got != tc.member {
			t.Errorf("%s: members = %v, want %v", tc.name, got, tc.member)
		}
		if _, written := fields[tc.historical]; written {
			t.Errorf("%s: writes %s, which is historical", tc.name, tc.historical)
		}
	}
}

// An operator's action is kept before it happens: the catalogue declares it
// `block`, which is what makes RecordDurable accept it and a failed delivery
// refuse the command (D266).
func TestOperatorActionsAreBlockActions(t *testing.T) {
	c, _, err := audit.Catalogue()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"roster.maintenance.changed", "roster.secrets.pruned", "roster.migration.copied"} {
		a, ok := c.Action(name)
		if !ok || a.Delivery != "block" {
			t.Errorf("%s = %+v (declared %v), want delivery block", name, a.Delivery, ok)
		}
	}
}

// A prune record carries names and counts, with room for a whole installation's
// worth of names, and the recorder refuses a record the catalogue would.
func TestAPruneRecordHoldsEveryNameAndNoValue(t *testing.T) {
	var names []string
	for i := range 300 {
		names = append(names, "internal/config/provider/p"+strings.Repeat("x", 20)+string(rune('a'+i%26)))
	}
	rec := audittest.New(t)
	r := audit.SecretsPruned(audit.Person("oleg@example.com"),
		audit.SecretsPrune{Installation: "example", From: "v4", Phase: audit.PhaseStarted, Plan: "abc", Planned: 300, Names: names}, audit.Succeeded())
	if err := rec.RecordDurable(context.Background(), r); err != nil {
		t.Fatalf("a prune of 300 names: %v", err)
	}
	got := rec.Find("roster.secrets.pruned")
	if len(got) != 1 || len(got[0].GetData().GetFields()["names"].GetListValue().GetValues()) != 300 {
		t.Fatalf("records = %v", got)
	}
	if err := rec.RecordDurable(context.Background(), audit.MigrationCopied(audit.Person("oleg@example.com"),
		audit.MigrationCopy{Installation: "example", From: "v4", Phase: audit.PhaseStarted, Overwrite: true, Written: 2, Replaced: 1,
			ReplacedNames: []string{"oidc/secret/recovery/password"}}, audit.Succeeded())); err != nil {
		t.Fatal(err)
	}
}

// A re-key is kept before it writes: the catalogue declares it a block action.
func TestARekeyIsABlockAction(t *testing.T) {
	c, _, err := audit.Catalogue()
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := c.Action("roster.secrets.rekeyed"); !ok || a.Delivery != "block" {
		t.Errorf("roster.secrets.rekeyed = %+v (declared %v), want delivery block", a.Delivery, ok)
	}
	rec := audittest.New(t)
	if err := rec.RecordDurable(context.Background(), audit.SecretsRekeyed(audit.Person("oleg@example.com"),
		audit.SecretsRekey{Installation: "example", Key: "alias/k", Phase: audit.RekeyStarted, Planned: 1, Names: []string{"/sluis/example/a"}}, audit.Succeeded())); err != nil {
		t.Fatal(err)
	}
}

// The writer of a deployment that declares external identifiers opaque refuses
// a batch holding a person target that reads as something typed: an address,
// or anything with whitespace (audit/internal/writer, looksDirect). The rule is
// restated here because audit/ is a module of its own that sluis cannot import
// from; this holds every record of every action to it.
func TestNoPersonTargetReadsAsAnAddress(t *testing.T) {
	c, _, err := audit.Catalogue()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, r := range every() {
		for i, tg := range r.GetTargets() {
			if !c.TargetTypes[tg.GetType()].IsPerson {
				continue
			}
			checked++
			if strings.ContainsAny(tg.GetId(), "@ \t\n") {
				t.Errorf("%s: targets[%d] %s %q reads as an address: the writer refuses it when identifiers are opaque",
					r.GetAction(), i, tg.GetType(), tg.GetId())
			}
		}
	}
	if checked == 0 {
		t.Fatal("no person target found: the check looks at nothing")
	}
}

// Every Slack record, console channels included, is held to the same rule, and
// the member of a channel is the same identifier in each.
func TestSlackConsoleChannelMembersAreMintedIdentifiers(t *testing.T) {
	ch := audit.SlackConsoleChannel{Workspace: "acme", Name: "platform", Members: []string{" A.Person@Example.com ", "a.person@example.com", "b@example.com"}}
	for _, r := range []*record.Record{
		audit.SlackConsoleChannelCreated(audit.System(), ch),
		audit.SlackConsoleChannelUpdated(audit.System(), ch, "members: 1 -> 2"),
		audit.SlackConsoleChannelDeleted(audit.System(), ch),
	} {
		var users []string
		for _, tg := range r.GetTargets() {
			if tg.GetType() == "directory_user" {
				users = append(users, tg.GetId())
			}
		}
		if len(users) != 2 || users[0] == users[1] || strings.ContainsAny(strings.Join(users, ""), "@ ") {
			t.Errorf("%s: directory users %v, want two minted identifiers", r.GetAction(), users)
		}
		if !slices.Contains(users, audit.DirectoryUserID("a.person@example.com")) {
			t.Errorf("%s: %v lacks the identifier of the first member", r.GetAction(), users)
		}
	}
}
