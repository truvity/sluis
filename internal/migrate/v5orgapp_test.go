package migrate_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/githubroster/appid"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	ghconn "github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/migrate"
	"github.com/truvity/sluis/internal/store"
)

// putOrg connects an organisation with an App of its own, as layout v4 did: the
// key is the organisation's credential.
func putOrg(t *testing.T, st *store.Stores, org string, appID int64, slug, key string) {
	t.Helper()
	d, err := migrate.OpenDomains(ctx, st, true)
	if err != nil {
		t.Fatal(err)
	}
	err = d.Orgs.Put(ctx,
		ghconn.Record{Org: org, AppID: appID, AppSlug: slug, InstallationID: appID + 1000, ConnectedAt: now, ConnectedBy: "ada@acme.example"},
		ghconn.Credential{Org: org, AppID: appID, InstallationID: appID + 1000, PrivateKey: key})
	if err != nil {
		t.Fatal(err)
	}
}

func putCatalogueApp(t *testing.T, st *store.Stores, id string, appID int64, key string) {
	t.Helper()
	d, err := migrate.OpenDomains(ctx, st, true)
	if err != nil {
		t.Fatal(err)
	}
	err = d.Catalogue.Put(ctx, catalogueapp.Record{
		ID: id, Org: "example", AppID: appID, AppSlug: "example-" + id, InstallationID: appID + 1000, ConnectedAt: now, ConnectedBy: "ada@acme.example",
	}, key)
	if err != nil {
		t.Fatal(err)
	}
}

// ownAppOptions are the options of a configuration that says nothing of the
// Apps: no appRefs entry.
func ownAppOptions() migrate.V5Options {
	return migrate.V5Options{WritersStopped: true}
}

// orgApps reads the destination's organisations and catalogue Apps.
func orgApps(t *testing.T, dst *v5Installation) (map[string]string, map[string]string) {
	t.Helper()
	d, err := migrate.OpenDomains(ctx, dst.st, false)
	if err != nil {
		t.Fatal(err)
	}
	orgs := map[string]string{}
	list, err := d.Orgs.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range list {
		orgs[o.Org] = o.AppRef
	}
	keys := map[string]string{}
	apps, err := d.Catalogue.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range apps {
		_, key, _, err := d.Catalogue.Get(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		keys[a.ID] = key
	}
	return orgs, keys
}

// catalogueItems are the plan's catalogue-App rows, by id and status.
func catalogueItems(r *migrate.PlanReport) map[string]migrate.PlanStatus {
	out := map[string]migrate.PlanStatus{}
	for _, m := range r.Modules {
		for _, it := range m.Items {
			if it.Concern == "state" && it.Kind == "app" {
				out[it.ID] = it.Status
			}
		}
	}
	return out
}

// An organisation connected with its own App, with no entry in the
// configuration: the App is made from the organisation's credential, the key is
// kept once under it, and the organisation names it.
func TestAnOrganisationOwnAppBecomesACatalogueApp(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	putOrg(t, src.st, "example", 31, "example-access", "EXAMPLE-ORG-KEY")

	plan, err := migrate.Plan(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), ownAppOptions().PlanOptions)
	if err != nil {
		t.Fatalf("Plan = %v\n%s", err, plan.JSON())
	}
	noPlanSecrets(t, plan.JSON())
	if strings.Contains(string(plan.JSON()), "EXAMPLE-ORG-KEY") {
		t.Error("the plan holds the key")
	}
	if got := catalogueItems(plan); len(got) != 1 || got["example-access"] != migrate.PlanNew {
		t.Errorf("the plan's Apps = %v, want the synthesized one as new", got)
	}

	report, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), ownAppOptions())
	if err != nil || !report.OK {
		t.Fatalf("CopyV5 = %v\n%s", err, report.JSON())
	}
	if v := report.Verify; v == nil || !v.OK || v.Missing+v.Different != 0 {
		t.Errorf("verify after the copy = %+v", v)
	}
	orgs, keys := orgApps(t, dst)
	if orgs["example"] != "example-access" {
		t.Errorf("the organisation names the App %q", orgs["example"])
	}
	if keys["example-access"] != "EXAMPLE-ORG-KEY" {
		t.Error("the synthesized App does not hold the organisation's key")
	}
	// The key is kept once, under the App.
	if names := dst.rec.Addresses("put"); !containsPrefix(names, "internal/github/apps/example-access/") {
		t.Errorf("the key was put at %v, want internal/github/apps/example-access/<ref>", names)
	}
	for _, n := range dst.rec.Addresses("put") {
		if strings.HasPrefix(n, "internal/github/orgs") || strings.Contains(n, "github-org") {
			t.Errorf("the organisation's key was kept under %s", n)
		}
	}

	verified, err := migrate.VerifyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), ownAppOptions().PlanOptions)
	if err != nil || !verified.OK {
		t.Fatalf("VerifyV5 = %v\n%s", err, verified.JSON())
	}
	if got := catalogueItems(verified); got["example-access"] != migrate.PlanSame {
		t.Errorf("verify's Apps = %v", got)
	}

	// A key that changed on the source is a difference.
	putOrg(t, src.st, "example", 31, "example-access", "ROTATED-KEY")
	if _, err = migrate.VerifyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), ownAppOptions().PlanOptions); !errors.Is(err, migrate.ErrMismatch) {
		t.Errorf("VerifyV5 after the key changed = %v, want ErrMismatch", err)
	}
}

