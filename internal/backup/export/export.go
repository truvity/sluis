package export

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"time"

	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
)

// BlobPrefixes are the blob prefixes each module owns and a backup keeps: what
// each controller last reported. The hub's directory snapshots (`google/`) are
// a cache that the module rewrites on its next refresh and are not kept.
var BlobPrefixes = map[port.Module][]string{
	port.ModuleGitHub: {"reports/github/"},
	port.ModuleSlack:  {"reports/slack/"},
}

// Source is what an export reads. All four are required.
type Source struct {
	// State and Index read every module's records and sets; the Router of the
	// DynamoDB adapter is both.
	State port.StateExporter
	Index port.IndexExporter
	// Secrets are the installation's secrets on layout v5.
	Secrets *secretstore.StoresV5
	// Blob holds the modules' blobs.
	Blob port.Blob
}

// Checkpoint is what a run keeps to continue after a stop: the number of units
// done and the writer's [backup.Progress]. It holds no secret and no record.
type Checkpoint struct {
	// Units is the length of the run's list of units; a resume refuses another.
	Units int `json:"units"`
	// Next is the number of units done.
	Next     int             `json:"next"`
	Progress backup.Progress `json:"progress"`
}

// Options are the optional parts of a run.
type Options struct {
	// Modules limits the run; the default is every module of [port.Modules].
	// A resume names the same ones.
	Modules []port.Module
	// Now is the clock the remaining lifetimes are made absolute by.
	Now func() time.Time
	// Resume continues from the last [Checkpoint] seen. The writer must be
	// one made with [backup.Resume] from the checkpoint's Progress.
	Resume *Checkpoint
	// Checkpoint is called after each unit with what a resume needs. An error
	// stops the run.
	Checkpoint func(context.Context, Checkpoint) error
	// Log receives one line per unit, with counts only. Nil is silent.
	Log *slog.Logger
}

type kind int

const (
	unitState kind = iota
	unitIndex
	unitSecrets
	unitBlobs
)

type unit struct {
	module port.Module
	kind   kind
	// arg is the key prefix, the namespace or the blob prefix.
	arg string
}

func (u unit) section() backup.Section {
	switch u.kind {
	case unitSecrets:
		return backup.Secrets
	case unitBlobs:
		return backup.Blobs
	}
	return backup.State
}

func (u unit) String() string {
	return fmt.Sprintf("%s/%s/%s", u.module, u.section(), u.arg)
}

// units is the fixed list a run walks: for each module its State families, its
// Index families, its secret namespaces, then its blob prefixes.
func units(mods []port.Module) []unit {
	var out []unit
	for _, m := range mods {
		for _, p := range port.StatePrefixes5(m) {
			out = append(out, unit{m, unitState, p})
		}
		for _, p := range port.SetPrefixes5(m) {
			out = append(out, unit{m, unitIndex, p})
		}
		out = append(out, unit{m, unitSecrets, Internal})
		if secretstore.ModuleExternal(secretstore.Module(m)) {
			out = append(out, unit{m, unitSecrets, External})
		}
		for _, p := range BlobPrefixes[m] {
			out = append(out, unit{m, unitBlobs, p})
		}
	}
	return out
}

// Run exports the source into w and closes it, returning the manifest. The
// writer is aborted when the run fails. See the package comment for resuming.
func Run(ctx context.Context, w *backup.Writer, src Source, opt Options) (*backup.Manifest, error) {
	m, err := run(ctx, w, src, opt)
	if err != nil {
		w.Abort()
		return nil, err
	}
	return m, nil
}

func run(ctx context.Context, w *backup.Writer, src Source, opt Options) (*backup.Manifest, error) {
	if src.State == nil || src.Index == nil || src.Secrets == nil || src.Blob == nil {
		return nil, errors.New("export: the source needs State, Index, Secrets and Blob")
	}
	mods := opt.Modules
	if len(mods) == 0 {
		mods = port.Modules()
	}
	for _, m := range mods {
		if !m.Valid() {
			return nil, fmt.Errorf("export: %q is not a module", m)
		}
	}
	list := units(mods)
	from := 0
	if r := opt.Resume; r != nil {
		if r.Units != len(list) || r.Next < 0 || r.Next > len(list) {
			return nil, fmt.Errorf("export: the checkpoint (%d of %d units) is for another run of %d units", r.Next, r.Units, len(list))
		}
		from = r.Next
	}
	e := &exporter{w: w, src: src, now: opt.Now}
	if e.now == nil {
		e.now = time.Now
	}
	for i := from; i < len(list); i++ {
		u := list[i]
		n, err := e.unit(ctx, u)
		if err != nil {
			return nil, fmt.Errorf("export %s: %w", u, err)
		}
		prog, err := w.Flush(ctx)
		if err != nil {
			return nil, err
		}
		if opt.Log != nil {
			opt.Log.InfoContext(ctx, "exported", slog.String("module", string(u.module)),
				slog.String("section", string(u.section())), slog.String("what", u.arg), slog.Int("records", n))
		}
		if opt.Checkpoint != nil {
			if err := opt.Checkpoint(ctx, Checkpoint{Units: len(list), Next: i + 1, Progress: prog}); err != nil {
				return nil, err
			}
		}
	}
	return w.Close(ctx)
}

