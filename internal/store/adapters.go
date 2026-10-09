package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/observe"
	"github.com/truvity/sluis/internal/secretstore"
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
	SigningFile    bool
	SigningKMS     *config.SigningKeyKMS
	SigningWrapped *config.SigningKeyKMSWrapped
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
		sel.SigningFile, sel.SigningKMS, sel.SigningWrapped = sk.File != "", sk.KMS, sk.KMSWrapped
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
	case AdapterDynamoDB:
		t[port.ConcernTrigger] = port.Choice{Adapter: c.Adapter}
	}
	if c.Blob != nil {
		t[port.ConcernBlobs] = port.Choice{Adapter: c.Blob.Adapter}
	}
	if k := c.sel.SigningKMS; k != nil && !c.sel.SigningFile {
		// What `signingKey.kms` has always meant, as settings. Marshalling a
		// struct of strings cannot fail.
		legacy := port.KMSSigning{Keys: k.Keys, Region: k.Region, StateSecret: k.StateSecret}
		for _, a := range k.Additional {
			legacy.Additional = append(legacy.Additional, port.KMSSigningAlg{Alg: a.Alg, Keys: a.Keys})
		}
		raw, _ := json.Marshal(legacy)
		var settings port.Settings
		_ = json.Unmarshal(raw, &settings)
		t[port.ConcernSigning] = port.Choice{Adapter: "kms", Settings: settings}
	}
	if k := c.sel.SigningWrapped; k != nil && !c.sel.SigningFile {
		legacy := port.KMSWrappedSigning{KeyID: k.KeyID, Region: k.Region, StateSecret: k.StateSecret, Algorithms: k.Algorithms}
		if k.RotateEvery != nil {
			legacy.RotateEvery = k.RotateEvery.D().String()
		}
		if k.Prepublish != nil {
			legacy.Prepublish = k.Prepublish.D().String()
		}
		if k.Retain != nil {
			legacy.Retain = k.Retain.D().String()
		}
		raw, _ := json.Marshal(legacy)
		var settings port.Settings
		_ = json.Unmarshal(raw, &settings)
		t[port.ConcernSigning] = port.Choice{Adapter: "kms-wrapped", Settings: settings}
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
	if w := port.Preset(c.sel.Preset).Deprecated(); w != "" {
		log.WarnContext(ctx, "adapter preset warning", slog.String("warning", w))
	}
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
	if c.sel.SigningFile || c.sel.SigningKMS != nil || c.sel.SigningWrapped != nil {
		sel.LegacySet = append(sel.LegacySet, port.ConcernSigning)
	}
	if len(c.sel.Adapters) > 0 {
		sel.Overrides = map[port.Concern]port.Override{}
		for k, v := range c.sel.Adapters {
			sel.Overrides[port.Concern(k)] = port.Override{Adapter: v.Adapter, Settings: v.Settings}
		}
		// A bare `adapters.signing: {adapter: kms}` names the adapter and leaves
		// its settings where they have always been, `signingKey.kms`: an override
		// with no settings of its own takes the legacy block's, when the block is
		// for the same adapter. Settings given in the override win whole.
		if o, ok := sel.Overrides[port.ConcernSigning]; ok {
			legacy := sel.Legacy[port.ConcernSigning]
			switch {
			case len(o.Settings) == 0 && legacy.Adapter == o.Adapter && len(legacy.Settings) > 0:
				o.Settings = legacy.Settings
				sel.Overrides[port.ConcernSigning] = o
			case len(o.Settings) > 0 && o.Adapter == "kms-wrapped" && legacy.Adapter == o.Adapter && len(legacy.Settings) > 0:
				// Two statements of one key's settings: refused, not one preferred.
				return c, nil, errors.New("adapters.signing.settings and signingKey.kmsWrapped are both set: " +
					"state the kms-wrapped settings in one of them")
			}
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
		env.PlatformStated = true
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
	case AdapterLegacy, AdapterMemory, AdapterDynamoDB:
		c.Adapter = st.Adapter
		if len(st.Settings) > 0 {
			switch st.Adapter {
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

// ssmLayoutV2Root is the SSM root of layout v2, which a document converted
// from v1 keeps when it names none.
const ssmLayoutV2Root = "/sluis"

// ssmRoot is the ssm Secrets adapter's settings with its root: the serve
// document's `secrets.root` when its source is ssm, which is the one root of an
// installation (/sluis/<instance>, layout v3). The adapter may name it again,
// and naming another is refused: one installation, one root. A document
// converted from v1 that names neither keeps /sluis (layout v2) until it moves.
func (c Config) ssmRoot(settings port.Settings) (port.Settings, error) {
	named, _ := settings["root"].(string)
	want := c.SecretsRoot
	switch {
	case want != "" && named != "" && named != want:
		return nil, fmt.Errorf("adapters.secrets: the ssm adapter's root %q is not secrets.root %q: an installation has one root", named, want)
	case named != "":
		return settings, nil
	case want == "" && c.Converted:
		want = ssmLayoutV2Root
	case want == "":
		return nil, errors.New("adapters.secrets: the ssm adapter has no root: set secrets: {source: ssm, root: /sluis/<instance>}")
	}
	out := port.Settings{"root": want}
	for k, v := range settings {
		out[k] = v
	}
	return out, nil
}

// ssmKey is the ssm Secrets adapter's settings with its key: the serve
// document's `secrets.kmsKeyId`, which the adapter may name again as `kmsKeyId`;
// naming another is refused. Unset leaves the settings as they are.
func (c Config) ssmKey(settings port.Settings) (port.Settings, error) {
	want := c.SecretsKMSKey
	if want == "" {
		return settings, nil
	}
	named, _ := settings["kmsKeyId"].(string)
	if named != "" && named != want {
		return nil, fmt.Errorf("adapters.secrets: the ssm adapter's kmsKeyId %q is not secrets.kmsKeyId %q: an installation has one key", named, want)
	}
	out := port.Settings{"kmsKeyId": want}
	for k, v := range settings {
		out[k] = v
	}
	out["kmsKeyId"] = want
	return out, nil
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
	settings := c.secrets.Settings
	if c.secrets.Adapter == "ssm" {
		var err error
		if settings, err = c.ssmRoot(settings); err != nil {
			return nil, err
		}
		if settings, err = c.ssmKey(settings); err != nil {
			return nil, err
		}
	}
	built, err := d.Factory(ctx, settings)
	if err != nil {
		return nil, fmt.Errorf("adapters.secrets: %w", err)
	}
	secrets, ok := built.(port.Secrets)
	if !ok {
		return nil, fmt.Errorf("adapters.secrets: %q built a %T, which is not a port.Secrets", c.secrets.Adapter, built)
	}
	return c.overLayout(ctx, secrets)
}

// v4Holder carries the v4 stores out of [Config.secretsOf], which runs on a
// copy of the Config.
type v4Holder struct{ stores *secretstore.Stores }

// overLayout puts the Secrets port over layout v4 when the document says so
// (`secrets.layout: transition | v4`, ssm source): callers keep their paths and
// the port maps them (internal/secretstore). On v3 it is the adapter itself.
func (c Config) overLayout(ctx context.Context, v3 port.Secrets) (port.Secrets, error) {
	layout, err := secretstore.ParseLayout(c.SecretsLayout)
	if err != nil {
		return nil, err
	}
	if layout == secretstore.LayoutV3 {
		return v3, nil
	}
	if c.secrets.Adapter != "ssm" {
		return nil, fmt.Errorf("secrets.layout: %s needs the ssm secrets adapter, not %q", layout, c.secrets.Adapter)
	}
	open := c.OpenState
	if open == nil {
		open = openSSMState
	}
	stores, err := secretstore.Open(ctx, &config.Secrets{
		Source: "ssm", Root: c.SecretsRoot, Region: c.SecretsRegion, Endpoint: c.SecretsEndpoint,
		KMSKeyID: c.SecretsKMSKey, Layout: string(layout),
	}, open)
	if err != nil {
		return nil, err
	}
	if c.v4 != nil {
		c.v4.stores = stores
	}
	return secretstore.NewSecrets(stores, v3, c.SecretsGrace), nil
}

// decodeStrict reads a settings object into v, refusing a key v lacks.
func decodeStrict(s port.Settings, v any) error { return s.Decode(v) }
