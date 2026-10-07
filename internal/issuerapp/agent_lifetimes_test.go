package issuerapp

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// Unset, lifetimes.agent is 14 days idle, 30 days absolute and a 30-minute
// access-token cap, wired into the issuer's config.
func TestAgentLifetimesDefault(t *testing.T) {
	cfg, err := FromConfig(withPolicy(t, &config.Serve{IssuerURL: "https://issuer.example"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	want := issuer.AgentLifetimes{Refresh: 336 * time.Hour, Absolute: 720 * time.Hour, Access: 30 * time.Minute}
	if cfg.agentLifetimes != want {
		t.Errorf("agentLifetimes = %+v, want %+v", cfg.agentLifetimes, want)
	}
}

// What is refused at load, in the style of a resource's absolute_cap: an
// absolute limit past 90 days or not positive, an idle limit past the
// absolute one (or not positive), and an access cap past one hour or not
// positive. A zero the file states is refused, never defaulted. Shortening
// is always allowed.
func TestAgentLifetimesRefusals(t *testing.T) {
	d := func(v time.Duration) *config.Duration { c := config.Duration(v); return &c }
	for _, tc := range []struct {
		name  string
		agent config.AgentLifetimes
		want  string // empty: accepted
	}{
		{"an empty block is the defaults", config.AgentLifetimes{}, ""},
		{"shorter everywhere", config.AgentLifetimes{Refresh: d(24 * time.Hour), Absolute: d(48 * time.Hour), Access: d(5 * time.Minute)}, ""},
		{"exactly 90 days and 1h", config.AgentLifetimes{Absolute: d(2160 * time.Hour), Access: d(time.Hour)}, ""},
		{"absolute past 90 days", config.AgentLifetimes{Absolute: d(2161 * time.Hour)}, "lifetimes.agent.absolute"},
		{"absolute zero", config.AgentLifetimes{Absolute: d(0)}, "lifetimes.agent.absolute"},
		{"absolute negative", config.AgentLifetimes{Absolute: d(-time.Hour)}, "lifetimes.agent.absolute"},
		{"refresh past absolute", config.AgentLifetimes{Refresh: d(721 * time.Hour)}, "lifetimes.agent.refresh"},
		{"refresh past a shortened absolute", config.AgentLifetimes{Absolute: d(48 * time.Hour)}, "lifetimes.agent.refresh"},
		{"refresh zero", config.AgentLifetimes{Refresh: d(0)}, "lifetimes.agent.refresh"},
		{"access past 1h", config.AgentLifetimes{Access: d(61 * time.Minute)}, "lifetimes.agent.access"},
		{"access zero", config.AgentLifetimes{Access: d(0)}, "lifetimes.agent.access"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := tc.agent
			_, err := FromConfig(withPolicy(t, &config.Serve{
				IssuerURL: "https://issuer.example",
				Lifetimes: &config.Lifetimes{Agent: &agent},
			}))
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("FromConfig(): %v, want it accepted", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("FromConfig() = %v, want a refusal naming %s", err, tc.want)
			}
		})
	}
}

// Warned at start, not refused: `session: agent` on a client with
// signed_out or backchannel_logout_uri, which describe a browser-facing
// application, and a read-only resource whose absolute_cap still lengthens
// lifetimes.absolute -- deprecated, still honoured, the warning naming
// `session: agent` as the way forward.
func TestAgentClassWarningsAtStart(t *testing.T) {
	cfg, err := FromConfig(withPolicy(t, &config.Serve{IssuerURL: "https://issuer.example"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	declared, err := policy.Parse([]byte("version: 1\ngroups: { a: { members: [g@h.example] } }\n" +
		"clients:\n" +
		"  browser-agent: { kind: public, redirects: ['http://127.0.0.1/cb'], signed_out: ['http://127.0.0.1/'], requires: [a], session: agent }\n" +
		"  mcp-host: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a], session: agent }\n" +
		"resources:\n" +
		"  'https://week.example/': { requires: [a], read_only: true, absolute_cap: 168h }\n" +
		"  'https://short.example/': { requires: [a], read_only: true, absolute_cap: 2h }\n"))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, nil))

	// No directory is supplied, so New stops right after the checks that
	// warn, which is all this test needs of it.
	if _, err = New(t.Context(), cfg, Deps{Policy: set}, log); err == nil || strings.Contains(err.Error(), "read_only") {
		t.Fatalf("New: %v, want it to stop for the missing directory and not refuse the rows", err)
	}

	logged := out.String()
	for _, want := range []string{
		"session: agent and also signed_out or backchannel_logout_uri",
		"clients=[browser-agent]",
		"that lengthening is deprecated",
		"session: agent",
		"resources=[https://week.example/]",
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("the start log does not say %q:\n%s", want, logged)
		}
	}
	if strings.Contains(logged, "mcp-host") || strings.Contains(logged, "short.example") {
		t.Errorf("the start log names a row it has no reason to warn about:\n%s", logged)
	}
}
