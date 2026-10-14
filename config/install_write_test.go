package config_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/config"
)

// roundTripInstallations are the installations the writer is held to: every
// installation fixture, and an installation around each access section the
// A1 goldens hold.
func roundTripInstallations(t *testing.T) map[string]*config.Installation {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "*.installation.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no installation fixtures: %v", err)
	}
	out := map[string]*config.Installation{}
	for _, f := range files {
		in, err := config.LoadInstallation(f)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(f)] = in
	}
	for _, shape := range []string{"kubernetes", "lambda"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "install", shape+".golden.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var access map[string]any
		if err := yaml.Unmarshal(raw, &access); err != nil {
			t.Fatal(err)
		}
		doc := map[string]any{
			"apiVersion": config.InstallationVersion,
			"instance":   "acme",
			"shape":      "lambda",
			"issuer":     map[string]any{"url": "https://id.example.com"},
			"aws":        map[string]any{"account": "111122223333", "region": "eu-west-1"},
			"access":     access,
		}
		raw, err = yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "installation.yaml")
		if err := os.WriteFile(file, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		in, err := config.LoadInstallation(file)
		if err != nil {
			t.Fatalf("%s access section in an installation: %v", shape, err)
		}
		out["access-"+shape] = in
	}
	return out
}

func loadBytes(t *testing.T, raw []byte) *config.Installation {
	t.Helper()
	file := filepath.Join(t.TempDir(), "installation.yaml")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := config.LoadInstallation(file)
	if err != nil {
		t.Fatalf("the writer's document does not load: %v\n%s", err, raw)
	}
	return in
}

// Load(Marshal(x)) is x, and writing it again gives the same bytes; and what
// loads is what Render renders for x.
func TestMarshalInstallationRoundTrips(t *testing.T) {
	for name, in := range roundTripInstallations(t) {
		t.Run(name, func(t *testing.T) {
			first, err := config.MarshalInstallation(in, config.MarshalOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(first, []byte("m0s")) || bytes.Contains(first, []byte("h0m")) {
				t.Errorf("a duration is not in the canonical spelling:\n%s", first)
			}
			back := loadBytes(t, first)
			if !reflect.DeepEqual(in, back) {
				t.Errorf("Load(Marshal(x)) is not x:\n%s", first)
			}
			second, err := config.MarshalInstallation(back, config.MarshalOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first, second) {
				t.Errorf("a second write differs:\n%s", lineDiffForTest(first, second))
			}
			again, err := config.MarshalInstallation(in, config.MarshalOptions{})
			if err != nil || !bytes.Equal(first, again) {
				t.Errorf("the writer is not deterministic: %v", err)
			}
			if s1, p1, err := config.Render(in); err != nil {
				t.Skipf("fixture does not render: %v", err)
			} else if s2, p2, err := config.Render(back); err != nil || !bytes.Equal(s1, s2) || !bytes.Equal(p1, p2) {
				t.Errorf("the reloaded installation renders differently: %v", err)
			}
		})
	}
}

func lineDiffForTest(a, b []byte) string {
	return "first:\n" + string(a) + "\nsecond:\n" + string(b)
}

// The key order is fixed: apiVersion first, every other key sorted, at every
// level.
func TestMarshalInstallationKeyOrderAndDurations(t *testing.T) {
	in := loadBytes(t, []byte(`apiVersion: sluis.truvity.github.io/installation/v1
shape: lambda
lifetimes: {token: 1h0m0s, absolute: 24h30m0s}
instance: acme
issuer: {url: "https://id.example.com", groupsScoping: enforce}
aws: {region: eu-west-1, account: "111122223333"}
`))
	got, err := config.MarshalInstallation(in, config.MarshalOptions{Header: "Generated.\n\nDo not edit."})
	if err != nil {
		t.Fatal(err)
	}
	want := `# Generated.
#
# Do not edit.
apiVersion: sluis.truvity.github.io/installation/v1
aws:
  account: "111122223333"
  region: eu-west-1
instance: acme
issuer:
  groupsScoping: enforce
  url: https://id.example.com
lifetimes:
  absolute: 24h30m
  token: 1h
shape: lambda
`
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	loadBytes(t, got)
}

// A writer cannot produce a document the loader refuses.
func TestMarshalInstallationRefusesWhatTheLoaderRefuses(t *testing.T) {
	valid := func() *config.Installation {
		return &config.Installation{
			Instance: "acme", Shape: config.ShapeLambda,
			Issuer: config.Issuer{URL: "https://id.example.com"},
			AWS:    &config.AWS{Region: "eu-west-1"},
		}
	}
	if _, err := config.MarshalInstallation(valid(), config.MarshalOptions{}); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*config.Installation){
		"no instance":           func(i *config.Installation) { i.Instance = "" },
		"reserved instance":     func(i *config.Installation) { i.Instance = "private" },
		"unknown shape":         func(i *config.Installation) { i.Shape = "mainframe" },
		"no issuer":             func(i *config.Installation) { i.Issuer.URL = "" },
		"lambda without region": func(i *config.Installation) { i.AWS = nil },
		"other apiVersion":      func(i *config.Installation) { i.APIVersion = "sluis.truvity.github.io/installation/v2" },
		"bad groups scoping":    func(i *config.Installation) { i.Issuer.GroupsScoping = "sometimes" },
	} {
		t.Run(name, func(t *testing.T) {
			in := valid()
			mod(in)
			if out, err := config.MarshalInstallation(in, config.MarshalOptions{}); err == nil {
				t.Fatalf("written:\n%s", out)
			}
		})
	}
	if _, err := config.MarshalInstallation(nil, config.MarshalOptions{}); err == nil {
		t.Fatal("nil installation written")
	}
}

func TestCheckInstallation(t *testing.T) {
	in := roundTripInstallations(t)["example.installation.yaml"]
	opts := config.MarshalOptions{Header: "Generated."}
	want, err := config.MarshalInstallation(in, opts)
	if err != nil {
		t.Fatal(err)
	}
	if eq, diff, err := config.CheckInstallation(want, in, opts); err != nil || !eq || diff != "" {
		t.Fatalf("equal = %v, diff %q, err %v", eq, diff, err)
	}
	stale := bytes.Replace(want, []byte("instance: "), []byte("instance: x"), 1)
	eq, diff, err := config.CheckInstallation(stale, in, opts)
	if err != nil || eq || !strings.Contains(diff, "-instance: x") || !strings.Contains(diff, "+instance: ") {
		t.Fatalf("equal = %v, diff %q, err %v", eq, diff, err)
	}
	if eq, _, _ := config.CheckInstallation(want, in, config.MarshalOptions{Header: "Other."}); eq {
		t.Fatal("a changed header is equal")
	}
	bad := *in
	bad.Instance = ""
	if _, _, err := config.CheckInstallation(want, &bad, opts); err == nil {
		t.Fatal("an installation the loader refuses was compared")
	}
}
