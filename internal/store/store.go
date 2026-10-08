// Package store builds the storage ports once, from configuration, for the
// apps to be handed: the one place that knows which adapter backs them.
//
// Everything else names the interfaces of internal/port. The adapter is
// chosen by the `ports.adapter` key of the configuration file: `legacy`
// (the default, today's ConfigMaps, Secrets and Valkey, unchanged), `memory`
// or `dynamodb`.
package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/port"
	dynamoport "github.com/truvity/sluis/internal/port/dynamodb"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/port/observe"
	"github.com/truvity/sluis/internal/port/openbao"
	"github.com/truvity/sluis/internal/port/s3blob"
	"github.com/truvity/sluis/internal/port/secretsexport"
	_ "github.com/truvity/sluis/internal/port/ssm" // registers the ssm secrets adapter
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/secretstore"
)

// The adapters `ports.adapter` names.
const (
	AdapterLegacy = "legacy"
	AdapterMemory = "memory"
	// AdapterDynamoDB keeps State, the session index and the trigger in one
	// DynamoDB table.
	AdapterDynamoDB = "dynamodb"
)

// BlobS3 is the adapter `ports.blob.adapter` names. It replaces one port and composes
// with any `ports.adapter`.
const BlobS3 = "s3"

// The adapters `ports.export.adapter` names. The Export port has none unless
// a deployment names one: nothing is copied out of the service by default.
const (
	ExportOpenBao = "openbao"
	ExportMemory  = "memory"
)

// KubeNeed says how much a process needs the namespace's objects.
type KubeNeed int

// How much a process needs the cluster.
const (
	// KubeNone opens nothing: a hub with `store: memory`.
	KubeNone KubeNeed = iota
	// KubeOptional tries, and goes on without when this is not a cluster: an
	// issuer proving recovery against one it may not be running in.
	KubeOptional
	// KubeRequired stops the start when this is not a cluster.
	KubeRequired
)

// Config is what Open needs.
type Config struct {
	Adapter string
	// Release prefixes the objects' names and the Valkey keys.
	Release string
	Kube    KubeNeed
	// Blob replaces the port of the same name; nil keeps what Adapter brings.
	Blob *config.PortsBlob
	// DynamoDB is the table of the `dynamodb` adapter.
	DynamoDB dynamoport.Config
	// Export is the Export port's adapter; nil is none.
	Export *config.PortsExport

	// Secrets delivers the secrets the document names (the Valkey password):
	// the composition root sets it after FromServe. Nil delivers none.
	Secrets secrets.Source
	// SecretsRoot is the serve document's `secrets.root` when its source is
	// ssm: the root the `ssm` Secrets adapter takes.
	SecretsRoot string
	// SecretsKMSKey is the serve document's `secrets.kmsKeyId` when its source
	// is ssm: the key the `ssm` Secrets adapter encrypts what it writes with.
	SecretsKMSKey string
	// Secrets layout (ADR 0041): the serve document's `secrets.layout`,
	// `.region`, `.endpoint` and `.grace` when its source is ssm. The layout
	// is v3 (the default) unless it is "transition" or "v4"; those put the
	// Secrets port over layout v4 (internal/secretstore).
	SecretsLayout   string
	SecretsRegion   string
	SecretsEndpoint string
	SecretsGrace    time.Duration
	// OpenState opens the backend of the v4 stores (storage/state/ssm.Open).
	// Nil is that backend.
	OpenState secretstore.Opener

	// v4 is where secretsOf leaves the v4 stores it built, for [Stores.V4].
	v4 *v4Holder
	// Converted is a document converted from v1: an `ssm` secrets adapter
	// that names no root keeps v1's layout, /sluis.
	Converted bool

	// secrets is the secrets adapter the plan chose, nil when nothing chose one.
	secrets *port.Choice

	// sel is what the file says about adapters beyond `ports.adapter`.
	sel selection

	// k8sConfig is what the Kubernetes build adds: the Valkey (store_k8s.go).
	// The Lambda build has none (store_lambda.go).
	k8sConfig
}

// validatePorts refuses a Blob the file names but this build has no
// adapter for, or names without its settings. The schema says the same; this is
// the check for a Config that was not read from a file.
func (c Config) validatePorts() error {
	if b := c.Blob; b != nil {
		if b.Adapter != BlobS3 {
			return fmt.Errorf("ports.blob.adapter: %q is not %q", b.Adapter, BlobS3)
		}
		if b.S3 == nil || b.S3.Bucket == "" {
			return errors.New("ports.blob.s3.bucket: required with ports.blob.adapter: s3")
		}
	}
	return nil
}

