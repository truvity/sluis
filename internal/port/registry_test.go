package port_test

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/port"
	_ "github.com/truvity/sluis/internal/port/dynamodb" // register
	_ "github.com/truvity/sluis/internal/port/legacy"   // register
	_ "github.com/truvity/sluis/internal/port/memory"   // register
	_ "github.com/truvity/sluis/internal/port/nats"     // register
	_ "github.com/truvity/sluis/internal/port/s3blob"   // register
)

// fake is a registry holding the adapters the aws-hybrid preset names, as
// their sibling packages will register them.
func fake() *port.Registry {
	r := port.NewRegistry()
	add := func(c port.Concern, name string, d port.Descriptor) {
		d.Concern, d.Name = c, name
		r.Register(d)
	}
	aws := port.Requires{AWS: true}
	add(port.ConcernState, "dynamodb", port.Descriptor{Requires: aws})
	add(port.ConcernState, "memory", port.Descriptor{ProcessLocal: true})
	add(port.ConcernState, "legacy", port.Descriptor{
		Requires: port.Requires{Kubernetes: true}, Runtimes: []port.Runtime{port.RuntimeKubernetes, port.RuntimeProcess},
	})
	add(port.ConcernSecrets, "ssm", port.Descriptor{Requires: aws, SecretStore: true})
	add(port.ConcernSecrets, "memory", port.Descriptor{ProcessLocal: true})
	add(port.ConcernBlobs, "s3", port.Descriptor{Requires: aws})
	add(port.ConcernSigning, "kms", port.Descriptor{Requires: aws})
	add(port.ConcernTrigger, "invoke", port.Descriptor{Requires: aws})
	add(port.ConcernSchedule, "eventbridge", port.Descriptor{Requires: aws})
	add(port.ConcernAudit, "sqs", port.Descriptor{Requires: aws})
	return r
}

func TestThePresetTreeFollowsTheFourQuestions(t *testing.T) {
	for name, tc := range map[string]struct {
		p    port.Platform
		want port.Preset
	}{
		"nothing":            {port.Platform{}, port.PresetServer},
		"kubernetes":         {port.Platform{Kubernetes: true}, port.PresetK8sMinimal},
		"kubernetes+openbao": {port.Platform{Kubernetes: true, OpenBao: true}, port.PresetK8sOpenBao},
		"aws":                {port.Platform{AWS: true}, port.PresetAWSServerless},
		"aws+k8s on lambda":  {port.Platform{AWS: true, Kubernetes: true, Runtime: port.RuntimeLambda}, port.PresetAWSHybrid},
		"aws+k8s on k8s":     {port.Platform{AWS: true, Kubernetes: true}, port.PresetAWSEKS},
		"openbao alone":      {port.Platform{OpenBao: true}, port.PresetServer},
	} {
		if got := port.PresetFor(tc.p); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}

func TestEveryPresetNamesEveryConcern(t *testing.T) {
	for _, p := range port.Presets {
		for _, c := range port.Concerns {
			if port.PresetTable(p)[c] == "" {
				t.Errorf("preset %s names no %s adapter", p, c)
			}
		}
	}
}

func TestOverrideBeatsPresetBeatsDerivedBeatsLegacy(t *testing.T) {
	legacy := port.Table{}
	for _, c := range port.Concerns {
		legacy[c] = port.Choice{Adapter: "L"}
	}
	plat := &port.Platform{AWS: true, Kubernetes: true, Runtime: port.RuntimeLambda}

	got, err := port.Resolve(port.Selection{Legacy: legacy})
	if err != nil || got.Name(port.ConcernState) != "L" || got[port.ConcernState].Source != port.SourceLegacy {
		t.Fatalf("no keys: %v %v", got, err)
	}
	got, _ = port.Resolve(port.Selection{Legacy: legacy, Platform: plat})
	if got.Name(port.ConcernState) != "dynamodb" || got[port.ConcernState].Source != port.SourceDerived {
		t.Fatalf("platform: %v", got)
	}
	got, _ = port.Resolve(port.Selection{Legacy: legacy, Platform: plat, Preset: port.PresetAWSEKS})
	if got.Name(port.ConcernTrigger) != "watch" || got[port.ConcernTrigger].Source != port.SourcePreset {
		t.Fatalf("preset beats the derived one: %v", got)
	}
	got, _ = port.Resolve(port.Selection{Legacy: legacy, Preset: port.PresetAWSHybrid, LegacySet: []port.Concern{port.ConcernState}})
	if got.Name(port.ConcernState) != "L" || got[port.ConcernState].Source != port.SourceOverride {
		t.Fatalf("a legacy key beside a preset overrides it: %v", got)
	}
	got, _ = port.Resolve(port.Selection{Legacy: legacy, Preset: port.PresetAWSHybrid, LegacySet: []port.Concern{port.ConcernState},
		Overrides: map[port.Concern]port.Override{port.ConcernState: {Adapter: "memory"}}})
	if got.Name(port.ConcernState) != "memory" {
		t.Fatalf("an explicit override wins over all: %v", got)
	}
	if _, err := port.Resolve(port.Selection{Legacy: legacy, Preset: "bogus"}); err == nil {
		t.Error("an unknown preset resolved")
	}
	if _, err := port.Resolve(port.Selection{Legacy: legacy, Overrides: map[port.Concern]port.Override{"nope": {Adapter: "x"}}}); err == nil {
		t.Error("an unknown concern resolved")
	}
}

func TestValidate(t *testing.T) {
	r := fake()
	hybrid := port.Platform{AWS: true, Kubernetes: true, Runtime: port.RuntimeLambda}
	table := func(t *testing.T, sel port.Selection) port.Table {
		t.Helper()
		tb, err := port.Resolve(sel)
		if err != nil {
			t.Fatal(err)
		}
		return tb
	}
	hy := table(t, port.Selection{Platform: &hybrid})
	if err := r.Validate(hy, port.Env{Answers: &hybrid, Runtime: port.RuntimeLambda}); err != nil {
		t.Fatalf("aws-hybrid on lambda: %v", err)
	}

	for name, tc := range map[string]struct {
		tbl  port.Table
		env  port.Env
		want string
	}{
		"needs AWS": {hy, port.Env{Answers: &port.Platform{Kubernetes: true}}, "needs AWS"},
		"legacy on lambda": {
			table(t, port.Selection{Platform: &hybrid, Overrides: map[port.Concern]port.Override{port.ConcernState: {Adapter: "legacy"}}}),
			port.Env{Runtime: port.RuntimeLambda}, "cannot run on the lambda runtime",
		},
		"memory with two replicas": {
			table(t, port.Selection{Platform: &hybrid, Overrides: map[port.Concern]port.Override{port.ConcernState: {Adapter: "memory"}}}),
			port.Env{Replicas: 2}, "2 replicas",
		},
		"not a secret store": {
			table(t, port.Selection{Platform: &hybrid, Overrides: map[port.Concern]port.Override{port.ConcernSecrets: {Adapter: "store"}}}),
			port.Env{Answers: &hybrid}, "is planned",
		},
		"planned adapter": {
			table(t, port.Selection{Platform: &hybrid, Overrides: map[port.Concern]port.Override{port.ConcernBlobs: {Adapter: "postgres"}}}),
			port.Env{}, "planned (on request)",
		},
		"unknown adapter": {
			table(t, port.Selection{Platform: &hybrid, Overrides: map[port.Concern]port.Override{port.ConcernBlobs: {Adapter: "gcs"}}}),
			port.Env{}, "unknown",
		},
	} {
		err := r.Validate(tc.tbl, tc.env)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want it to say %q", name, err, tc.want)
		}
	}
}

