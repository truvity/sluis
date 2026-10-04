package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/observe"
)

// selection is what the file says about adapters beyond `ports.adapter`: the
// `platform` block, the `preset` and the per-concern `adapters`. All absent is
// the common case, and the `ports` keys alone decide, as they always did.
type selection struct {
	Platform *config.Platform
	Preset   string
	Adapters map[string]config.AdapterChoice
	// AuditWriter says an audit receiver is configured, which is what the
	// legacy audit sink `connect` means.
	AuditWriter bool
	// PortsAdapter and PortsBlob say the legacy keys were written, so beside
	// a preset they override it.
	PortsAdapter, PortsBlob bool
	// SigningFile and SigningKMS say `signingKey` names a key source, which is
	// the legacy spelling of the signing adapter: beside a preset it
	// overrides it, as `ports.adapter` does for state.
	SigningFile bool
	SigningKMS  *config.SigningKeyKMS
}

func selectionOf(
	p *config.Ports, plat *config.Platform, preset string, adapters map[string]config.AdapterChoice,
	auditWriter bool, sk *config.SigningKey,
) selection {
	sel := selection{
		Platform: plat, Preset: preset, Adapters: adapters, AuditWriter: auditWriter,
		PortsAdapter: p != nil && p.Adapter != "", PortsBlob: p != nil && p.Blob != nil,
	}
	if sk != nil {
		sel.SigningFile, sel.SigningKMS = sk.File != "", sk.KMS
	}
	return sel
}

// legacyTable is what the `ports` keys mean, as a table: the mapping of the
// old configuration onto the adapters by name. Nothing here is new behaviour.
func (c Config) legacyTable() port.Table {
	t := port.Table{
		port.ConcernState:    {Adapter: c.Adapter},
		port.ConcernSecrets:  {Adapter: "legacy"},
		port.ConcernBlobs:    {Adapter: "legacy"},
		port.ConcernTrigger:  {Adapter: "legacy"},
		port.ConcernSigning:  {Adapter: "file"},
		port.ConcernSchedule: {Adapter: "ticker"},
		port.ConcernAudit:    {Adapter: "log"},
	}
	switch c.Adapter {
	case AdapterMemory:
		for _, c := range []port.Concern{port.ConcernSecrets, port.ConcernBlobs, port.ConcernTrigger} {
			t[c] = port.Choice{Adapter: AdapterMemory}
		}
	case AdapterNATS, AdapterDynamoDB:
		t[port.ConcernTrigger] = port.Choice{Adapter: c.Adapter}
	}
	if c.Blob != nil {
		t[port.ConcernBlobs] = port.Choice{Adapter: c.Blob.Adapter}
	}
	if k := c.sel.SigningKMS; k != nil && !c.sel.SigningFile {
		// What `signingKey.kms` has always meant, as settings. Marshalling a
		// struct of strings cannot fail.
		raw, _ := json.Marshal(port.KMSSigning{Keys: k.Keys, Region: k.Region, StateSecretFile: k.StateSecretFile})
		var settings port.Settings
		_ = json.Unmarshal(raw, &settings)
		t[port.ConcernSigning] = port.Choice{Adapter: "kms", Settings: settings}
	}
	if c.sel.AuditWriter {
		t[port.ConcernAudit] = port.Choice{Adapter: "connect"}
	}
	return t
}

// plan resolves and validates the adapters, applies the choices this build
// wires, and announces the table. It returns the Config the adapters are then
// opened with.
func (c Config) plan(ctx context.Context, log *slog.Logger) (Config, port.Table, error) {
	sel := port.Selection{Legacy: c.legacyTable(), Preset: port.Preset(c.sel.Preset)}
	if p := c.sel.Platform; p != nil {
		sel.Platform = &port.Platform{AWS: p.AWS, Kubernetes: p.Kubernetes, OpenBao: p.OpenBao, Runtime: port.Runtime(p.Runtime), Replicas: p.Replicas}
	}
	if c.sel.PortsAdapter {
		sel.LegacySet = append(sel.LegacySet, port.ConcernState)
	}
	if c.sel.PortsBlob {
		sel.LegacySet = append(sel.LegacySet, port.ConcernBlobs)
	}
	if c.sel.SigningFile || c.sel.SigningKMS != nil {
		sel.LegacySet = append(sel.LegacySet, port.ConcernSigning)
	}
	if len(c.sel.Adapters) > 0 {
		sel.Overrides = map[port.Concern]port.Override{}
		for k, v := range c.sel.Adapters {
			sel.Overrides[port.Concern(k)] = port.Override{Adapter: v.Adapter, Settings: v.Settings}
		}
	}
	table, err := port.Resolve(sel)
	if err != nil {
		return c, nil, err
	}
	env := port.Env{}
	switch {
	case sel.Platform != nil:
		answers := *sel.Platform
		env.Answers, env.Runtime, env.Replicas = &answers, answers.RuntimeOf(), answers.Replicas
	case sel.Preset != "":
		answers := sel.Preset.Answers()
		env.Answers, env.Runtime = &answers, answers.Runtime
	}
	if env.Runtime == "" && os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
		env.Runtime = port.RuntimeLambda
	}
	if err := port.Default.Validate(table, env); err != nil {
		return c, nil, fmt.Errorf("adapters: %w", err)
	}
	if c, err = c.apply(table); err != nil {
		return c, nil, err
	}
	observe.Announce(ctx, log, table)
	return c, table, nil
}

