package controller_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/githubapp/githubfake"
	"github.com/truvity/sluis/internal/githubroster/controller"
	"github.com/truvity/sluis/internal/githubroster/orgconfig"
	"github.com/truvity/sluis/internal/githubroster/orgsettings/settingstest"
	"github.com/truvity/sluis/internal/githubroster/status"
)

// The secret's value, which must never appear anywhere but in GitHub's sealed
// copy of it.
const deployKey = "s3cr3t-deploy-key-value"

const settingsDocument = `
org:
  variables:
    GLOBEX_REGION: {value: eu-west-1, visibility: all}
    GLOBEX_HOSTS: {value: "a.example,b.example", visibility: selected, repositories: [shop, web]}
    ACCESS_ROSTER_ISSUER: {visibility: selected, repositories: [shop]}
  secrets:
    GLOBEX_DEPLOY_KEY: {parameter: /globex/sluis/credentials/deploy-key, visibility: private}
  security_configurations:
    baseline:
      description: Scanning on for the shop and the web site.
      settings: {secret_scanning: enabled, dependabot_alerts: enabled}
      attach: {scope: selected, repositories: [shop, web]}
`

func settingsConfig(mode orgconfig.Mode) orgconfig.Config {
	return orgconfig.Config{
		Mode:              mode,
		Read:              orgconfig.Read{RoleARN: "arn:aws:iam::111122223333:role/sluis-read", Region: "eu-west-1", Parameter: "/globex/sluis/github-org"},
		PlatformVariables: []string{"CI_RUNNER_LABEL_*"},
		PlatformValued:    []string{"ACCESS_ROSTER_ISSUER"},
		SecretPrefix:      "/globex/sluis/credentials/",
		Variables: map[string]orgconfig.Variable{
			"CI_RUNNER_LABEL_DEFAULT": {Value: "self-hosted"},
			"ACCESS_ROSTER_ISSUER":    {Value: "https://sluis.example.com"},
		},
	}
}

// syncBuffer is a log sink the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

type settingsRig struct {
	github *githubfake.Org
	// account is the organisation's own account.
	account *settingstest.Source
	audit   *auditLog
	report  *report
	logs    *syncBuffer
	cfg     orgconfig.Config
	// noEntry leaves the organisation out of the controller's settings, and
	// noSource gives the controller no way to read an account.
	noEntry, noSource bool
	// pass runs one pass with a controller kept across passes.
	pass func()
	// restart makes the next pass a new process: it knows only what the
	// report store holds.
	restart func()
}

func newSettingsRig(t *testing.T, cfg orgconfig.Config, doc string) *settingsRig {
	t.Helper()

	github := githubfake.Start(t, "globex")
	github.AddMember("boss", true, "boss@globex.example")
	github.AddRepository("shop")
	github.AddRepository("web")
	github.AddRepository("docs")

	dir := t.TempDir()
	writeCredential(t, dir, "globex")

	account := settingstest.New(doc)
	account.Put("/globex/sluis/credentials/deploy-key", deployKey)

	r := &settingsRig{
		github: github, account: account, audit: &auditLog{audittest.New(t)}, report: &report{}, logs: &syncBuffer{}, cfg: cfg,
	}

	var current *controller.Controller

	build := func() {
		entries := map[string]orgconfig.Config{"globex": r.cfg}
		if r.noEntry {
			entries = nil
		}

		var reader controller.SettingsReader = settingstest.Reader{"globex": account}
		if r.noSource {
			reader = nil
		}

		current = controller.New(controller.Config{
			AppsDir: dir, Enabled: map[string]bool{"globex": false}, OrgSettings: entries,
		}, controller.Deps{
			Log:    slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
			GitHub: github.Client(), Access: &console{policy: testPolicy, holders: map[string]map[string]bool{}},
			Audit: r.audit, Status: r.report, Bindings: bindings, Policy: testPolicy,
			Settings: reader,
			Now:      func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) },
		})
	}

	build()

	r.pass = func() { current.Pass(context.Background()) }
	r.restart = build

	return r
}

func (r *settingsRig) settings(t *testing.T) status.Settings {
	t.Helper()

	got := r.report.org(t, "globex").Settings
	if got == nil {
		t.Fatal("the report has no settings")
	}

	return *got
}

// settingActions are the writes made to variables, secrets and configurations.
func (r *settingsRig) settingActions() []string {
	var out []string

	for _, did := range r.github.Did() {
		if strings.Contains(did, "variable") || strings.Contains(did, "secret") || strings.Contains(did, "config") {
			out = append(out, did)
		}
	}

	return out
}

