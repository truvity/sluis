// Package app assembles the backup module as a process of its own
// (docs/decisions/0071): the storage it reads an installation from, the archive
// bucket and key it writes to, the audit trail, and the job that runs under the
// backup lease (internal/backup/job). It has no listener and no inbound API but
// the module-call methods of internal/backup/rpc.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/backup/export"
	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/backup/rpc"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/s3blob"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/signer"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/internal/version"
	"github.com/truvity/sluis/storage/keys"
)

// Config is what the module is made of, built from its document.
type Config struct {
	doc      *config.SluisBackup
	stores   store.Config
	audit    audit.Config
	keys     keys.Config
	logLevel slog.Level

	// Test seams: an archive and a key backend in place of the S3 bucket and
	// KMS the document names.
	archive    port.Blob
	keyBackend keys.Backend
}

// LogLevel is the level the process should log at.
func (c Config) LogLevel() slog.Level { return c.logLevel }

// Load reads the module's document and builds the settings. Opening connects to
// nothing.
func Load(file string) (Config, error) {
	doc, err := config.LoadBackup(file)
	if err != nil {
		return Config{}, err
	}
	return FromDocument(doc)
}

// FromDocument builds the settings from the document already read.
func FromDocument(doc *config.SluisBackup) (Config, error) {
	if !backup.ValidName(doc.Name()) {
		return Config{}, fmt.Errorf("instance: %q is not one path segment of letters, digits, '.', '-' and '_' (it is the archive path)", doc.Name())
	}
	kc, err := doc.ArchiveKeys()
	if err != nil {
		return Config{}, err
	}
	serve := doc.Serve()
	stores, err := store.FromServe(serve)
	if err != nil {
		return Config{}, err
	}
	// The whole-estate view: the router over every module's table, the
	// secrets of layout v5.
	if stores.SecretsLayout != config.SecretsLayoutV5 || len(stores.DynamoDB.Tables) == 0 {
		return Config{}, errors.New("the backup module reads layout v5 (ADR 0072): set secrets.layout: v5 and ports.dynamodb.tables")
	}
	if stores.Secrets, err = secrets.Open(context.Background(), serve); err != nil {
		return Config{}, err
	}
	c := Config{doc: doc, stores: stores, keys: kc, audit: audit.Config{Version: version.String()}}
	// The audit instance is the pod, which is its hostname in a cluster.
	c.audit.Instance, _ = os.Hostname()
	if a := doc.Audit; a != nil {
		c.audit.Writer = a.Writer
		c.audit.TokenFile = a.TokenFile
	}
	level := "info"
	if doc.Log != nil && doc.Log.Level != "" {
		level = doc.Log.Level
	}
	if err = c.logLevel.UnmarshalText([]byte(level)); err != nil {
		return Config{}, fmt.Errorf("log.level: %w", err)
	}
	return c, nil
}

// Creator names this build in each manifest.
func Creator() string { return "sluis-backup " + version.String() }

// App is the assembled module.
type App struct {
	job    *job.Job
	rpc    *modcall.Server
	log    *slog.Logger
	stores *store.Stores
	trail  *audit.Trail
}

