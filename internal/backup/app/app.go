// Package app assembles the backup module as a process of its own
// (docs/decisions/0071): the storage it reads an installation from, the archive
// bucket and the age recipients it writes to (or, in the restore role, the age
// identity it reads with), the audit trail, and the job that runs under the
// backup lease (internal/backup/job). It has no listener and no inbound API but
// the module-call methods of internal/backup/rpc.
package app

import (
	"bytes"
	"context"

	"errors"
	"fmt"
	"log/slog"
	"os"

	"filippo.io/age"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/backup/attest"
	"github.com/truvity/sluis/internal/backup/export"
	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/backup/restore"
	"github.com/truvity/sluis/internal/backup/restorejob"
	"github.com/truvity/sluis/internal/backup/rpc"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/port"
	dynamoport "github.com/truvity/sluis/internal/port/dynamodb"
	"github.com/truvity/sluis/internal/port/s3blob"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/internal/version"
	"github.com/truvity/sluis/storage/state"
)

// Config is what the module is made of, built from its document.
type Config struct {
	doc      *config.SluisBackup
	stores   store.Config
	audit    audit.Config
	logLevel slog.Level
	// role is the function the zip acts as: the document's, or the command's.
	role string

	// Test seams: an archive in place of the S3 bucket the document names, and
	// the restore identity in place of the secret.
	archive  port.Blob
	identity backup.Identities
	attestor backup.Attestor
}

// LogLevel is the level the process should log at.
func (c Config) LogLevel() slog.Level { return c.logLevel }

// Role is [config.BackupRoleBackup] or [config.BackupRoleRestore].
func (c Config) Role() string { return c.role }

// AsRole is the configuration acting as role: the command line's `sluis restore`
// is the restore role whatever the document says.
func (c Config) AsRole(role string) Config {
	c.role = role
	return c
}

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
	serve := doc.Serve()
	stores, err := store.FromServe(serve)
	if err != nil {
		return Config{}, err
	}
	// The module notifies no controller: the preset's `invoke` trigger has no
	// function to invoke here and is not built.
	stores.NoTrigger = true
	// The whole-estate view: the router over every module's table, the
	// secrets of layout v5.
	if stores.SecretsLayout != config.SecretsLayoutV5 || len(stores.DynamoDB.Tables) == 0 {
		return Config{}, errors.New("the backup module reads layout v5 (ADR 0072): set secrets.layout: v5 and ports.dynamodb.tables")
	}
	if stores.Secrets, err = secrets.Open(context.Background(), serve); err != nil {
		return Config{}, err
	}
	c := Config{doc: doc, stores: stores, role: doc.RoleOf(), audit: audit.Config{Version: version.String()}}
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

// AuditConfig is the audit configuration the module runs with, from the adapter
// table its document resolves to. It opens nothing; a document whose audit
// adapter the build does not serve, or whose settings it does not decode, is
// refused here as it is at start.
func (c Config) AuditConfig(ctx context.Context, log *slog.Logger) (audit.Config, error) {
	plan, err := c.stores.Plan(ctx, log)
	if err != nil {
		return audit.Config{}, err
	}
	return audit.FromPlan(c.audit, plan)
}

// Creator names this build in each manifest.
func Creator() string { return "sluis-backup " + version.String() }

// App is the assembled module.
type App struct {
	role     string
	instance string
	archive  port.Blob
	job      *job.Job
	restore  *restorejob.Job
	rpc      *modcall.Server
	log      *slog.Logger
	stores   *store.Stores
	trail    *audit.Trail
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
	archive := cfg.archive
	if archive == nil {
		t := cfg.doc.Backup.Target
		archive, err = s3blob.New(ctx, s3blob.Config{Bucket: t.Bucket, Prefix: t.Prefix, Region: t.Region, KMSKey: t.KMSKey,
			Endpoint: t.Endpoint, PathStyle: t.PathStyle, Checksum: t.Checksum})
		if err != nil {
			return fail(fmt.Errorf("backup.target: %w", err))
		}
	}
	if dep := cfg.doc.Deprecated(); len(dep) > 0 {
		log.WarnContext(ctx, "the backup document sets keys that backup format 2 ignores; remove them (backups are age-encrypted to backup.recipients)",
			slog.Any("keys", dep))
	}

	cfg.audit.Log = log
	if cfg.audit, err = audit.FromPlan(cfg.audit, stores.Plan); err != nil {
		return fail(err)
	}
	trail, err := audit.Open(ctx, cfg.audit)
	if err != nil {
		return fail(err)
	}
	a := &App{role: cfg.role, instance: cfg.doc.Name(), archive: archive, log: log, stores: stores, trail: trail}
	if cfg.role == config.BackupRoleRestore {
		if err := a.openRestore(cfg, archive); err != nil {
			_ = trail.Close()
			return fail(err)
		}
		return a, nil
	}
	if err := a.openBackup(cfg, archive); err != nil {
		_ = trail.Close()
		return fail(err)
	}
	return a, nil
}