// settingRecords are the audit records about settings, as "action name outcome".
func (r *settingsRig) settingRecords() []string {
	var out []string

	for _, rec := range r.audit.Records() {
		if !strings.HasPrefix(rec.GetAction(), "roster.github_org_setting") {
			continue
		}

		outcome := strings.ToLower(strings.TrimPrefix(rec.GetOutcome().GetResult().String(), "RESULT_"))
		d := rec.GetData().GetFields()
		out = append(out, fmt.Sprintf("%s %s/%s %s %s", rec.GetAction(), d["kind"].GetStringValue(), d["name"].GetStringValue(),
			d["action"].GetStringValue()+d["outcome"].GetStringValue(), outcome+" "+d["mode"].GetStringValue()))
	}

	return out
}

// nowhereButSealed fails when the secret's value is in the logs, the audit
// trail or the report: GitHub's own sealed copy is the one place it goes.
func (r *settingsRig) nowhereButSealed(t *testing.T) {
	t.Helper()

	if strings.Contains(r.logs.String(), deployKey) {
		t.Errorf("the secret's value is in the logs:\n%s", r.logs.String())
	}

	for _, rec := range r.audit.Records() {
		if strings.Contains(rec.String(), deployKey) {
			t.Errorf("the secret's value is in an audit record: %s", rec.String())
		}
	}

	if documents, _ := r.report.Reports(context.Background()); strings.Contains(fmt.Sprint(documents), deployKey) {
		t.Errorf("the secret's value is in the report: %v", documents)
	}
}

// report mode: the document is read and checked and what differs is said,
// and GitHub is not written to.
func TestReportModeChangesNothing(t *testing.T) {
	r := newSettingsRig(t, settingsConfig(""), settingsDocument)
	r.github.SetVariable("FOREIGN", githubfake.Variable{Value: "keep", Visibility: "all"})

	r.pass()

	if did := r.settingActions(); len(did) != 0 {
		t.Fatalf("report mode wrote to GitHub: %v", did)
	}

	got := r.settings(t)
	if got.Mode != "report" || got.Document.Outcome != status.DocumentApplied {
		t.Fatalf("settings = %+v, want report mode and an applied document", got)
	}

	var rows []string
	for _, c := range got.Changes {
		if c.State != status.SettingDrift {
			t.Errorf("%+v: a report-mode row is drift", c)
		}

		rows = append(rows, c.Kind+"/"+c.Name+" "+c.Action)
	}

	for _, want := range []string{
		"variable/GLOBEX_REGION create", "variable/GLOBEX_HOSTS create", "variable/ACCESS_ROSTER_ISSUER create",
		"variable/CI_RUNNER_LABEL_DEFAULT create", "secret/GLOBEX_DEPLOY_KEY create",
		"security_configuration/baseline create", "security_configuration/baseline attach",
	} {
		if !slices.Contains(rows, want) {
			t.Errorf("changes = %v, want %q", rows, want)
		}
	}

	if len(got.Managed.Variables) != 0 || len(got.Managed.Secrets) != 0 || len(got.Managed.SecurityConfigurations) != 0 {
		t.Errorf("report mode recorded managed names: %+v", got.Managed)
	}

	// A drift row is recorded once, with the mode, and not again while it stands.
	first := r.settingRecords()
	if len(first) != len(rows) || !strings.Contains(first[0], "success report") {
		t.Fatalf("audit = %v, want one record per drift row in report mode", first)
	}

	r.pass()

	if again := r.settingRecords(); len(again) != len(first) {
		t.Errorf("a standing difference was recorded again: %v", again)
	}

	r.nowhereButSealed(t)
}