func TestAnUnrealSecretStoreIsRefusedWhereARealOneExists(t *testing.T) {
	r := fake()
	r.Register(port.Descriptor{Name: "store", Concern: port.ConcernSecrets})
	hybrid := port.Platform{AWS: true, Kubernetes: true, Runtime: port.RuntimeLambda}
	tb, _ := port.Resolve(port.Selection{Platform: &hybrid, Overrides: map[port.Concern]port.Override{port.ConcernSecrets: {Adapter: "store"}}})
	if err := r.Validate(tb, port.Env{Answers: &hybrid}); err == nil || !strings.Contains(err.Error(), "not a secret store") {
		t.Fatalf("store on a platform with a secret store: %v", err)
	}
	none := port.Platform{}
	tb, _ = port.Resolve(port.Selection{Platform: &none, Overrides: map[port.Concern]port.Override{
		port.ConcernState: {Adapter: "memory"}, port.ConcernSecrets: {Adapter: "store"}, port.ConcernBlobs: {Adapter: "s3"},
		port.ConcernSigning: {Adapter: "kms"}, port.ConcernTrigger: {Adapter: "invoke"},
		port.ConcernSchedule: {Adapter: "eventbridge"}, port.ConcernAudit: {Adapter: "sqs"},
	}})
	err := r.Validate(tb, port.Env{Answers: &none})
	if err != nil && strings.Contains(err.Error(), "not a secret store") {
		t.Fatalf("store with no platform store was refused: %v", err)
	}
}

func TestTheMatrixHoldsRegisteredAndPlannedAdapters(t *testing.T) {
	m := port.Default.Matrix()
	seen := map[string]port.Status{}
	for _, d := range m {
		seen[string(d.Concern)+"/"+d.Name] = d.Status
	}
	for key, want := range map[string]port.Status{
		"state/legacy": port.StatusImplemented, "state/dynamodb": port.StatusImplemented, "state/postgres": port.StatusOnRequest,
		"secrets/memory": port.StatusImplemented, "secrets/openbao": port.StatusOnRequest, "blobs/s3": port.StatusImplemented,
		"signing/file": port.StatusImplemented, "signing/transit": port.StatusOnRequest, "trigger/watch": port.StatusOnRequest,
		"schedule/ticker": port.StatusImplemented, "audit/connect": port.StatusImplemented,
	} {
		if got, ok := seen[key]; !ok || got != want {
			t.Errorf("%s: %q (present %v), want %q", key, got, ok, want)
		}
	}
	for _, d := range port.Catalogue {
		if d.Status != port.StatusOnRequest || d.Factory != nil {
			t.Errorf("catalogue entry %s/%s is not a plain planned one", d.Concern, d.Name)
		}
	}
}

func TestRegisterRefusesDuplicatesAndPlanned(t *testing.T) {
	r := port.NewRegistry()
	r.Register(port.Descriptor{Name: "x", Concern: port.ConcernState})
	for name, d := range map[string]port.Descriptor{
		"duplicate": {Name: "x", Concern: port.ConcernState},
		"planned":   {Name: "y", Concern: port.ConcernState, Status: port.StatusOnRequest},
		"concern":   {Name: "y", Concern: "nope"},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			r.Register(d)
		}()
	}
}

func TestSettingsDecodeRefusesAnUnknownKey(t *testing.T) {
	var v struct{ KeyID string }
	if err := (port.Settings{"keyId": "k"}).Decode(&v); err != nil || v.KeyID != "k" {
		t.Fatalf("%v %+v", err, v)
	}
	if err := (port.Settings{"keyid": "k", "typo": 1}).Decode(&v); err == nil {
		t.Fatal("an unknown key was accepted")
	}
}
