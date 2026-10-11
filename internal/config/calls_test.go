package config_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/config/schema"
	"github.com/truvity/sluis/internal/modcall"
)

func TestCallsRouterTable(t *testing.T) {
	for name, tc := range map[string]struct {
		calls *config.Calls
		want  map[string]modcall.Target
		err   string
	}{
		"unset": {calls: nil, want: nil},
		"function": {
			calls: &config.Calls{Modules: map[string]config.CallTarget{"google": {Function: "sluis-acme-google"}}},
			want:  map[string]modcall.Target{"google": {Function: "sluis-acme-google"}},
		},
		"url": {
			calls: &config.Calls{Modules: map[string]config.CallTarget{"github": {URL: "https://github.sluis.svc", Audience: "gh", TokenFile: "/t"}}},
			want:  map[string]modcall.Target{"github": {URL: "https://github.sluis.svc", Audience: "gh", TokenFile: "/t"}},
		},
		"local": {
			calls: &config.Calls{Modules: map[string]config.CallTarget{"slack": {}}},
			want:  map[string]modcall.Target{"slack": {}},
		},
		"both": {
			calls: &config.Calls{Modules: map[string]config.CallTarget{"slack": {Function: "f", URL: "http://s"}}},
			err:   "at most one of function and url",
		},
		"not a URL": {
			calls: &config.Calls{Modules: map[string]config.CallTarget{"slack": {URL: "ftp://s"}}},
			err:   "not an http(s) URL",
		},
		"bad name": {
			calls: &config.Calls{Modules: map[string]config.CallTarget{"Slack_": {}}},
			err:   "not a module name",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.calls.Validate()
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := tc.calls.ModCall(nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Modules, tc.want) {
				t.Errorf("got %+v, want %+v", got.Modules, tc.want)
			}
		})
	}
}

func TestTheDeprecatedCloudflareRemoteRendersTheSameModcallConfig(t *testing.T) {
	for name, remote := range map[string]config.CloudflareRemote{
		"function": {Function: "sluis-acme-cloudflare"},
		"url":      {URL: "http://cloudflare.sluis.svc:8080", Audience: "cloudflare", TokenFile: "/var/run/secrets/sluis/cloudflare/token"},
	} {
		t.Run(name, func(t *testing.T) {
			old, err := (*config.Calls)(nil).ModCall(&config.Cloudflare{Remote: &remote})
			if err != nil {
				t.Fatal(err)
			}
			now, err := (&config.Calls{Modules: map[string]config.CallTarget{
				"cloudflare": {Function: remote.Function, URL: remote.URL, Audience: remote.Audience, TokenFile: remote.TokenFile},
			}}).ModCall(nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(old, now) {
				t.Errorf("cloudflare.remote renders %+v, calls.modules.cloudflare renders %+v", old, now)
			}
			// The v2 serve document reads the deprecated form to the same result.
			if v2, err := (&config.Serve{Cloudflare: &config.Cloudflare{Remote: &remote}}).ModCall(); err != nil || !reflect.DeepEqual(v2, old) {
				t.Errorf("serve renders %+v, %v", v2, err)
			}
		})
	}
}

func TestTheCloudflareModuleNamedTwiceIsRefused(t *testing.T) {
	s := &config.Sluis{
		Calls: &config.Calls{Modules: map[string]config.CallTarget{"cloudflare": {Function: "f"}}},
	}
	s.Cloudflare = &config.Cloudflare{Remote: &config.CloudflareRemote{Function: "g"}}
	err := s.ValidateCalls()
	if err == nil || !strings.Contains(err.Error(), "both name the Cloudflare module") {
		t.Fatalf("%v", err)
	}
	s.Calls = &config.Calls{Modules: map[string]config.CallTarget{"github": {Function: "f"}}}
	if err = s.ValidateCalls(); err != nil {
		t.Fatalf("different modules: %v", err)
	}
}

// The three documents refuse a module that names both a function and a URL at
// the schema, before the type is asked, and read the section when it is valid.
func TestTheSchemasHoldCalls(t *testing.T) {
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
			doc["calls"] = map[string]any{"modules": map[string]any{"github": map[string]any{"function": "f", "url": "http://x"}}}
			if err := config.Validate(name, doc); err == nil {
				t.Error("a module with both a function and a url validates")
			}
			doc["calls"] = map[string]any{"modules": map[string]any{"Bad_Name": map[string]any{}}}
			if err := config.Validate(name, doc); err == nil {
				t.Error("a bad module name validates")
			}
			doc["calls"] = map[string]any{"modules": map[string]any{"github": map[string]any{"surprise": true}}}
			if err := config.Validate(name, doc); err == nil {
				t.Error("an unknown key validates")
			}
		})
	}
}

// A v2 serve document has no `calls`: the section is the v3 document's.
func TestServeSchemaHasNoCalls(t *testing.T) {
	body, _ := schema.Schema("serve")
	if bytes.Contains(body, []byte(`"calls"`)) {
		t.Error("serve (v2) gained a calls section")
	}
}