// enforce mode applies the document, platform variables included.
func TestEnforceModeApplies(t *testing.T) {
	r := newSettingsRig(t, settingsConfig(orgconfig.ModeEnforce), settingsDocument)
	r.pass()

	shop, web := r.github.RepositoryID("shop"), r.github.RepositoryID("web")

	if v := r.github.Variable("GLOBEX_REGION"); v == nil || v.Value != "eu-west-1" || v.Visibility != "all" {
		t.Errorf("GLOBEX_REGION = %+v", v)
	}

	if v := r.github.Variable("GLOBEX_HOSTS"); v == nil || v.Visibility != "selected" || !slices.Equal(v.Selected, []int64{shop, web}) {
		t.Errorf("GLOBEX_HOSTS = %+v", v)
	}

	// The installation sets the value; the organisation chose the selection.
	if v := r.github.Variable("ACCESS_ROSTER_ISSUER"); v == nil || v.Value != "https://sluis.example.com" || !slices.Equal(v.Selected, []int64{shop}) {
		t.Errorf("ACCESS_ROSTER_ISSUER = %+v", v)
	}

	if v := r.github.Variable("CI_RUNNER_LABEL_DEFAULT"); v == nil || v.Value != "self-hosted" || v.Visibility != "all" {
		t.Errorf("CI_RUNNER_LABEL_DEFAULT = %+v", v)
	}

	if opened, err := r.github.OpenSecret("GLOBEX_DEPLOY_KEY"); err != nil || opened != deployKey {
		t.Errorf("the secret was sealed to GitHub's key as %q, %v", opened, err)
	}

	cfg := r.github.Configuration("baseline")
	if cfg == nil || cfg.Settings["secret_scanning"] != "enabled" {
		t.Fatalf("baseline = %+v", cfg)
	}

	if len(cfg.Attached) != 2 || cfg.Attached[shop] == "" || cfg.Attached[web] == "" {
		t.Errorf("baseline is attached to %v, want the shop and the web site", cfg.Attached)
	}

	got := r.settings(t)
	if got.Mode != "enforce" || got.Document.Outcome != status.DocumentApplied {
		t.Fatalf("settings = %+v", got)
	}

	managed := got.Managed
	if !slices.Equal(managed.SecurityConfigurations, []string{"baseline"}) || len(managed.Variables) != 4 || managed.Secrets["GLOBEX_DEPLOY_KEY"] != 1 {
		t.Errorf("managed = %+v", got.Managed)
	}

	// One audit record per change, with the mode, and never a value.
	records := r.settingRecords()
	for _, want := range []string{
		"roster.github_org_setting.changed variable/GLOBEX_REGION create success enforce",
		"roster.github_org_setting.changed secret/GLOBEX_DEPLOY_KEY create success enforce",
		"roster.github_org_setting.changed security_configuration/baseline create success enforce",
		"roster.github_org_setting.changed security_configuration/baseline attach success enforce",
	} {
		if !slices.Contains(records, want) {
			t.Errorf("audit = %v, want %q", records, want)
		}
	}

	if len(records) != len(got.Changes) {
		t.Errorf("%d audit records for %d changes", len(records), len(got.Changes))
	}

	// A second pass finds nothing to do, and does not write the secret again.
	before := len(r.github.Did())
	r.pass()

	if after := r.github.Did()[before:]; len(after) != 0 {
		t.Errorf("a pass over a settled organisation did %v", after)
	}

	if writes := r.github.Secret("GLOBEX_DEPLOY_KEY").Writes; writes != 1 {
		t.Errorf("the secret was written %d times, want once", writes)
	}

	r.nowhereButSealed(t)
}

// A secret is written again only when its parameter has a new version.
func TestASecretIsRewrittenWhenItsParameterChanges(t *testing.T) {
	r := newSettingsRig(t, settingsConfig(orgconfig.ModeEnforce), settingsDocument)
	r.pass()
	r.account.Put("/globex/sluis/credentials/deploy-key", "rotated-value")
	r.pass()

	if opened, _ := r.github.OpenSecret("GLOBEX_DEPLOY_KEY"); opened != "rotated-value" {
		t.Errorf("the secret is %q after a rotation", opened)
	}

	if got := r.settings(t).Managed.Secrets["GLOBEX_DEPLOY_KEY"]; got != 2 {
		t.Errorf("the recorded version is %d, want 2", got)
	}

	r.nowhereButSealed(t)
}

// A document that declares a platform-owned name is refused whole: nothing of
// it is applied, and the refusal is reported and audited with a stable code.
func TestAPlatformOwnedNameIsRefused(t *testing.T) {
	r := newSettingsRig(t, settingsConfig(orgconfig.ModeEnforce), `
org:
  variables:
    GLOBEX_REGION: {value: eu-west-1, visibility: all}
    CI_RUNNER_LABEL_DEFAULT: {value: mine, visibility: all}
`)
	r.pass()

	if did := r.settingActions(); len(did) != 0 {
		t.Fatalf("a refused document was applied in part: %v", did)
	}

	got := r.settings(t)
	if got.Document.Outcome != status.DocumentRefused || !strings.Contains(got.Document.Error, "owner-org-platform org.variables[CI_RUNNER_LABEL_DEFAULT]") {
		t.Fatalf("document = %+v", got.Document)
	}

	if records := r.settingRecords(); len(records) != 1 || !strings.HasPrefix(records[0], "roster.github_org_settings.refused") ||
		!strings.Contains(records[0], "failure") {
		t.Errorf("audit = %v, want one refusal", records)
	}

	// The same refusal on the next pass is not recorded again.
	r.pass()

	if records := r.settingRecords(); len(records) != 1 {
		t.Errorf("a standing refusal was recorded again: %v", records)
	}
}