// validateExport refuses an Export the file names but this build has no
// adapter for, or names without its settings. The schema says the same; this
// is the check for a Config that was not read from a file.
func (c Config) validateExport() error {
	e := c.Export
	if e == nil {
		return nil
	}
	switch e.Adapter {
	case ExportMemory:
		return nil
	case ExportOpenBao:
		if e.OpenBao == nil || e.OpenBao.Address == "" || e.OpenBao.Auth == nil {
			return errors.New("ports.export.openbao: address and auth are required with ports.export.adapter: openbao")
		}
		return nil
	default:
		return fmt.Errorf("ports.export.adapter: %q is %q or %q", e.Adapter, ExportOpenBao, ExportMemory)
	}
}

// exportOf builds the Export port. It connects to nothing: an OpenBao that is
// down at start must not stop the service, since a copy is never a dependency.
func (c Config) exportOf() (port.Export, error) {
	if err := c.validateExport(); err != nil || c.Export == nil {
		return nil, err
	}
	if c.Export.Adapter == ExportMemory {
		return memory.NewExport(), nil
	}
	o := c.Export.OpenBao
	return openbao.New(openbao.Config{
		Address: o.Address, CAFile: o.CAFile, Mount: o.Mount, Namespace: o.Namespace,
		Auth: openbao.Auth{Method: o.Auth.Method, Mount: o.Auth.Mount, Role: o.Auth.Role, TokenFile: o.Auth.TokenFile},
	})
}

// compose replaces the Blob the base adapter brought with the one configured. It runs before the set is observed, so the replacements are
// timed and counted like every other port.
func (c Config) compose(ctx context.Context, set port.Set, log *slog.Logger) (port.Set, error) {
	if err := c.validatePorts(); err != nil {
		return port.Set{}, err
	}
	if b := c.Blob; b != nil {
		blob, err := s3blob.New(ctx, s3blob.Config{
			Bucket: b.S3.Bucket, Prefix: b.S3.Prefix, Region: b.S3.Region, KMSKey: b.S3.KMSKey,
			Endpoint: b.S3.Endpoint, PathStyle: b.S3.PathStyle,
		})
		if err != nil {
			return port.Set{}, fmt.Errorf("ports.blob: %w", err)
		}
		set.Blob = blob
		log.InfoContext(ctx, "blobs are kept in S3", "adapter", BlobS3, "bucket", b.S3.Bucket, "prefix", b.S3.Prefix)
	}
	secrets, err := c.secretsOf(ctx)
	if err != nil {
		return port.Set{}, err
	}
	if secrets != nil {
		set.Secrets = secrets
		log.InfoContext(ctx, "secrets are kept by the secrets adapter", "adapter", c.secrets.Adapter)
	}
	exp, err := c.exportOf()
	if err != nil {
		return port.Set{}, fmt.Errorf("ports.export: %w", err)
	}
	if exp == nil && secrets != nil {
		// No `ports.export`: the copies go through the Secrets port, to
		// `export/<path>` (SSM `/sluis/export/<path>`).
		exp = secretsexport.New(secrets)
		log.InfoContext(ctx, "exports are written through the secrets adapter", "adapter", c.secrets.Adapter, "prefix", port.ExportPrefix)
	}
	set.Export = exp
	return set, nil
}

// FromServe reads the configuration of `sluis serve`.
func FromServe(f *config.Serve) (Config, error) {
	c := Config{
		Adapter: adapterOf(f.Ports),
		Release: orDefault(f.Release, "sluis"),
		Blob:    blobOf(f.Ports),

		DynamoDB: dynamoOf(f.Ports),
		Export:   exportConfigOf(f.Ports),
		sel:      selectionOf(f.Ports, f.Platform, f.Preset, f.Adapters, f.Audit != nil && f.Audit.Writer != "", f.SigningKey),

		Converted: f.Converted(),
	}
	if s := f.Secrets; s != nil && s.Source == "ssm" {
		c.SecretsRoot = s.Root
		c.SecretsKMSKey = s.KMSKeyID
		c.SecretsLayout = s.Layout
		c.SecretsRegion = s.Region
		c.SecretsEndpoint = s.Endpoint
		if s.Grace != nil {
			c.SecretsGrace = s.Grace.D()
		}
	}
	var err error
	if c.Adapter == AdapterDynamoDB && f.Valkey != nil && f.Valkey.Address != "" {
		return Config{}, fmt.Errorf("ports.adapter: %s holds the shared state, so it cannot be combined with valkey.address", c.Adapter)
	}
	if err = c.fromServeK8s(f); err != nil {
		return Config{}, err
	}
	switch {
	case f.Store == "kubernetes":
		c.Kube = KubeRequired
	case f.InCluster && f.Recovery != nil && f.Recovery.Enabled != nil && *f.Recovery.Enabled:
		c.Kube = KubeOptional
	}
	if c.Adapter == AdapterMemory {
		switch {
		case f.Store == "kubernetes":
			return Config{}, errors.New("ports.adapter: memory keeps nothing, so it cannot back store: kubernetes")
		case f.Valkey != nil && f.Valkey.Address != "":
			return Config{}, errors.New("ports.adapter: memory keeps nothing, so it cannot be combined with valkey.address")
		}
		c.Kube = KubeNone
	}
	return c, nil
}

