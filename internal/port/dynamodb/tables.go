package dynamodb

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/truvity/sluis/internal/port"
)

// # A table per module (layout 5, ADR 0072)
//
// Each module owns one table, with the same shape as the layout 4 table: `pk`
// is the kind (without the module prefix: `org`, not `github-org`), `sk` the id.
// A key is located by [port.Locate5]. The table carries its own `lease` and
// `notify` kinds, so Watch, Subscribe, Notify and ExportState of a module's
// [Store] poll and export that module's table and no other.
//
// A process holds the table of its own module for writing and the tables of the
// peers it is granted for reading ([Tables.Set]). The own State is wrapped in
// [port.Owned], so a write of another module's key is [port.ErrNotOwner] before
// any request; a peer is a [port.ReadOnly] view over the peer's own Store, and a
// read goes to that table only. A key of no module (a lease, a notification, a
// gate) is the table's own; a lease whose target names its module is that
// module's, and the oidc table refuses it.
//
// Opening makes no request. Only [Config.Create] (a test or development
// installation) calls the API at start; production tables are found by the
// readiness probe ([Tables.Ping]) and by the first use, where a missing table
// is [port.ErrUnavailable].

// TableName is the default name of a module's table: sluis-<instance>-<module>,
// which matches the SSM root /sluis/<instance>.
func TableName(instance string, m port.Module) string {
	return "sluis-" + instance + "-" + string(m)
}

// DefaultTables names every module's table for an instance.
func DefaultTables(instance string) map[port.Module]string {
	out := map[port.Module]string{}
	for _, m := range port.Modules() {
		out[m] = TableName(instance, m)
	}
	return out
}

// Tables is the stores of the configured modules' tables: the per-module
// router of layout 5.
type Tables struct {
	stores map[port.Module]*Store
}

// OpenTables connects with the platform's credentials and binds the tables of
// cfg.Tables. It makes no request unless cfg.Create is set.
func OpenTables(ctx context.Context, cfg Config, opts ...Option) (*Tables, error) {
	if err := checkTables(cfg); err != nil {
		return nil, err
	}
	client, err := newClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return NewTables(ctx, client, cfg, opts...)
}

func checkTables(cfg Config) error {
	if cfg.Table != "" {
		return errors.New("dynamodb: table and tables are both set (layout 4 and layout 5 do not mix)")
	}
	if len(cfg.Tables) == 0 {
		return errors.New("dynamodb: no tables")
	}
	names := map[string]port.Module{}
	for m, name := range cfg.Tables {
		if !m.Valid() {
			return fmt.Errorf("dynamodb: tables: %q is not a module", m)
		}
		if name == "" {
			return fmt.Errorf("dynamodb: tables: the %s module has no table name", m)
		}
		if other, dup := names[name]; dup {
			return fmt.Errorf("dynamodb: tables: %s and %s share the table %q", other, m, name)
		}
		names[name] = m
	}
	return nil
}

// NewTables binds the tables over a client the caller made.
func NewTables(ctx context.Context, api API, cfg Config, opts ...Option) (*Tables, error) {
	if err := checkTables(cfg); err != nil {
		return nil, err
	}
	t := &Tables{stores: map[port.Module]*Store{}}
	for m, name := range cfg.Tables {
		s := newStore(api, name, opts)
		s.v5, s.module = true, m
		t.stores[m] = s
	}
	if cfg.Create {
		for _, m := range t.Modules() {
			s := t.stores[m]
			if err := s.createTable(ctx); err != nil {
				return nil, unavailable(fmt.Errorf("table %q (%s): %w", s.table, m, err))
			}
		}
	}
	return t, nil
}

// Modules lists the modules that have a table, in the order of [port.Modules].
func (t *Tables) Modules() []port.Module {
	var out []port.Module
	for _, m := range port.Modules() {
		if _, ok := t.stores[m]; ok {
			out = append(out, m)
		}
	}
	return out
}

// Store is the module's table, with no ownership wrapper (the migration, the
// backup and a test use it).
func (t *Tables) Store(m port.Module) (*Store, bool) {
	s, ok := t.stores[m]
	return s, ok
}

// Set is the ports of a process that runs module own: State is own's table
// wrapped in [port.Owned], Index and Trigger are the table's, and Peers holds a
// read-only view of each named peer's table. A module without a table is an
// error: a role that lacks the grant has no name for it either.
func (t *Tables) Set(own port.Module, peers ...port.Module) (port.Set, error) {
	s, ok := t.stores[own]
	if !ok {
		return port.Set{}, fmt.Errorf("dynamodb: no table for the %s module", own)
	}
	set := s.Set()
	set.Module = own
	set.State = port.Owned(own, s)
	for _, p := range peers {
		if p == own {
			continue
		}
		ps, ok := t.stores[p]
		if !ok {
			return port.Set{}, fmt.Errorf("dynamodb: no table for the peer %s module", p)
		}
		if set.Peers == nil {
			set.Peers = map[port.Module]port.StateReader{}
		}
		set.Peers[p] = port.ReadOnly(ps)
	}
	return set, nil
}

// Ping is the readiness probe: every table is there and may be described. The
// error names the modules that fail.
func (t *Tables) Ping(ctx context.Context) error {
	var errs []error
	for _, m := range t.Modules() {
		if err := t.stores[m].Ping(ctx); err != nil {
			errs = append(errs, fmt.Errorf("table %q (%s): %w", t.stores[m].table, m, err))
		}
	}
	return errors.Join(errs...)
}

// Advance moves every table's clock forward, for a test.
func (t *Tables) Advance(d time.Duration) {
	for _, s := range t.stores {
		s.Advance(d)
	}
}

// Close is a no-op, as for a [Store].
func (t *Tables) Close() {}

// TableNames lists the configured table names, sorted.
func (t *Tables) TableNames() []string {
	var out []string
	for _, s := range t.stores {
		out = append(out, s.table)
	}
	sort.Strings(out)
	return out
}