// A value for a name whose value the installation sets is refused too.
func TestAValueForAPlatformValuedNameIsRefused(t *testing.T) {
	r := newSettingsRig(t, settingsConfig(orgconfig.ModeEnforce), `
org:
  variables:
    ACCESS_ROSTER_ISSUER: {value: "https://other.example", visibility: all}
`)
	r.pass()

	if got := r.settings(t); got.Document.Outcome != status.DocumentRefused || len(r.settingActions()) != 0 {
		t.Fatalf("settings = %+v, actions = %v", got, r.settingActions())
	}
}

// A document that stops holding to its checks, or cannot be read, leaves what
// the last good one made, and the controller goes on from there when it is
// good again.
func TestAnInvalidDocumentKeepsTheLastGoodOne(t *testing.T) {
	r := newSettingsRig(t, settingsConfig(orgconfig.ModeEnforce), settingsDocument)
	r.pass()

	for name, bad := range map[string]func(){
		"not YAML": func() { r.account.SetDocument("org: [") },
		"a platform name": func() {
			r.account.SetDocument("org:\n  variables:\n    CI_RUNNER_LABEL_X: {value: x, visibility: all}\n")
		},
		"an unknown key":    func() { r.account.SetDocument("org: {variables: {}}\nrungs: {}\nmystery: 1\n") },
		"another section":   func() { r.account.SetDocument("rungs:\n  dev: {groups: [t@globex.example]}\n") },
		"an empty document": func() { r.account.SetDocument("") },
		"the read failing": func() {
			r.account.SetDocument(settingsDocument)
			r.account.DocErr = errors.New("AccessDenied: not authorized to assume the role")
		},
		"the secret missing": func() {
			r.account.DocErr = nil
			r.account.SetDocument(strings.ReplaceAll(settingsDocument, "deploy-key", "gone"))
		},
	} {
		applied := len(r.github.Did())
		managed := r.settings(t).Managed

		bad()
		r.pass()

		// The empty document is a valid empty section: it removes what the controller
		// wrote, which is its own case below.
		if name == "an empty document" {
			r.account.SetDocument(settingsDocument)
			r.pass()

			continue
		}

		got := r.settings(t)

		switch name {
		case "the secret missing":
			// A secret whose parameter cannot be read is held; the rest of the
			// document is still the document.
			if !slices.ContainsFunc(got.Changes, func(c status.SettingChange) bool {
				return c.Kind == status.KindSecret && c.State == status.SettingHeld
			}) {
				t.Errorf("%s: changes = %+v, want the secret held", name, got.Changes)
			}
		case "the read failing":
			if got.Document.Outcome != status.DocumentUnreadable {
				t.Errorf("%s: document = %+v", name, got.Document)
			}
		default:
			if got.Document.Outcome != status.DocumentRefused {
				t.Errorf("%s: document = %+v, want refused", name, got.Document)
			}
		}

		if name != "the secret missing" {
			if after := r.github.Did()[applied:]; len(after) != 0 {
				t.Errorf("%s: GitHub was changed: %v", name, after)
			}

			if !slices.Equal(got.Managed.Variables, managed.Variables) {
				t.Errorf("%s: the managed record changed to %+v", name, got.Managed)
			}
		}

		r.account.DocErr = nil
		r.account.SetDocument(settingsDocument)
		r.pass()
	}

	if v := r.github.Variable("GLOBEX_REGION"); v == nil {
		t.Error("the last good document's variable is gone")
	}
}

