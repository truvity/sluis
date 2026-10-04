package store

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/port"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// With none of the new keys, the table is what the `ports` keys have always
// meant, whichever adapter they name.
func TestWithoutTheNewKeysTheLegacyKeysDecide(t *testing.T) {
	for _, tc := range []struct {
		adapter string
		state   string
		trigger string
		blobs   string
	}{
		{AdapterLegacy, "legacy", "legacy", "legacy"},
		{AdapterMemory, "memory", "memory", "memory"},
		{AdapterDynamoDB, "dynamodb", "dynamodb", "legacy"},
	} {
		got, table, err := Config{Adapter: tc.adapter, Kube: KubeNone}.plan(context.Background(), quiet)
		if err != nil {
			t.Fatalf("%s: %v", tc.adapter, err)
		}
		if got.Adapter != tc.adapter {
			t.Errorf("%s: the plan changed the adapter to %s", tc.adapter, got.Adapter)
		}
		if table.Name(port.ConcernState) != tc.state || table.Name(port.ConcernTrigger) != tc.trigger ||
			table.Name(port.ConcernBlobs) != tc.blobs || table.Name(port.ConcernSigning) != "file" ||
			table.Name(port.ConcernSchedule) != "ticker" || table.Name(port.ConcernAudit) != "log" {
			t.Errorf("%s: %s", tc.adapter, table)
		}
	}
	_, table, _ := Config{Adapter: AdapterLegacy, sel: selection{AuditWriter: true}}.plan(context.Background(), quiet)
	if table.Name(port.ConcernAudit) != "connect" {
		t.Errorf("an audit writer is the connect sink: %s", table)
	}
	c, table, err := Config{Adapter: AdapterLegacy, Blob: &config.PortsBlob{Adapter: "s3", S3: &config.PortsBlobS3{Bucket: "b"}}}.plan(context.Background(), quiet)
	if err != nil || table.Name(port.ConcernBlobs) != "s3" || c.Blob == nil {
		t.Errorf("ports.blob is the s3 blobs: %s %v", table, err)
	}
}

func TestPlatformChecksAndOverrides(t *testing.T) {
	ctx := context.Background()
	// Legacy needs Kubernetes: a platform that says there is none refuses it.
	_, _, err := Config{Adapter: AdapterLegacy, sel: selection{Platform: &config.Platform{Runtime: "process"}, Preset: "server",
		PortsAdapter: true}}.plan(ctx, quiet)
	if err == nil || !strings.Contains(err.Error(), "needs Kubernetes") {
		t.Errorf("legacy on a platform with no cluster: %v", err)
	}
	// Memory beyond one replica.
	_, _, err = Config{Adapter: AdapterMemory, sel: selection{Platform: &config.Platform{Kubernetes: true, Replicas: 2}, PortsAdapter: true}}.plan(ctx, quiet)
	if err == nil || !strings.Contains(err.Error(), "2 replicas") {
		t.Errorf("memory with two replicas: %v", err)
	}
	// A planned adapter.
	_, _, err = Config{Adapter: AdapterLegacy, sel: selection{Adapters: map[string]config.AdapterChoice{"state": {Adapter: "postgres"}}}}.plan(ctx, quiet)
	if err == nil || !strings.Contains(err.Error(), "planned") {
		t.Errorf("postgres: %v", err)
	}
	// An override carries its settings into the Config.
	c, table, err := Config{Adapter: AdapterLegacy, sel: selection{Adapters: map[string]config.AdapterChoice{
		"state": {Adapter: "dynamodb", Settings: map[string]any{"table": "t", "region": "eu-west-1"}},
		"blobs": {Adapter: "s3", Settings: map[string]any{"bucket": "b", "pathStyle": true}},
	}}}.plan(ctx, quiet)
	if err != nil || c.Adapter != AdapterDynamoDB || c.DynamoDB.Table != "t" || c.Blob == nil || !c.Blob.S3.PathStyle {
		t.Fatalf("override: %+v %v", c, err)
	}
	if table[port.ConcernState].Source != port.SourceOverride {
		t.Errorf("source: %s", table[port.ConcernState].Source)
	}
	// A typo in the settings is refused.
	_, _, err = Config{Adapter: AdapterLegacy, sel: selection{Adapters: map[string]config.AdapterChoice{
		"state": {Adapter: "dynamodb", Settings: map[string]any{"tabel": "t"}}}}}.plan(ctx, quiet)
	if err == nil {
		t.Error("a settings typo was accepted")
	}
	// A preset whose adapters are not all registered refuses and says which.
	_, _, err = Config{Adapter: AdapterLegacy, sel: selection{Preset: "k8s-minimal"}}.plan(ctx, quiet)
	if err == nil || !strings.Contains(err.Error(), "planned") {
		t.Errorf("k8s-minimal: %v", err)
	}
}

func TestOpenKeepsTheMemoryAdapterWorking(t *testing.T) {
	st, err := Open(context.Background(), Config{Adapter: AdapterMemory}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.Plan.Name(port.ConcernState) != "memory" {
		t.Errorf("plan: %s", st.Plan)
	}
}

func TestLambdaRefusesLegacy(t *testing.T) {
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "sluis")
	_, _, err := Config{Adapter: AdapterLegacy}.plan(context.Background(), quiet)
	if err == nil || !strings.Contains(err.Error(), "lambda") {
		t.Errorf("legacy on Lambda: %v", err)
	}
	// A retired adapter is no adapter at all.
	_, _, err = Config{Adapter: "nats"}.plan(context.Background(), quiet)
	if err == nil {
		t.Error("nats was planned")
	}
}

