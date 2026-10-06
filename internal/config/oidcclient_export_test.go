package config_test

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/exports"
)

const oidcBase = "apiVersion: sluis.truvity.github.io/policy/v2\n" +
	"groups:\n  all:access-roster:operator: {}\n" +
	"clients:\n" +
	"  grafana: {kind: confidential, secret: {generate: true}, requires: [all:access-roster:operator]}\n" +
	"  argocd:  {kind: confidential, secret: argocd-oidc, requires: [all:access-roster:operator]}\n" +
	"  console: {kind: public, requires: [all:access-roster:operator], redirects: ['https://c.example/cb']}\n"

func TestAnOIDCClientExportOfAGeneratedClientLoads(t *testing.T) {
	t.Parallel()
	d, err := config.Load[config.PolicyDocument](write(t, oidcBase+
		"exports: [{source: oidc-client, client: grafana, namespace: example, path: oidc/grafana, properties: {client-secret: GF_SECRET}}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	specs, err := exports.FromConfig(d.Exports, d.DeclaredForExports())
	if err != nil || len(specs) != 1 || specs[0].Name != "oidc-client.grafana" || specs[0].Properties["client-secret"] != "GF_SECRET" {
		t.Errorf("%+v, %v", specs, err)
	}
}

func TestDeclaredForExportsSaysWhichClientsAreGenerated(t *testing.T) {
	t.Parallel()
	d, err := config.Load[config.PolicyDocument](write(t, oidcBase))
	if err != nil {
		t.Fatal(err)
	}
	got := d.DeclaredForExports().Clients
	if len(got) != 3 || !got["grafana"] || got["argocd"] || got["console"] {
		t.Errorf("clients = %v", got)
	}
}

func TestAnOIDCClientExportTheDocumentRefuses(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct{ export, want string }{
		"an unknown client":            {"{source: oidc-client, client: nobody, path: a/b}", "not a client of the policy"},
		"a client with a named secret": {"{source: oidc-client, client: argocd, path: a/b}", "does not have `secret: {generate: true}`"},
		"a public client":              {"{source: oidc-client, client: console, path: a/b}", "does not have `secret: {generate: true}`"},
		"no client":                    {"{source: oidc-client, path: a/b}", "missing property 'client'"},
		"a stray app":                  {"{source: oidc-client, client: grafana, app: x, path: a/b}", "no app, tier, org or bundle"},
		"client on a Slack export":     {"{source: slack-app, app: alerts, client: grafana, path: a/b}", "client"},
	} {
		_, err := config.Load[config.PolicyDocument](write(t, oidcBase+"exports: ["+c.export+"]\n"))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}
