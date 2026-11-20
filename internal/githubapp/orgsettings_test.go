package githubapp_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubapp/githubfake"
)

// A secret is sealed with the organisation's own key, as a libsodium sealed
// box, and only that key opens it. The same value sealed twice differs, as a
// sealed box does.
func TestASecretIsSealedForTheOrganisationsKey(t *testing.T) {
	fake := githubfake.Start(t, "globex")
	org := githubapp.Org{HTTP: fake.Client(), Login: "globex"}
	ctx := context.Background()

	key, err := org.SecretsPublicKey(ctx, fake.Token)
	if err != nil || key.ID == "" {
		t.Fatalf("key = %+v, %v", key, err)
	}

	first, err := key.Seal([]byte("the-value"))
	if err != nil {
		t.Fatal(err)
	}

	second, _ := key.Seal([]byte("the-value"))
	if first == second || strings.Contains(first, "the-value") {
		t.Errorf("a sealed value must differ every time and hold no plain text: %q %q", first, second)
	}

	if err = org.PutSecret(ctx, fake.Token, "EXAMPLE", first, key, "all", nil); err != nil {
		t.Fatal(err)
	}

	if opened, err := fake.OpenSecret("EXAMPLE"); err != nil || opened != "the-value" {
		t.Errorf("opened %q, %v", opened, err)
	}

	// A write that names another key is refused by GitHub, and the error
	// carries GitHub's words, not the value.
	other := key
	other.ID = "1"

	err = org.PutSecret(ctx, fake.Token, "EXAMPLE", first, other, "all", nil)
	if err == nil || strings.Contains(err.Error(), "the-value") {
		t.Errorf("a write under the wrong key: %v", err)
	}
}

func TestVariablesAndConfigurationsRoundTrip(t *testing.T) {
	fake := githubfake.Start(t, "globex")
	shop := fake.AddRepository("shop")
	fake.AddGlobalConfiguration("GitHub recommended")

	org := githubapp.Org{HTTP: fake.Client(), Login: "globex"}
	ctx := context.Background()

	if err := org.CreateVariable(ctx, fake.Token, githubapp.Variable{Name: "EXAMPLE", Value: "1", Visibility: "selected"}, []int64{shop}); err != nil {
		t.Fatal(err)
	}

	if err := org.UpdateVariable(ctx, fake.Token, githubapp.Variable{Name: "EXAMPLE", Value: "2", Visibility: "all"}, nil); err != nil {
		t.Fatal(err)
	}

	variables, err := org.Variables(ctx, fake.Token)
	if err != nil || len(variables) != 1 || variables[0].Value != "2" || variables[0].Visibility != "all" {
		t.Fatalf("variables = %+v, %v", variables, err)
	}

	id, err := org.CreateSecurityConfiguration(ctx, fake.Token, "baseline", "Scanning on.", map[string]string{"secret_scanning": "enabled"})
	if err != nil {
		t.Fatal(err)
	}

	if err = org.AttachSecurityConfiguration(ctx, fake.Token, id, "selected", []int64{shop}); err != nil {
		t.Fatal(err)
	}

	configs, err := org.SecurityConfigurations(ctx, fake.Token)
	if err != nil || len(configs) != 1 || configs[0].Name != "baseline" || configs[0].Settings["secret_scanning"] != "enabled" {
		t.Fatalf("configurations = %+v, %v; want the organisation's own and not GitHub's", configs, err)
	}

	// The default for new repositories is read back as the API words it, and
	// setting it takes the kinds from whichever configuration held them.
	if defaults, err := org.ConfigurationDefaults(ctx, fake.Token); err != nil || len(defaults) != 0 {
		t.Fatalf("defaults = %v, %v, want none", defaults, err)
	}

	if err = org.SetConfigurationDefault(ctx, fake.Token, id, "all"); err != nil {
		t.Fatal(err)
	}

	if defaults, err := org.ConfigurationDefaults(ctx, fake.Token); err != nil || defaults[id] != "all" || len(defaults) != 1 {
		t.Fatalf("defaults = %v, %v, want the configuration for all", defaults, err)
	}

	if err = org.SetConfigurationDefault(ctx, fake.Token, id, "none"); err != nil {
		t.Fatal(err)
	}

	if defaults, err := org.ConfigurationDefaults(ctx, fake.Token); err != nil || len(defaults) != 0 {
		t.Fatalf("defaults = %v, %v, want none after none", defaults, err)
	}

	attached, err := org.ConfigurationRepositories(ctx, fake.Token, id)
	if err != nil || len(attached) != 1 || attached[0].Name != "shop" {
		t.Fatalf("attached = %+v, %v", attached, err)
	}

	if err = org.DetachSecurityConfigurations(ctx, fake.Token, []int64{shop}); err != nil {
		t.Fatal(err)
	}

	if err = org.DeleteSecurityConfiguration(ctx, fake.Token, id); err != nil {
		t.Fatal(err)
	}

	if err = org.DeleteVariable(ctx, fake.Token, "EXAMPLE"); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"create-variable EXAMPLE", "update-variable EXAMPLE", "create-config baseline", "attach-config baseline selected",
		"set-default baseline all", "set-default baseline none",
		"detach-config " + itoa(shop), "delete-config baseline", "delete-variable EXAMPLE",
	}
	if did := fake.Did(); !slices.Equal(did, want) {
		t.Errorf("did %v, want %v", did, want)
	}
}

func itoa(n int64) string {
	out := ""
	for ; n > 0; n /= 10 {
		out = string(rune('0'+n%10)) + out
	}

	return out
}