// FromRoster reads what the two controllers share. A controller's reports
// and links are objects in its namespace, so it requires the cluster.
func FromRoster(f *config.Roster) Config {
	c := Config{
		Adapter: adapterOf(f.Ports), Release: orDefault(f.Release, "sluis"), Kube: KubeRequired,
		Blob: blobOf(f.Ports), DynamoDB: dynamoOf(f.Ports),
		sel:       selectionOf(f.Ports, f.Platform, f.Preset, f.Adapters, f.Audit != nil && f.Audit.Writer != "", nil),
		Converted: f.Converted(),
	}
	if c.Adapter == AdapterMemory {
		c.Kube = KubeNone
	}
	return c
}

// dynamoOf reads the table's settings; dynamoport.Open refuses an incomplete
// set, naming it, when the adapter is the chosen one.
func dynamoOf(p *config.Ports) dynamoport.Config {
	if p == nil || p.DynamoDB == nil {
		return dynamoport.Config{}
	}
	d := p.DynamoDB
	return dynamoport.Config{Table: d.Table, Region: d.Region, Endpoint: d.Endpoint, Create: d.Create}
}

func adapterOf(p *config.Ports) string {
	if p == nil || p.Adapter == "" {
		return AdapterLegacy
	}
	return p.Adapter
}

func blobOf(p *config.Ports) *config.PortsBlob {
	if p == nil {
		return nil
	}
	return p.Blob
}

func exportConfigOf(p *config.Ports) *config.PortsExport {
	if p == nil {
		return nil
	}
	return p.Export
}

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// Stores is the ports, built once.
type Stores struct {
	// Ports is every port, over the chosen adapter.
	Ports port.Set
	// Backend is today's storage as it was opened: what the `legacy` adapter
	// is made of, and, with any adapter, the cluster's own objects that stay the
	// cluster's (the token review, the declared OAuth client). The domain stores
	// (a connected workspace and its credential, an organisation, a person's
	// link, the Slack records) are on Ports for every adapter but `legacy`
	// (internal/portstore), and in the ConfigMaps and Secrets this reaches for
	// `legacy`. It is nil with the memory adapter, which has none.
	Backend *Backend
	// Adapter is the state adapter's name.
	Adapter string
	// Plan is the resolved adapter per concern, for the packages that wire a
	// concern this package does not.
	Plan port.Table
	// Secrets delivers the secrets the document names, by name: what the
	// composition root put in Config.Secrets. Nil delivers none.
	Secrets secrets.Source
	// V4 is the installation's secrets on layout v4: Internal and External
	// over one state store. Nil on layout v3, the default.
	V4 *secretstore.Stores
	// Shared is true when the state is one every replica sees: a Valkey.
	Shared bool
	// Usable is whether the State, Index and snapshot Blob ports work at all:
	// the memory adapter, and the legacy one with a Valkey. Without them a
	// caller keeps its own process-local store, as it always has.
	Usable bool

	pinger any
	close  func()
}

// Name says where state lives, for a log line and the console's page.
func (s *Stores) Name() string {
	switch {
	case s.Adapter == AdapterDynamoDB:
		return "dynamodb"
	case s.Shared:
		return "valkey"
	default:
		return "memory"
	}
}

// LeaseState is the State the controllers take their tick leases from, and
// whether it is shared by every replica. A lease is only exclusive across
// processes when the State is: the legacy adapter keeps leases in Valkey, so
// without one a controller holds its leases in its own memory and a second
// replica would not be kept off (docs/explanation/ports.md, "The legacy adapter").
func (s *Stores) LeaseState() (state port.State, shared bool) {
	if s.Shared {
		return s.Ports.State, true
	}
	if s.Adapter == AdapterMemory {
		return s.Ports.State, false
	}
	return memory.New().Set().State, false
}

// ErrLocalLease is the refusal of a one-shot tick whose lease would not exclude
// the running controller.
var ErrLocalLease = errors.New("the tick leases are held in this process only, so a running controller " +
	"would not be kept off the same target and both could act on it (duplicate invites or removals): " +
	"this is safe once a shared State exists (docs/decisions/0029, B3). Scale the controller to 0 and " +
	"pass --unsafe-local-lease to run the tick anyway")