// openBackup assembles the scheduled export.
func (a *App) openBackup(cfg Config, archive port.Blob) error {
	stores := a.stores
	src := export.Source{Secrets: stores.V5, Blob: stores.Ports.Blob}
	var ok bool
	if src.State, ok = stores.Ports.State.(port.StateExporter); !ok {
		return errors.New("the State adapter cannot export its records")
	}
	if src.Index, ok = stores.Ports.Index.(port.IndexExporter); !ok {
		return errors.New("the Index adapter cannot export its sets")
	}
	if src.Blob == nil {
		return errors.New("ports.blob: the module backs up the reports kept in the blob store, so it needs the installation's")
	}

	keep, maxAge := cfg.doc.Backup.Retention.Rule()
	state := stores.Ports.State
	jc := job.Config{
		Installation: cfg.doc.Name(), Creator: Creator(),
		State: state, Source: src, Archive: archive, Recipients: cfg.doc.Backup.Recipients,
		Keep: keep, MaxAge: maxAge, Audit: a.trail, Log: a.log,
	}
	// The backup module's own table holds the flag the restore sets; the gate
	// reads that table, not the router's guess at where a module-less key lives.
	jc.Maintenance = backupGate(func(m port.Module) (port.State, bool) {
		t, ok := stores.Tables.Store(m)
		if !ok {
			return nil, false
		}
		return t, true
	})
	j, err := job.New(jc)
	if err != nil {
		return err
	}
	a.job, a.rpc = j, modcall.NewServer(rpc.Module)
	rpc.Register(a.rpc, a)
	return nil
}

// backupGate is the gate over the maintenance flag in the backup module's own
// table, which table returns. This process hosts no module of its own in the
// ports (store.Stores.MaintenanceOf falls back to Ports.State, which routes a
// module-less key to the oidc table), so the table is named here. Nil, which
// never refuses, when there is no such table.
func backupGate(table func(port.Module) (port.State, bool)) *maintenance.Gate {
	st, ok := table(port.ModuleBackup)
	if !ok {
		return nil
	}
	return maintenance.New(st)
}

// openRestore assembles the restore function: the same binary, selected by
// `backup.role` (or by `sluis restore`), whose role may write every module's
// table. It writes the maintenance flag in each table itself.
func (a *App) openRestore(cfg Config, archive port.Blob) error {
	stores := a.stores
	if stores.Ports.Blob == nil {
		return errors.New("ports.blob: the restore writes the reports kept in the blob store back, so it needs the installation's")
	}
	mods := stores.Tables.Modules()
	tables := func(m port.Module) (*dynamoport.Store, error) {
		t, ok := stores.Tables.Store(m)
		if !ok {
			return nil, fmt.Errorf("ports.dynamodb.tables: the restore needs the table of module %s", m)
		}
		return t, nil
	}
	for _, m := range port.Modules() {
		if _, err := tables(m); err != nil {
			return err
		}
	}
	identity := cfg.identity
	if identity == nil {
		identity = restoreIdentity(stores.V5, cfg.doc.IdentityRef())
	}
	// How a backup is proved genuine before it is decrypted: the audit trail's
	// record of it. Not configured, every restore is refused unless an
	// administrator passes allowUnverified.
	attestor := cfg.attestor
	if attestor == nil {
		attestor = backup.NoAttestor{}
		if v := cfg.doc.Backup.Verify; v != nil {
			q, err := attest.New(attest.Config{URL: v.Query, TokenFile: v.TokenFile, Profile: v.Profile, Observer: v.Observer, AnyObserver: v.AnyObserver})
			if err != nil {
				return fmt.Errorf("backup.verify: %w", err)
			}
			if v.AnyObserver {
				a.log.WarnContext(context.Background(),
					"backup.verify.anyObserver is set: any principal that may send audit records can vouch for a forged backup; set backup.verify.observer")
			}
			attestor = q
		} else {
			a.log.WarnContext(context.Background(), "backup.verify is not set: this restore refuses every backup unless it is started with allowUnverified")
		}
	}
	j, err := restorejob.New(restorejob.Config{
		Installation: cfg.doc.Name(), State: stores.Ports.State,
		Modules: mods,
		Flags:   func(m port.Module) port.State { t, _ := tables(m); return t },
		Target: restore.Target{
			Table:   func(m port.Module) restore.Table { t, _ := tables(m); return t },
			Secrets: stores.V5, Blob: stores.Ports.Blob,
		},
		Archive: archive, Identity: identity, Audit: a.trail, Log: a.log,
		Attestor: attestor, Recipients: cfg.doc.Backup.Recipients,
	})
	if err != nil {
		return err
	}
	a.restore, a.rpc = j, modcall.NewServer(rpc.RestoreModule)
	rpc.RegisterRestore(a.rpc, a)
	return nil
}