// Removing a name from the document removes it from GitHub if the controller
// wrote it, and touches nothing the controller did not write.
func TestRemovalTouchesOnlyManagedNames(t *testing.T) {
	r := newSettingsRig(t, settingsConfig(orgconfig.ModeEnforce), settingsDocument)

	// Somebody else's settings, one of them with a name the document will
	// name later, and one GitHub provides.
	r.github.SetVariable("FOREIGN_VARIABLE", githubfake.Variable{Value: "theirs", Visibility: "all"})
	r.github.SetSecret("FOREIGN_SECRET", "all")
	r.github.AddConfiguration("foreign-config", map[string]string{"secret_scanning": "disabled"})
	r.github.AddGlobalConfiguration("GitHub recommended")

	r.pass()

	// Now the document drops everything it declared.
	r.account.SetDocument("org: {}\n")
	r.pass()

	for _, gone := range []string{"GLOBEX_REGION", "GLOBEX_HOSTS"} {
		if r.github.Variable(gone) != nil {
			t.Errorf("%s was written by the controller and the document dropped it, but it is still there", gone)
		}
	}

	// A platform variable is the installation's and stays, with the
	// installation's visibility once the document no longer chooses.
	if r.github.Variable("CI_RUNNER_LABEL_DEFAULT") == nil {
		t.Error("a platform variable was removed with the document's")
	}

	if v := r.github.Variable("ACCESS_ROSTER_ISSUER"); v == nil || v.Value != "https://sluis.example.com" || v.Visibility != "all" {
		t.Errorf("ACCESS_ROSTER_ISSUER = %+v, want the installation's value and visibility", v)
	}

	if r.github.Secret("GLOBEX_DEPLOY_KEY") != nil || r.github.Configuration("baseline") != nil {
		t.Error("the secret or the configuration the controller wrote is still there")
	}

	for _, kept := range []string{"FOREIGN_VARIABLE"} {
		if r.github.Variable(kept) == nil {
			t.Errorf("%s was not the controller's and was removed", kept)
		}
	}

	if r.github.Secret("FOREIGN_SECRET") == nil || r.github.Configuration("foreign-config") == nil {
		t.Error("a secret or a configuration the controller did not write was removed")
	}

	for _, did := range r.github.Did() {
		if strings.Contains(did, "FOREIGN") || strings.Contains(did, "foreign") || strings.Contains(did, "recommended") {
			t.Errorf("%q touched what the controller does not manage", did)
		}
	}

	got := r.settings(t)
	if len(got.Managed.SecurityConfigurations) != 0 || len(got.Managed.Secrets) != 0 || slices.Contains(got.Managed.Variables, "GLOBEX_REGION") {
		t.Errorf("managed = %+v, want only the platform variables left", got.Managed)
	}
}

// The record of what the controller wrote survives a restart: it is in the
// report store, and a new process removes what the old one wrote and nothing
// else.
func TestTheManagedRecordSurvivesARestart(t *testing.T) {
	r := newSettingsRig(t, settingsConfig(orgconfig.ModeEnforce), settingsDocument)
	r.github.SetVariable("FOREIGN_VARIABLE", githubfake.Variable{Value: "theirs", Visibility: "all"})
	r.pass()
	r.restart()
	r.account.SetDocument("org: {}\n")
	r.pass()

	if r.github.Variable("GLOBEX_REGION") != nil {
		t.Error("a new process did not remove what the old one wrote")
	}

	if r.github.Variable("FOREIGN_VARIABLE") == nil {
		t.Error("a new process removed what nobody wrote")
	}
}

// A setting the document names and GitHub already holds the same way is not
// written, and so is not the controller's to remove.
func TestAnIdenticalExistingSettingIsNotAdopted(t *testing.T) {
	r := newSettingsRig(t, settingsConfig(orgconfig.ModeEnforce), "org:\n  variables:\n    GLOBEX_REGION: {value: eu-west-1, visibility: all}\n")
	r.github.SetVariable("GLOBEX_REGION", githubfake.Variable{Value: "eu-west-1", Visibility: "all"})
	r.pass()

	for _, did := range r.settingActions() {
		if strings.Contains(did, "GLOBEX_REGION") {
			t.Errorf("an identical setting was written: %q", did)
		}
	}

	r.account.SetDocument("org: {}\n")
	r.pass()

	if r.github.Variable("GLOBEX_REGION") == nil {
		t.Error("a setting the controller never wrote was removed")
	}
}