// New assembles the module: the ports, the archive, the key, the audit trail.
func New(ctx context.Context, cfg Config, log *slog.Logger) (*App, error) {
	if log == nil {
		log = slog.Default()
	}
	stores, err := store.Open(ctx, cfg.stores, log)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*App, error) {
		stores.Close()
		return nil, err
	}
	if stores.V5 == nil || stores.Tables == nil {
		return fail(errors.New("the backup module reads layout v5: secrets.layout: v5 on the ssm source, and ports.dynamodb.tables"))
	}
	src := export.Source{Secrets: stores.V5, Blob: stores.Ports.Blob}
	var ok bool
	if src.State, ok = stores.Ports.State.(port.StateExporter); !ok {
		return fail(errors.New("the State adapter cannot export its records"))
	}
	if src.Index, ok = stores.Ports.Index.(port.IndexExporter); !ok {
		return fail(errors.New("the Index adapter cannot export its sets"))
	}
	if src.Blob == nil {
		return fail(errors.New("ports.blob: the module backs up the reports kept in the blob store, so it needs the installation's"))
	}

	archive := cfg.archive
	if archive == nil {
		t := cfg.doc.Backup.Target
		archive, err = s3blob.New(ctx, s3blob.Config{Bucket: t.Bucket, Prefix: t.Prefix, Region: t.Region, KMSKey: t.KMSKey,
			Endpoint: t.Endpoint, PathStyle: t.PathStyle})
		if err != nil {
			return fail(fmt.Errorf("backup.target: %w", err))
		}
	}
	key, err := openKey(ctx, cfg)
	if err != nil {
		return fail(err)
	}

	cfg.audit.Log = log
	if cfg.audit, err = audit.FromPlan(cfg.audit, stores.Plan); err != nil {
		return fail(err)
	}
	trail, err := audit.Open(ctx, cfg.audit)
	if err != nil {
		return fail(err)
	}
	keep, maxAge := cfg.doc.Backup.Retention.Rule()
	state := stores.Ports.State
	jc := job.Config{
		Installation: cfg.doc.Name(), Creator: Creator(),
		State: state, Source: src, Archive: archive, Key: key,
		Keep: keep, MaxAge: maxAge, Audit: trail, Log: log,
	}
	// The backup module's own table holds the flag the restore sets; the gate
	// reads that table, not the router's guess at where a module-less key lives.
	if g := stores.MaintenanceOf(port.ModuleBackup); g != nil {
		jc.Maintenance = g
	}
	j, err := job.New(jc)
	if err != nil {
		_ = trail.Close()
		return fail(err)
	}
	a := &App{job: j, log: log, stores: stores, trail: trail, rpc: modcall.NewServer(rpc.Module)}
	rpc.Register(a.rpc, a)
	return a, nil
}

// openKey opens the archive key (purpose archive) of the configured adapter.
func openKey(ctx context.Context, cfg Config) (*keys.Key, error) {
	backend := cfg.keyBackend
	if backend == nil {
		if cfg.keys.Adapter != "kms" {
			return nil, fmt.Errorf("keys.adapter %q: the backup module opens kms keys here", cfg.keys.Adapter)
		}
		var err error
		if backend, err = signer.OpenKMS(ctx, cfg.doc.Backup.Region); err != nil {
			return nil, err
		}
	}
	set, err := keys.Open(cfg.keys, keys.Options{Backend: backend, Instance: cfg.doc.Name()})
	if err != nil {
		return nil, err
	}
	return set.For(keys.Archive)
}

// RPC is the module's server: what a transport of internal/modcall serves.
func (a *App) RPC() *modcall.Server { return a.rpc }

// Run runs one backup (or continues one), see [job.Job.Run].
func (a *App) Run(ctx context.Context, req job.Request) (job.Result, error) {
	return a.job.Run(ctx, req)
}

// Status is the latest records.
func (a *App) Status(ctx context.Context) (job.Status, error) { return a.job.Status(ctx) }

// List is the backups in the archive, newest first.
func (a *App) List(ctx context.Context) ([]job.Info, error) { return a.job.List(ctx) }

// Prune applies the retention rule.
func (a *App) Prune(ctx context.Context, actor audit.Actor, dryRun bool) (job.Pass, string, error) {
	return a.job.Prune(ctx, actor, dryRun)
}

// Flush delivers the audit records still queued: a Lambda function calls it
// before an invocation returns, since the process is frozen after it.
func (a *App) Flush(ctx context.Context) error { return a.trail.Flush(ctx) }

// Close closes the audit emitter, which delivers what its queue holds within its
// timeout and drops the rest, and the ports.
func (a *App) Close() error {
	err := a.trail.Close()
	a.stores.Close()
	return err
}