// RequireSharedLease refuses a one-shot tick when its lease State is not
// shared with the controller's, unless the operator opted in.
func RequireSharedLease(shared, unsafeLocal bool) error {
	if shared || unsafeLocal {
		return nil
	}
	return ErrLocalLease
}

// Readiness is what readiness should ask: the Valkey, or nothing.
func (s *Stores) Readiness() any { return s.pinger }

// Close releases what Open opened.
func (s *Stores) Close() {
	if s != nil && s.close != nil {
		s.close()
	}
}

// Open builds the ports for the adapter the configuration names.
func Open(ctx context.Context, cfg Config, log *slog.Logger) (*Stores, error) {
	var plan port.Table
	switch cfg.Adapter {
	case AdapterLegacy, AdapterMemory, AdapterDynamoDB:
		var err error
		if cfg, plan, err = cfg.plan(ctx, log); err != nil {
			return nil, err
		}
	}
	cfg.v4 = &v4Holder{}
	st, err := open(ctx, cfg, log)
	if err != nil {
		return st, err
	}
	st.V4 = cfg.v4.stores
	st.Plan = plan
	st.Secrets = cfg.Secrets
	if err = st.applyTrigger(ctx, log); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

// applyTrigger replaces the Trigger the state adapter brought with the one the
// plan names, when that is an adapter of its own: `invoke`, which starts a
// controller function. The state adapters' triggers (the table's, the in-process
// one) are theirs and stay.
func (s *Stores) applyTrigger(ctx context.Context, log *slog.Logger) error {
	choice, ok := s.Plan[port.ConcernTrigger]
	if !ok || choice.Adapter == "" || choice.Adapter == s.Adapter || choice.Adapter == "legacy" {
		return nil
	}
	d, ok := port.Default.Lookup(port.ConcernTrigger, choice.Adapter)
	if !ok || d.Factory == nil {
		return fmt.Errorf("adapters.trigger: %q is not built into this binary", choice.Adapter)
	}
	built, err := d.Factory(ctx, choice.Settings)
	if err != nil {
		return fmt.Errorf("adapters.trigger: %s: %w", choice.Adapter, err)
	}
	trigger, ok := built.(port.Trigger)
	if !ok {
		return fmt.Errorf("adapters.trigger: %s is not a trigger", choice.Adapter)
	}
	s.Ports.Trigger = trigger
	log.InfoContext(ctx, "run-now notifications go through the trigger adapter", "adapter", choice.Adapter)
	return nil
}

func open(ctx context.Context, cfg Config, log *slog.Logger) (*Stores, error) {
	switch cfg.Adapter {
	case AdapterMemory:
		log.WarnContext(ctx, "the storage ports are in memory: a restart loses every login in progress, "+
			"snapshot and report", "adapter", AdapterMemory)
		set, err := cfg.compose(ctx, memory.New().Set(), log)
		if err != nil {
			return nil, err
		}
		return &Stores{Ports: observe.Set(set), Adapter: AdapterMemory, Usable: true}, nil
	case AdapterLegacy:
		// Kubernetes' own: the namespace's objects and Valkey. The Lambda build
		// has none of them (store_lambda.go).
		return openK8s(ctx, cfg, log)
	case AdapterDynamoDB:
		return openDynamoDB(ctx, cfg, log)
	}
	return nil, fmt.Errorf("ports.adapter: %q is none of %q, %q, %q", cfg.Adapter, AdapterLegacy, AdapterMemory, AdapterDynamoDB)
}

// openDynamoDB holds State, the session index and the trigger in the table.
// Blob and Identity are the Kubernetes build's legacy adapter over the
// namespace's objects, unless `ports.blob` names an adapter of its own; the
// Lambda build has no such adapter, so it requires one.
func openDynamoDB(ctx context.Context, cfg Config, log *slog.Logger) (*Stores, error) {
	st := &Stores{Adapter: AdapterDynamoDB, Shared: true, Usable: true}
	rest, backend, err := restPorts(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	st.Backend = backend
	table, err := dynamoport.Open(ctx, cfg.DynamoDB)
	if err != nil {
		return nil, fmt.Errorf("ports.dynamodb: %w", err)
	}
	st.pinger = table
	st.close = table.Close
	set := table.Set()
	set.Blob, set.Identity = rest.Blob, rest.Identity
	if set, err = cfg.compose(ctx, set, log); err != nil {
		st.Close()
		return nil, err
	}
	st.Ports = observe.Set(set)
	log.InfoContext(ctx, "keeping state in DynamoDB", "adapter", AdapterDynamoDB,
		"table", cfg.DynamoDB.Table, "region", cfg.DynamoDB.Region, "create", cfg.DynamoDB.Create)
	return st, nil
}