// apply carries the choices for the concerns this build consumes into the
// Config. State, blobs and secrets are consumed here; the others are announced
// and await the packages that wire them. A choice that is registered but that
// nothing consumes yet is refused for state and blobs, where ignoring it would
// run on another store than the one named, and logged for the rest.
func (c Config) apply(t port.Table) (Config, error) {
	switch st := t[port.ConcernState]; st.Adapter {
	case AdapterLegacy, AdapterMemory, AdapterNATS, AdapterDynamoDB:
		c.Adapter = st.Adapter
		if len(st.Settings) > 0 {
			switch st.Adapter {
			case AdapterNATS:
				var n config.NATS
				if err := decodeStrict(st.Settings, &n); err != nil {
					return c, fmt.Errorf("adapters.state.settings: %w", err)
				}
				c.NATS = natsOf(&config.Ports{NATS: &n})
			case AdapterDynamoDB:
				var d config.DynamoDB
				if err := decodeStrict(st.Settings, &d); err != nil {
					return c, fmt.Errorf("adapters.state.settings: %w", err)
				}
				c.DynamoDB = dynamoOf(&config.Ports{DynamoDB: &d})
			default:
				return c, fmt.Errorf("adapters.state.settings: the %s adapter has none", st.Adapter)
			}
		}
	default:
		return c, fmt.Errorf("adapters: state adapter %q is registered but this build does not serve with it yet", st.Adapter)
	}
	switch b := t[port.ConcernBlobs]; b.Adapter {
	case "legacy", AdapterMemory:
		// What the state adapter brings; `memory` is the memory state's.
		if b.Adapter == AdapterMemory && c.Adapter != AdapterMemory {
			return c, fmt.Errorf("adapters: blobs adapter %q needs state adapter %q", b.Adapter, AdapterMemory)
		}
		c.Blob = nil
	case BlobS3:
		if len(b.Settings) > 0 {
			var s3 config.PortsBlobS3
			if err := decodeStrict(b.Settings, &s3); err != nil {
				return c, fmt.Errorf("adapters.blobs.settings: %w", err)
			}
			c.Blob = &config.PortsBlob{Adapter: BlobS3, S3: &s3}
		}
		if c.Blob == nil {
			return c, fmt.Errorf("adapters: blobs adapter %q needs settings: set adapters.blobs.settings.bucket or ports.blob.s3", BlobS3)
		}
	default:
		return c, fmt.Errorf("adapters: blobs adapter %q is registered but this build does not serve with it yet", b.Adapter)
	}
	// Secrets: the legacy table names `legacy` (and `memory` beside the memory
	// state) for a deployment that chose nothing, and that keeps today's
	// behaviour of no Secrets port. Only a choice somebody made is built.
	if sc := t[port.ConcernSecrets]; sc.Adapter != "" && sc.Adapter != "legacy" && sc.Source != port.SourceLegacy {
		c.secrets = &sc
	}
	return c, nil
}

// secretsOf builds the Secrets port the plan chose, nil when none was chosen.
func (c Config) secretsOf(ctx context.Context) (port.Secrets, error) {
	if c.secrets == nil {
		return nil, nil
	}
	d, ok := port.Default.Lookup(port.ConcernSecrets, c.secrets.Adapter)
	if !ok || d.Factory == nil {
		return nil, fmt.Errorf("adapters: secrets adapter %q is registered but this build does not serve with it yet", c.secrets.Adapter)
	}
	built, err := d.Factory(ctx, c.secrets.Settings)
	if err != nil {
		return nil, fmt.Errorf("adapters.secrets: %w", err)
	}
	secrets, ok := built.(port.Secrets)
	if !ok {
		return nil, fmt.Errorf("adapters.secrets: %q built a %T, which is not a port.Secrets", c.secrets.Adapter, built)
	}
	return secrets, nil
}

// decodeStrict reads a settings object into v, refusing a key v lacks.
func decodeStrict(s port.Settings, v any) error { return s.Decode(v) }