func containsPrefix(names []string, prefix string) bool {
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

// Organisations installed by one App share the one App made of it.
func TestOrganisationsOfOneAppShareTheMadeApp(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	putOrg(t, src.st, "example", 31, "example-access", "SHARED-KEY")
	putOrg(t, src.st, "acme", 31, "example-access", "SHARED-KEY")

	report, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), ownAppOptions())
	if err != nil || !report.OK {
		t.Fatalf("CopyV5 = %v\n%s", err, report.JSON())
	}
	orgs, keys := orgApps(t, dst)
	if len(keys) != 1 || orgs["example"] != "example-access" || orgs["acme"] != "example-access" {
		t.Errorf("organisations %v, Apps %d", orgs, len(keys))
	}

	// The same App with two keys cannot be one App.
	src2, dst2 := newV4Installation(t), newV5Installation(t)
	putOrg(t, src2.st, "example", 31, "example-access", "KEY-ONE")
	putOrg(t, src2.st, "acme", 31, "example-access", "KEY-TWO")
	_, err = migrate.Plan(ctx, side("v4.yaml", src2.st), side("v5.yaml", dst2.st), ownAppOptions().PlanOptions)
	if !errors.Is(err, migrate.ErrRefused) {
		t.Errorf("Plan with two keys of one App = %v, want ErrRefused", err)
	}
}

// An App the source already holds with the organisation's App id is referenced,
// and nothing is made.
func TestAnOrganisationUsesTheSourceAppThatInstallsIt(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	putCatalogueApp(t, src.st, "roster", 31, "ROSTER-APP-KEY")
	putOrg(t, src.st, "example", 31, "example-access", "ORG-OWN-KEY")

	report, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), ownAppOptions())
	if err != nil || !report.OK {
		t.Fatalf("CopyV5 = %v\n%s", err, report.JSON())
	}
	if got := catalogueItems(report); len(got) != 1 {
		t.Errorf("the Apps = %v, want only the source's", got)
	}
	orgs, keys := orgApps(t, dst)
	if orgs["example"] != "roster" || len(keys) != 1 || keys["roster"] != "ROSTER-APP-KEY" {
		t.Errorf("organisations %v, Apps %d", orgs, len(keys))
	}
}

// An entry in the configuration overrides the match; one that names an App
// other than the installing one is refused, as before.
func TestAnExplicitAppRefOverridesTheMatch(t *testing.T) {
	seed := func() (*v4Installation, *v5Installation) {
		src, dst := newV4Installation(t), newV5Installation(t)
		putCatalogueApp(t, src.st, "alpha", 31, "ALPHA-KEY")
		putCatalogueApp(t, src.st, "bravo", 31, "BRAVO-KEY")
		putCatalogueApp(t, src.st, "charlie", 32, "CHARLIE-KEY")
		putOrg(t, src.st, "example", 31, "example-access", "ORG-OWN-KEY")
		return src, dst
	}

	src, dst := seed()
	report, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), ownAppOptions())
	if err != nil || !report.OK {
		t.Fatalf("CopyV5 = %v\n%s", err, report.JSON())
	}
	if orgs, _ := orgApps(t, dst); orgs["example"] != "alpha" {
		t.Errorf("without an entry the organisation names %q, want the first match alpha", orgs["example"])
	}

	src, dst = seed()
	opt := ownAppOptions()
	opt.AppRef = func(string) string { return "bravo" }
	report, err = migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
	if err != nil || !report.OK {
		t.Fatalf("CopyV5 with an entry = %v\n%s", err, report.JSON())
	}
	if orgs, _ := orgApps(t, dst); orgs["example"] != "bravo" {
		t.Errorf("with an entry the organisation names %q, want bravo", orgs["example"])
	}

	src, dst = seed()
	opt.AppRef = func(string) string { return "charlie" }
	report, err = migrate.Plan(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt.PlanOptions)
	if !errors.Is(err, migrate.ErrRefused) || !strings.Contains(report.Error, "would not sign") {
		t.Errorf("Plan with an entry for another GitHub App = %v", err)
	}
}

// An organisation's App slug that cannot be an App id is refused, naming the
// way out.
func TestAnOrganisationWhoseSlugCannotBeAnIdIsRefused(t *testing.T) {
	for name, slug := range map[string]string{
		"capitals": "Example-Access",
		"too long": strings.Repeat("a", 40),
		"reserved": appid.RunnerPrefix + "x",
	} {
		t.Run(name, func(t *testing.T) {
			src, dst := newV4Installation(t), newV5Installation(t)
			putOrg(t, src.st, "example", 31, slug, "KEY")
			report, err := migrate.Plan(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), ownAppOptions().PlanOptions)
			if !errors.Is(err, migrate.ErrRefused) || !strings.Contains(report.Error, "appRefs") {
				t.Errorf("Plan = %v", err)
			}
		})
	}
}