// A secrets adapter that nobody chose is not built, so the kernel and hive,
// which name none, run as they did; one that was chosen is the Secrets port of
// the set.
func TestTheSecretsConcernIsWiredFromThePlan(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		cfg  Config
		want bool
	}{
		{"legacy keys", Config{Adapter: AdapterLegacy, Kube: KubeNone}, false},
		{"memory state names memory secrets, by the legacy table", Config{Adapter: AdapterMemory, Kube: KubeNone}, false},
		{"memory chosen", Config{Adapter: AdapterLegacy, Kube: KubeNone, sel: selection{
			Adapters: map[string]config.AdapterChoice{"secrets": {Adapter: "memory"}}}}, true},
		{"ssm chosen", Config{Adapter: AdapterLegacy, Kube: KubeNone, sel: selection{
			Adapters: map[string]config.AdapterChoice{"secrets": {Adapter: "ssm", Settings: map[string]any{"root": "/test"}}}}}, true},
	} {
		c, table, err := tc.cfg.plan(ctx, quiet)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		set, err := c.compose(ctx, port.Set{}, quiet)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := set.Secrets != nil; got != tc.want {
			t.Errorf("%s: Secrets set = %v, want %v (%s)", tc.name, got, tc.want, table)
		}
	}
}

func TestAnSsmSettingNobodyKnowsIsRefusedAtStart(t *testing.T) {
	cfg := Config{Adapter: AdapterLegacy, Kube: KubeNone, sel: selection{
		Adapters: map[string]config.AdapterChoice{"secrets": {Adapter: "ssm", Settings: map[string]any{"rooot": "/x"}}}}}
	c, _, err := cfg.plan(context.Background(), quiet)
	if err == nil {
		_, err = c.compose(context.Background(), port.Set{}, quiet)
	}
	if err == nil || !strings.Contains(err.Error(), "rooot") {
		t.Fatalf("err = %v, want one naming the unknown setting", err)
	}
}

// `signingKey.kms` is the legacy spelling of the kms signing adapter, and a
// `signingKey.file` beside a preset that picks kms still means file.
func TestSigningKeyIsTheLegacyMappingOfTheSigningAdapter(t *testing.T) {
	kms := &config.SigningKeyKMS{Keys: []string{"alias/a"}, StateSecretFile: "/s"}
	_, table, err := Config{Adapter: AdapterLegacy, Kube: KubeNone,
		sel: selection{SigningKMS: kms}}.plan(context.Background(), quiet)
	if err != nil {
		t.Fatal(err)
	}
	if table.Name(port.ConcernSigning) != "kms" || table[port.ConcernSigning].Settings["stateSecretFile"] != "/s" {
		t.Fatalf("got %+v", table[port.ConcernSigning])
	}

	table, err = port.Resolve(port.Selection{
		Legacy:    Config{Adapter: AdapterLegacy, sel: selection{SigningFile: true}}.legacyTable(),
		Preset:    "aws-serverless",
		LegacySet: []port.Concern{port.ConcernSigning},
	})
	if err != nil {
		t.Fatal(err)
	}
	if table.Name(port.ConcernSigning) != "file" {
		t.Fatalf("signing = %s, want file", table.Name(port.ConcernSigning))
	}
}

// A bare `adapters.signing: {adapter: kms}` leaves the settings where they have
// always been, in `signingKey.kms`, and does not hide them.
func TestABareSigningAdapterKeepsTheSigningKeySettings(t *testing.T) {
	ctx := context.Background()
	kms := &config.SigningKeyKMS{Keys: []string{"alias/a"}, StateSecretFile: "/s"}
	_, table, err := Config{Adapter: AdapterLegacy, sel: selection{
		SigningKMS: kms, Adapters: map[string]config.AdapterChoice{"signing": {Adapter: "kms"}},
	}}.plan(ctx, quiet)
	if err != nil {
		t.Fatal(err)
	}
	got := table[port.ConcernSigning]
	if got.Adapter != "kms" || got.Settings["stateSecretFile"] != "/s" || got.Source != port.SourceOverride {
		t.Errorf("a bare kms adapter lost signingKey.kms: %+v", got)
	}

	// The wrapped adapter, too.
	wrapped := &config.SigningKeyKMSWrapped{KeyID: "alias/w", StateSecretFile: "/s"}
	_, table, err = Config{Adapter: AdapterLegacy, sel: selection{
		SigningWrapped: wrapped, Adapters: map[string]config.AdapterChoice{"signing": {Adapter: "kms-wrapped"}},
	}}.plan(ctx, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if got := table[port.ConcernSigning]; got.Adapter != "kms-wrapped" || got.Settings["keyId"] != "alias/w" {
		t.Errorf("a bare kms-wrapped adapter lost signingKey.kmsWrapped: %+v", got)
	}

	// Settings of its own win whole; another adapter's block is not borrowed.
	_, table, _ = Config{Adapter: AdapterLegacy, sel: selection{
		SigningKMS: kms, Adapters: map[string]config.AdapterChoice{"signing": {Adapter: "kms", Settings: map[string]any{"keys": []any{"alias/b"}, "stateSecretFile": "/other"}}},
	}}.plan(ctx, quiet)
	if got := table[port.ConcernSigning]; got.Settings["stateSecretFile"] != "/other" {
		t.Errorf("the override's own settings were replaced: %+v", got)
	}
	_, table, _ = Config{Adapter: AdapterLegacy, sel: selection{
		SigningKMS: kms, Adapters: map[string]config.AdapterChoice{"signing": {Adapter: "kms-wrapped"}},
	}}.plan(ctx, quiet)
	if got := table[port.ConcernSigning]; len(got.Settings) != 0 {
		t.Errorf("kms-wrapped borrowed signingKey.kms: %+v", got)
	}
}