type exporter struct {
	w   *backup.Writer
	src Source
	now func() time.Time
}

func (e *exporter) unit(ctx context.Context, u unit) (int, error) {
	switch u.kind {
	case unitState:
		return e.state(ctx, u)
	case unitIndex:
		return e.index(ctx, u)
	case unitSecrets:
		return e.secrets(ctx, u)
	}
	return e.blobs(ctx, u)
}

func (e *exporter) add(ctx context.Context, s backup.Section, m port.Module, rec any) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return e.w.Add(ctx, s, m, b)
}

// owned reports whether the exported key is a record of module m that a backup
// keeps: not another module's (an adapter that cannot route a prefix reads
// every table), not of no module, and never the maintenance record.
func owned(m port.Module, key string) bool {
	a, err := port.Locate5(key)
	return err == nil && a.Module == m && a.Kind != "maintenance"
}

func (e *exporter) state(ctx context.Context, u unit) (int, error) {
	n := 0
	now := e.now()
	err := e.src.State.ExportState(ctx, u.arg, func(x port.Exported) error {
		if !owned(u.module, x.Key) || x.TTL < 0 {
			return nil
		}
		n++
		return e.add(ctx, backup.State, u.module, State{V: RecordVersion, T: TypeState, Key: x.Key, Value: x.Value,
			Expires: expires(now, x.TTL)})
	})
	return n, err
}

func (e *exporter) index(ctx context.Context, u unit) (int, error) {
	n := 0
	now := e.now()
	err := e.src.Index.ExportIndex(ctx, u.arg, func(x port.Exported) error {
		if a, err := port.LocateSet5(x.Key); err != nil || a.Module != u.module || x.TTL < 0 {
			return nil
		}
		members := slices.Clone(x.Members)
		sort.Strings(members)
		n++
		return e.add(ctx, backup.State, u.module, Index{V: RecordVersion, T: TypeIndex, Key: x.Key, Members: members,
			Expires: expires(now, x.TTL)})
	})
	return n, err
}

func (e *exporter) secrets(ctx context.Context, u unit) (int, error) {
	sm := secretstore.Module(u.module)
	st := e.src.Secrets.InternalStore(sm)
	if u.arg == External {
		st = e.src.Secrets.ExternalStore(sm)
	}
	names, err := state.ListAll(ctx, st)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, name := range names {
		it, err := st.Get(ctx, name)
		if errors.Is(err, state.ErrNotFound) {
			continue // removed between the listing and the read
		}
		if err != nil {
			return n, fmt.Errorf("read %s: %w", name, err)
		}
		n++
		if err := e.add(ctx, backup.Secrets, u.module, Secret{V: RecordVersion, T: TypeSecret, NS: u.arg, Name: name,
			Doc: json.RawMessage(it.Value)}); err != nil {
			return n, err
		}
	}
	return n, nil
}

func (e *exporter) blobs(ctx context.Context, u unit) (int, error) {
	names, err := e.src.Blob.List(ctx, u.arg)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, name := range names {
		o, err := e.src.Blob.Read(ctx, name)
		if errors.Is(err, port.ErrNotFound) {
			continue
		}
		if err != nil {
			return n, fmt.Errorf("read %s: %w", name, err)
		}
		sum := sha256.Sum256(o.Body)
		parts := max(1, (len(o.Body)+BlobPart-1)/BlobPart)
		for i := range parts {
			part := o.Body[min(i*BlobPart, len(o.Body)):min((i+1)*BlobPart, len(o.Body))]
			n++
			if err := e.add(ctx, backup.Blobs, u.module, Blob{V: RecordVersion, T: TypeBlob, Name: name,
				Size: int64(len(o.Body)), SHA256: hex.EncodeToString(sum[:]), Part: i, Parts: parts, Data: part}); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}
