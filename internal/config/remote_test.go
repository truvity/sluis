package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/config/schema"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/modcall/dial"
)

func TestModuleRemoteTable(t *testing.T) {
	remote := func(r config.ModuleRemote) *config.ModuleRoute { return &config.ModuleRoute{Remote: &r} }
	for name, tc := range map[string]struct {
		doc  config.Sluis
		want map[string]modcall.Target
		err  string
	}{
		"unset": {},
		"function": {
			doc:  config.Sluis{Google: remote(config.ModuleRemote{Function: "sluis-acme-google"})},
			want: map[string]modcall.Target{"google": {Function: "sluis-acme-google"}},
		},
		"url": {
			doc:  config.Sluis{GitHub: remote(config.ModuleRemote{URL: "https://github.sluis.svc", Audience: "gh", TokenFile: "/t"})},
			want: map[string]modcall.Target{"github": {URL: "https://github.sluis.svc", Audience: "gh", TokenFile: "/t"}},
		},
		"no remote stays local": {doc: config.Sluis{Slack: &config.ModuleRoute{}}},
		"both":                  {doc: config.Sluis{Slack: remote(config.ModuleRemote{Function: "f", URL: "http://s"})}, err: "slack.remote: set exactly one of function and url"},
		"neither":               {doc: config.Sluis{Slack: remote(config.ModuleRemote{Audience: "a"})}, err: "slack.remote: set exactly one of function and url"},
		"not a URL":             {doc: config.Sluis{Google: remote(config.ModuleRemote{URL: "ftp://s"})}, err: "google.remote.url"},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.doc.ValidateRoutes()
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := dial.FromTargets(tc.doc.Targets()); !reflect.DeepEqual(got.Modules, tc.want) {
				t.Errorf("got %+v, want %+v", got.Modules, tc.want)
			}
		})
	}
}

// cloudflare.remote is the same routing, not a deprecated one: it is read with
// the per-module sections, and a v2 serve document reads it alone.
func TestCloudflareRemoteIsRoutedBesideTheModuleSections(t *testing.T) {
	cf := &config.Cloudflare{Remote: &config.CloudflareRemote{URL: "http://cloudflare.sluis.svc:8080", Audience: "cloudflare", TokenFile: "/c"}}
	s := &config.Sluis{
		Serve:  config.Serve{Cloudflare: cf},
		GitHub: &config.ModuleRoute{Remote: &config.ModuleRemote{Function: "g"}},
	}
	want := map[string]modcall.Target{
		"cloudflare": {URL: "http://cloudflare.sluis.svc:8080", Audience: "cloudflare", TokenFile: "/c"},
		"github":     {Function: "g"},
	}
	if got := dial.FromTargets(s.Targets()); !reflect.DeepEqual(got.Modules, want) {
		t.Errorf("sluis routes %+v, want %+v", got.Modules, want)
	}
	if got := dial.FromTargets((&config.Serve{Cloudflare: cf}).Targets()); !reflect.DeepEqual(got.Modules, map[string]modcall.Target{"cloudflare": want["cloudflare"]}) {
		t.Errorf("serve routes %+v", got.Modules)
	}
}

// The three documents refuse unknown keys under a module section and read the
// example; the Go validation holds what the schema cannot say.
func TestTheSchemasHoldTheModuleSections(t *testing.T) {
	for _, name := range []string{"sluis", "controller-github", "controller-slack"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", name+".full.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			if err := config.Validate(name, doc); err != nil {
				t.Fatalf("the example: %v", err)
			}
			doc["github"] = map[string]any{"remote": map[string]any{"surprise": true}}
			if err := config.Validate(name, doc); err == nil {
				t.Error("an unknown key validates")
			}
			doc["github"] = map[string]any{"surprise": true}
			if err := config.Validate(name, doc); err == nil {
				t.Error("an unknown module key validates")
			}
			doc["github"] = map[string]any{"remote": map[string]any{"url": "ftp://x"}}
			if err := config.Validate(name, doc); err == nil {
				t.Error("a url that is not http(s) validates")
			}
		})
	}
}

// A document with both a function and a url on one module is refused when
// loaded, for each of the three documents.
func TestLoadRefusesAModuleWithBothAFunctionAndAURL(t *testing.T) {
	for _, name := range []string{"sluis", "controller-github", "controller-slack"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", name+".full.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			doc["google"] = map[string]any{"remote": map[string]any{"function": "f", "url": "http://x"}}
			if raw, err = yaml.Marshal(doc); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), name+".yaml")
			if err := os.WriteFile(file, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			var lerr error
			switch name {
			case "sluis":
				_, lerr = config.Load[config.Sluis](file)
			case "controller-github":
				_, lerr = config.Load[config.ControllerGitHub](file)
			default:
				_, lerr = config.Load[config.ControllerSlack](file)
			}
			if lerr == nil || !strings.Contains(lerr.Error(), "set exactly one of function and url") {
				t.Fatalf("got %v", lerr)
			}
		})
	}
}

// A v2 serve document has no module sections: they are the v3 document's.
func TestServeSchemaHasNoModuleSections(t *testing.T) {
	body, _ := schema.Schema("serve")
	var doc struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"github", "slack", "google", "calls"} {
		if _, ok := doc.Properties[key]; ok {
			t.Errorf("serve (v2) gained %s", key)
		}
	}
}
