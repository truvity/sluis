package policy_test

import (
	"encoding/json"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/policy"
)

func TestClientSecretRoundTrips(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		secret   policy.ClientSecret
		wantYAML string
		wantJSON string
	}{
		{"a name", policy.ClientSecret{Name: "grafana"}, "grafana\n", `"grafana"`},
		{"generate", policy.ClientSecret{Generate: true}, "generate: true\n", `{"generate":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			y, err := yaml.Marshal(tc.secret)
			if err != nil {
				t.Fatalf("yaml marshal: %v", err)
			}
			if string(y) != tc.wantYAML {
				t.Errorf("yaml = %q, want %q", y, tc.wantYAML)
			}
			var fromYAML policy.ClientSecret
			if err = yaml.Unmarshal(y, &fromYAML); err != nil || fromYAML != tc.secret {
				t.Errorf("yaml round trip = %+v, %v; want %+v", fromYAML, err, tc.secret)
			}
			j, err := json.Marshal(tc.secret)
			if err != nil {
				t.Fatalf("json marshal: %v", err)
			}
			if string(j) != tc.wantJSON {
				t.Errorf("json = %s, want %s", j, tc.wantJSON)
			}
			var fromJSON policy.ClientSecret
			if err = json.Unmarshal(j, &fromJSON); err != nil || fromJSON != tc.secret {
				t.Errorf("json round trip = %+v, %v; want %+v", fromJSON, err, tc.secret)
			}
		})
	}
}

func TestClientSecretInsideAClientRoundTrips(t *testing.T) {
	t.Parallel()
	for _, secret := range []policy.ClientSecret{{Name: "grafana"}, {Generate: true}} {
		in := policy.Client{Kind: policy.KindConfidential, Secret: secret, Requires: []string{"a"}}
		y, err := yaml.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var out policy.Client
		if err = yaml.Unmarshal(y, &out); err != nil {
			t.Fatalf("%s: %v", y, err)
		}
		if out.Secret != secret {
			t.Errorf("yaml %q: secret = %+v, want %+v", y, out.Secret, secret)
		}
		j, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		out = policy.Client{}
		if err = json.Unmarshal(j, &out); err != nil || out.Secret != secret {
			t.Errorf("json %s: secret = %+v, %v; want %+v", j, out.Secret, err, secret)
		}
	}
	// An absent secret stays absent, in both encodings.
	y, _ := yaml.Marshal(policy.Client{Kind: policy.KindPublic, Requires: []string{"a"}})
	if strings.Contains(string(y), "secret") {
		t.Errorf("an absent secret was written: %s", y)
	}
	j, _ := json.Marshal(policy.Client{Kind: policy.KindPublic, Requires: []string{"a"}})
	var back policy.Client
	if err := json.Unmarshal(j, &back); err != nil || !back.Secret.IsZero() {
		t.Errorf("json without a secret: %+v, %v", back.Secret, err)
	}
}

func TestClientSecretRefusals(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ yaml, json, want string }{
		"generate false":  {"{generate: false}", `{"generate":false}`, "generate: false"},
		"no generate key": {"{}", `{}`, "{generate: true}"},
		"extra key":       {"{generate: true, name: x}", `{"generate":true,"name":"x"}`, "unknown key"},
		"only other key":  {"{name: x}", `{"name":"x"}`, "unknown key"},
		"generate string": {`{generate: "true"}`, `{"generate":"true"}`, "{generate: true}"},
		"generate number": {"{generate: 1}", `{"generate":1}`, "{generate: true}"},
		"generate null":   {"{generate: null}", `{"generate":null}`, "{generate: true}"},
		"a sequence":      {"[a, b]", `["a","b"]`, "name or {generate: true}"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var s policy.ClientSecret
			if err := yaml.Unmarshal([]byte(tc.yaml), &s); err == nil {
				t.Errorf("yaml %s was accepted as %+v", tc.yaml, s)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("yaml error %q lacks %q", err, tc.want)
			}
			var j policy.ClientSecret
			if err := json.Unmarshal([]byte(tc.json), &j); err == nil {
				t.Errorf("json %s was accepted as %+v", tc.json, j)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("json error %q lacks %q", err, tc.want)
			}
		})
	}
}

func TestClientSecretAccessors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		secret    policy.ClientSecret
		name      string
		generated bool
		zero      bool
	}{
		{policy.ClientSecret{}, "", false, true},
		{policy.ClientSecret{Name: "grafana"}, "grafana", false, false},
		{policy.ClientSecret{Generate: true}, "", true, false},
	} {
		c := policy.Client{Secret: tc.secret}
		if c.SecretName() != tc.name || c.SecretGenerated() != tc.generated || tc.secret.IsZero() != tc.zero {
			t.Errorf("%+v: name %q generated %v zero %v", tc.secret, c.SecretName(), c.SecretGenerated(), tc.secret.IsZero())
		}
	}
}

func TestParseClientSecretShapes(t *testing.T) {
	t.Parallel()
	const head = "version: 1\ngroups: { a: { members: [g@h.example] } }\nclients:\n  c: "
	t.Run("accepted", func(t *testing.T) {
		t.Parallel()
		for name, tc := range map[string]struct {
			client    string
			name      string
			generated bool
		}{
			"name":     {"{ kind: confidential, secret: grafana, requires: [a] }", "grafana", false},
			"generate": {"{ kind: confidential, secret: { generate: true }, requires: [a] }", "", true},
			"block":    {"\n    kind: confidential\n    secret:\n      generate: true\n    requires: [a]", "", true},
		} {
			p, err := policy.Parse([]byte(head + tc.client + "\n"))
			if err == nil {
				err = p.Validate()
			}
			if err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			c := p.Clients["c"]
			if c.SecretName() != tc.name || c.SecretGenerated() != tc.generated {
				t.Errorf("%s: name %q generated %v", name, c.SecretName(), c.SecretGenerated())
			}
		}
	})
	t.Run("refused", func(t *testing.T) {
		t.Parallel()
		for name, tc := range map[string]struct{ client, want string }{
			"generate false":          {"{ kind: confidential, secret: { generate: false }, requires: [a] }", "generate: false"},
			"object without generate": {"{ kind: confidential, secret: {}, requires: [a] }", "{generate: true}"},
			"extra key":               {"{ kind: confidential, secret: { generate: true, x: 1 }, requires: [a] }", "unknown key"},
			"non-bool generate":       {"{ kind: confidential, secret: { generate: yes-please }, requires: [a] }", "{generate: true}"},
			"a sequence":              {"{ kind: confidential, secret: [a], requires: [a] }", "name or {generate: true}"},
			"generate on public":      {"{ kind: public, secret: { generate: true }, requires: [a] }", "only a confidential client"},
			"generate on exchange":    {"{ kind: exchange, secret: { generate: true }, requires: [a] }", "only a confidential client"},
			"confidential no secret":  {"{ kind: confidential, requires: [a] }", "names no secret"},
		} {
			p, err := policy.Parse([]byte(head + tc.client + "\n"))
			if err == nil {
				err = p.Validate()
			}
			if err == nil {
				t.Errorf("%s: accepted", name)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: error %q lacks %q", name, err, tc.want)
			}
		}
	})
}