// restoreIdentity reads the age identity from the backup module's secret
// internal/backup/<ref> ({"value":"<base64>"}), each time a backup is opened,
// so a rotated identity is used without a restart.
func restoreIdentity(st *secretstore.StoresV5, ref string) backup.Identities {
	return func(ctx context.Context) ([]age.Identity, error) {
		raw, _, err := st.Backup().RestoreIdentity(ref).Get(ctx)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return nil, fmt.Errorf("the restore identity internal/backup/%s is not set", ref)
			}
			return nil, fmt.Errorf("read the restore identity internal/backup/%s: %w", ref, err)
		}
		return backup.ParseIdentities(string(raw))
	}
}

// RPC is the module's server: what a transport of internal/modcall serves.
func (a *App) RPC() *modcall.Server { return a.rpc }

// Run runs one backup (or continues one), see [job.Job.Run].
func (a *App) Run(ctx context.Context, req job.Request) (job.Result, error) {
	if a.job == nil {
		return job.Result{}, errNotBackup
	}
	return a.job.Run(ctx, req)
}

// Role is the function the app acts as: backup or restore.
func (a *App) Role() string { return a.role }

// Restore starts a restore or continues the unfinished one, see
// [restorejob.Job.Start]. Only the restore role has one.
func (a *App) Restore(ctx context.Context, req restorejob.Request) (restorejob.Result, error) {
	if a.restore == nil {
		return restorejob.Result{}, errNotRestore
	}
	return a.restore.Start(ctx, req)
}

// PreviewRestore reports what restoring a backup would do and writes nothing.
// The backup is verified as a restore verifies it.
func (a *App) PreviewRestore(ctx context.Context, backupID string) (*restore.Report, error) {
	return a.PreviewRestoreWith(ctx, backupID, false)
}

// PreviewRestoreWith is [App.PreviewRestore] that, with allowUnverified,
// previews a backup the sealed audit trail does not vouch for.
func (a *App) PreviewRestoreWith(ctx context.Context, backupID string, allowUnverified bool) (*restore.Report, error) {
	if a.restore == nil {
		return nil, errNotRestore
	}
	return a.restore.PreviewWith(ctx, backupID, allowUnverified)
}

// RestoreStatus is the latest restores and the maintenance flags.
func (a *App) RestoreStatus(ctx context.Context) (restorejob.Status, error) {
	if a.restore == nil {
		return restorejob.Status{}, errNotRestore
	}
	return a.restore.Status(ctx)
}

var errNotRestore = errors.New("this is the backup role: the restore function is the same zip with backup.role: restore (or `sluis restore`)")

var errNotBackup = errors.New("this is the restore role: it runs no backup")

// Status is the latest records.
func (a *App) Status(ctx context.Context) (job.Status, error) {
	if a.job == nil {
		return job.Status{}, errNotBackup
	}
	return a.job.Status(ctx)
}

// List is the backups in the archive, newest first.
func (a *App) List(ctx context.Context) ([]job.Info, error) {
	if a.job == nil {
		return nil, errNotBackup
	}
	return a.job.List(ctx)
}

// Prune applies the retention rule.
func (a *App) Prune(ctx context.Context, actor audit.Actor, dryRun bool) (job.Pass, string, error) {
	if a.job == nil {
		return job.Pass{}, "", errNotBackup
	}
	return a.job.Prune(ctx, actor, dryRun)
}

// ProvenanceNotChecked is what `sluis backup verify` says of where a backup
// came from.
const ProvenanceNotChecked = "not checked: the audit trail is not consulted; a restore does"

// Verification is what `sluis backup verify` found.
type Verification struct {
	ID string `json:"id"`
	// Object is the encrypted object as its sidecar describes it; its size
	// and SHA-256 matched.
	Object backup.ObjectInfo `json:"object"`
	// Decrypted is whether an identity was given and the archive decrypted,
	// and every member matched the manifest.
	Decrypted bool `json:"decrypted"`
	// Provenance says what this does not check: that the backup is the one the
	// backup function made. A restore checks that against the audit trail.
	Provenance string `json:"provenance"`
	Modules    int    `json:"modules"`
	Records    int64  `json:"records"`
	Files      int    `json:"files,omitempty"`
}

// Verify reads a backup and checks its object against its sidecar; with
// identities it also decrypts it and checks every member against the
// manifest. It writes nothing and takes no lease.
func (a *App) Verify(ctx context.Context, id string, ids []age.Identity) (Verification, error) {
	sc, body, err := backup.Fetch(ctx, a.archive, a.instance, id)
	if err != nil {
		return Verification{}, err
	}
	v := Verification{ID: id, Object: sc.Object, Modules: len(sc.Modules), Records: sc.Records(), Provenance: ProvenanceNotChecked}
	if len(ids) == 0 {
		return v, nil
	}
	r, err := backup.Decrypt(ctx, sc, bytes.NewReader(body), ids, "")
	if err != nil {
		return v, err
	}
	defer r.Close()
	v.Decrypted, v.Files = true, len(r.Manifest().Files)+1
	return v, nil
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
