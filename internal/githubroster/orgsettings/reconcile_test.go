package orgsettings_test

import (
	"context"
	"slices"
	"testing"

	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubapp/githubfake"
	"github.com/truvity/sluis/internal/githubroster/orgsettings"
	"github.com/truvity/sluis/internal/githubroster/orgsettings/settingstest"
	"github.com/truvity/sluis/internal/githubroster/status"
	"github.com/truvity/sluis/policy/tenant"
)

// A record that names foo and FOO is one name to GitHub: one removal, not two.
func TestAFoldedDuplicateInTheRecordIsOneRemoval(t *testing.T) {
	fake := githubfake.Start(t, "globex")
	fake.SetVariable("FOO", githubfake.Variable{Value: "x", Visibility: "all"})

	result, err := orgsettings.Reconcile(context.Background(), orgsettings.Input{
		Mode:   orgsettings.ModeEnforce,
		Target: orgsettings.Target{Org: githubapp.Org{HTTP: fake.Client(), Login: "globex"}, Token: fake.Token},
		Desired: orgsettings.Desired{Variables: map[string]tenant.OrgVariable{
			"KEPT": {Value: "k", Visibility: "all"},
		}},
		Previous: status.Managed{Variables: []string{"foo", "FOO"}},
		Secrets:  settingstest.New(""),
	})
	if err != nil {
		t.Fatal(err)
	}

	deletes := 0

	for _, c := range result.Changes {
		if c.Action == status.SettingDelete {
			deletes++
		}
	}

	if deletes != 1 || fake.Variable("FOO") != nil {
		t.Errorf("deletes = %d, changes = %+v, did = %v", deletes, result.Changes, fake.Did())
	}

	if slices.Contains(result.Managed.Variables, "foo") || slices.Contains(result.Managed.Variables, "FOO") {
		t.Errorf("managed = %+v", result.Managed)
	}
}