// One repository that does not exist holds one setting and no other.
func TestAMissingRepositoryHoldsOnlyItsSetting(t *testing.T) {
	r := newSettingsRig(t, settingsConfig(orgconfig.ModeEnforce), `
org:
  variables:
    GLOBEX_A: {value: a, visibility: selected, repositories: [nowhere]}
    GLOBEX_B: {value: b, visibility: all}
`)
	r.pass()

	if r.github.Variable("GLOBEX_A") != nil || r.github.Variable("GLOBEX_B") == nil {
		t.Errorf("A = %+v, B = %+v", r.github.Variable("GLOBEX_A"), r.github.Variable("GLOBEX_B"))
	}

	held := slices.IndexFunc(r.settings(t).Changes, func(c status.SettingChange) bool {
		return c.Name == "GLOBEX_A" && c.State == status.SettingHeld && strings.Contains(c.Reason, "nowhere")
	})
	if held < 0 {
		t.Errorf("changes = %+v, want GLOBEX_A held for the missing repository", r.settings(t).Changes)
	}
}

// GitHub refusing one change is a failed row and an audit record, and the
// pass goes on.
func TestARefusedChangeIsAFailedRow(t *testing.T) {
	r := newSettingsRig(t, settingsConfig(orgconfig.ModeEnforce), settingsDocument)
	r.github.Refuse["create-variable GLOBEX_REGION"] = "Validation Failed"
	r.pass()

	failed := slices.IndexFunc(r.settings(t).Changes, func(c status.SettingChange) bool {
		return c.Name == "GLOBEX_REGION" && c.State == status.SettingFailed && strings.Contains(c.Reason, "Validation Failed")
	})
	if failed < 0 {
		t.Errorf("changes = %+v", r.settings(t).Changes)
	}

	if r.github.Variable("GLOBEX_HOSTS") == nil {
		t.Error("one refused change stopped the others")
	}

	if slices.Contains(r.settings(t).Managed.Variables, "GLOBEX_REGION") {
		t.Error("a change that failed was recorded as written")
	}

	if !slices.Contains(r.settingRecords(), "roster.github_org_setting.changed variable/GLOBEX_REGION create failure enforce") {
		t.Errorf("audit = %v", r.settingRecords())
	}
}

// An organisation without an entry has no settings, and one with an entry and
// no way to read its account reports the document unreadable.
func TestSettingsAreOffWithoutAnEntryAndUnreadableWithoutASource(t *testing.T) {
	r := newSettingsRig(t, settingsConfig(orgconfig.ModeEnforce), settingsDocument)
	r.noEntry = true
	r.restart()
	r.pass()

	if got := r.report.org(t, "globex").Settings; got != nil {
		t.Errorf("an organisation with no entry has settings: %+v", got)
	}

	r.noEntry, r.noSource = false, true
	r.restart()
	r.pass()

	if got := r.settings(t); got.Document.Outcome != status.DocumentUnreadable || len(r.settingActions()) != 0 {
		t.Errorf("settings = %+v, actions = %v", got, r.settingActions())
	}
}

// The mode is the installation's: switching it to report stops the writes
// whatever the document says.
func TestTheInstallationCanForceReport(t *testing.T) {
	r := newSettingsRig(t, settingsConfig(orgconfig.ModeEnforce), settingsDocument)
	r.pass()

	r.cfg.Mode = orgconfig.ModeReport
	r.restart()
	r.account.SetDocument(strings.ReplaceAll(settingsDocument, "eu-west-1", "us-east-1"))

	before := len(r.github.Did())
	r.pass()

	if after := r.github.Did()[before:]; len(after) != 0 {
		t.Fatalf("report mode wrote: %v", after)
	}

	if got := r.settings(t); got.Mode != "report" || !slices.ContainsFunc(got.Changes, func(c status.SettingChange) bool {
		return c.Name == "GLOBEX_REGION" && c.Action == status.SettingUpdate && c.State == status.SettingDrift
	}) {
		t.Errorf("settings = %+v, want the update reported as drift", got)
	}
}

// A configuration attached to every repository leaves an archived one alone:
// GitHub takes none, and the controller must not report it missing for ever.
func TestAnArchivedRepositoryIsNotMissingForEver(t *testing.T) {
	r := newSettingsRig(t, settingsConfig(orgconfig.ModeEnforce), `
org:
  security_configurations:
    baseline:
      description: Scanning on everywhere.
      settings: {secret_scanning: enabled}
      attach: {scope: all}
`)
	r.github.Archive("docs")
	r.pass()

	cfg := r.github.Configuration("baseline")
	if cfg == nil || len(cfg.Attached) != 2 {
		t.Fatalf("baseline = %+v, want the two live repositories", cfg)
	}

	before := len(r.github.Did())
	r.pass()

	if after := r.github.Did()[before:]; len(after) != 0 {
		t.Errorf("a settled organisation was written to again: %v", after)
	}
}
