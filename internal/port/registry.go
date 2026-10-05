package port

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Concern is one thing an installation needs from its platform and an adapter
// provides: where state lives, where secrets live, and so on. The adapter is
// chosen by name, per concern (docs/explanation/ports.md, "Adapters").
type Concern string

// The concerns. State includes sessions (keys under `ses.`, with a lifetime);
// secrets are the dynamic secrets and the exports under `export/`.
const (
	ConcernState    Concern = "state"
	ConcernSecrets  Concern = "secrets"
	ConcernBlobs    Concern = "blobs"
	ConcernSigning  Concern = "signing"
	ConcernTrigger  Concern = "trigger"
	ConcernSchedule Concern = "schedule"
	ConcernAudit    Concern = "audit"
)

// Concerns lists every concern, in the order a table prints them.
var Concerns = []Concern{
	ConcernState, ConcernSecrets, ConcernBlobs, ConcernSigning,
	ConcernTrigger, ConcernSchedule, ConcernAudit,
}

// Valid reports whether c is one of [Concerns].
func (c Concern) Valid() bool {
	for _, k := range Concerns {
		if c == k {
			return true
		}
	}
	return false
}

// Runtime is where the sluis process itself runs.
type Runtime string

// The runtimes an adapter can work on.
const (
	// RuntimeKubernetes is a pod, long-running, possibly several replicas.
	RuntimeKubernetes Runtime = "kubernetes"
	// RuntimeLambda is an AWS Lambda function: no long-lived process, no
	// background goroutine between invocations.
	RuntimeLambda Runtime = "lambda"
	// RuntimeProcess is a plain process on a host.
	RuntimeProcess Runtime = "process"
)

// Runtimes lists them.
var Runtimes = []Runtime{RuntimeKubernetes, RuntimeLambda, RuntimeProcess}

// Valid reports whether r is one of [Runtimes].
func (r Runtime) Valid() bool {
	for _, k := range Runtimes {
		if r == k {
			return true
		}
	}
	return false
}

// Status says whether an adapter exists in this build.
type Status string

const (
	// StatusImplemented is an adapter that is built and registered.
	StatusImplemented Status = "implemented"
	// StatusOnRequest is a planned adapter: it appears in the matrix and is
	// refused at start, and is built when a deployment needs it.
	StatusOnRequest Status = "on-request"
)

// Requires is what an adapter needs from the platform. Each is the answer to
// one question of the preset decision tree.
type Requires struct {
	AWS        bool
	Kubernetes bool
	OpenBao    bool
}

// Settings are one adapter's settings as configuration gave them: a JSON
// object. The adapter decodes them with [Settings.Decode].
type Settings map[string]any

// Decode reads the settings into v, a pointer to a struct. A key v has no
// field for is an error naming it, so a typo is never ignored. Field names
// match case-insensitively (`keyId` is KeyID).
func (s Settings) Decode(v any) error {
	if len(s) == 0 {
		return nil
	}
	raw, err := json.Marshal(map[string]any(s))
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	return nil
}

// Factory builds an adapter from its settings. What it returns is the port
// the concern names: [State] for state (an adapter may also implement
// [Index] and [Trigger]), [Secrets] for secrets, [Blob] for blobs, [Trigger]
// for trigger. The concerns that are not ports of this package (signing,
// schedule, audit) have the wiring of their own package.
type Factory func(ctx context.Context, settings Settings) (any, error)

// Descriptor is what an adapter says about itself.
type Descriptor struct {
	// Name is the adapter's name in configuration, unique per concern.
	Name    string
	Concern Concern
	// Summary is one sentence for the matrix.
	Summary string
	// Requires are the platform answers that must be yes.
	Requires Requires
	// Runtimes are the runtimes it works on. Empty is all of them.
	Runtimes []Runtime
	Status   Status
	// ProcessLocal is an adapter that keeps its data in this process, so a
	// second replica would see another copy: refused above one replica.
	ProcessLocal bool
	// SecretStore is a secrets adapter that is a real secret store (access
	// controlled, audited, encrypted at rest). One that is not is refused
	// while the platform has one to offer.
	SecretStore bool
	// Factory builds the adapter. Nil for an on-request adapter, and for a
	// built-in adapter that the process wires itself because it needs more
	// than settings (the legacy adapter takes the cluster client and Valkey).
	Factory Factory
}

// Works reports whether the adapter runs on rt.
func (d Descriptor) Works(rt Runtime) bool {
	if len(d.Runtimes) == 0 {
		return true
	}
	for _, r := range d.Runtimes {
		if r == rt {
			return true
		}
	}
	return false
}

// Registry holds the adapters. Built-in adapters register themselves in the
// [Default] one from an init function.
type Registry struct {
	mu      sync.RWMutex
	adapter map[Concern]map[string]Descriptor
}

// NewRegistry returns an empty registry, with the planned adapters of
// [Catalogue] answering lookups.
func NewRegistry() *Registry {
	return &Registry{adapter: map[Concern]map[string]Descriptor{}}
}

// Default is the registry the built-in adapters register in.
var Default = NewRegistry()

// Register adds an implemented adapter to [Default]. It panics on an invalid
// descriptor or a name registered twice: both are programming errors found at
// start of any test run.
func Register(d Descriptor) { Default.Register(d) }

// Register adds an implemented adapter.
func (r *Registry) Register(d Descriptor) {
	if d.Name == "" || !d.Concern.Valid() {
		panic(fmt.Sprintf("port: adapter %q has no name or an unknown concern %q", d.Name, d.Concern))
	}
	if d.Status == "" {
		d.Status = StatusImplemented
	}
	if d.Status != StatusImplemented {
		panic(fmt.Sprintf("port: %s/%s: only an implemented adapter registers; planned ones are in the catalogue", d.Concern, d.Name))
	}
	for _, rt := range d.Runtimes {
		if !rt.Valid() {
			panic(fmt.Sprintf("port: %s/%s: unknown runtime %q", d.Concern, d.Name, rt))
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.adapter[d.Concern] == nil {
		r.adapter[d.Concern] = map[string]Descriptor{}
	}
	if _, dup := r.adapter[d.Concern][d.Name]; dup {
		panic(fmt.Sprintf("port: adapter %s/%s registered twice", d.Concern, d.Name))
	}
	r.adapter[d.Concern][d.Name] = d
}

// Lookup finds an adapter: a registered one, else a planned one of the
// catalogue (whose Status says it is not built).
func (r *Registry) Lookup(c Concern, name string) (Descriptor, bool) {
	r.mu.RLock()
	d, ok := r.adapter[c][name]
	r.mu.RUnlock()
	if ok {
		return d, true
	}
	for _, p := range Catalogue {
		if p.Concern == c && p.Name == name {
			return p, true
		}
	}
	return Descriptor{}, false
}

// Names lists the adapter names of a concern, implemented first, sorted.
func (r *Registry) Names(c Concern) []string {
	var done, planned []string
	for _, d := range r.Matrix() {
		if d.Concern != c {
			continue
		}
		if d.Status == StatusImplemented {
			done = append(done, d.Name)
		} else {
			planned = append(planned, d.Name)
		}
	}
	return append(done, planned...)
}

// Matrix is every adapter, registered or planned, ordered by concern (in the
// order of [Concerns]) and name. It is what the compatibility and
// implementation matrix of the documentation is generated from.
func (r *Registry) Matrix() []Descriptor {
	r.mu.RLock()
	var all []Descriptor
	seen := map[[2]string]bool{}
	for c, byName := range r.adapter {
		for n, d := range byName {
			all = append(all, d)
			seen[[2]string{string(c), n}] = true
		}
	}
	r.mu.RUnlock()
	for _, p := range Catalogue {
		if !seen[[2]string{string(p.Concern), p.Name}] {
			all = append(all, p)
		}
	}
	order := map[Concern]int{}
	for i, c := range Concerns {
		order[c] = i
	}
	sort.Slice(all, func(i, j int) bool {
		if order[all[i].Concern] != order[all[j].Concern] {
			return order[all[i].Concern] < order[all[j].Concern]
		}
		return all[i].Name < all[j].Name
	})
	return all
}

// Catalogue is the static list of the planned adapters, the ones built on
// request. An adapter leaves it by being registered: the catalogue never
// shadows a registered adapter.
var Catalogue = []Descriptor{
	planned(ConcernState, "kubernetes", "State in a ConfigMap the service rebuilds and owns.", Requires{Kubernetes: true}, RuntimeKubernetes),
	planned(ConcernState, "postgres", "State in a PostgreSQL table.", Requires{}),
	planned(ConcernState, "valkey", "State, sessions included, in Valkey.", Requires{}),
	planned(ConcernSecrets, "kubernetes", "Dynamic secrets and exports as Kubernetes Secrets the service writes.", Requires{Kubernetes: true}, RuntimeKubernetes),
	planned(ConcernSecrets, "store", "Secrets in the service's own encrypted store, for a platform with no secret store.", Requires{}),
	planned(ConcernBlobs, "postgres", "Blobs in PostgreSQL large objects.", Requires{}),
	planned(ConcernBlobs, "off", "No blob storage: reports and snapshots are not kept.", Requires{}),
	planned(ConcernSigning, "generated", "A token-signing key generated at start and shared through state.", Requires{}),
	planned(ConcernSigning, "transit", "Token signing by an OpenBao transit key.", Requires{OpenBao: true}),
	planned(ConcernTrigger, "watch", "\"Run a pass now\" from a Kubernetes watch.", Requires{Kubernetes: true}, RuntimeKubernetes),
	planned(ConcernTrigger, "http", "\"Run a pass now\" from an authenticated HTTP request.", Requires{}),
}

func planned(c Concern, name, summary string, req Requires, rts ...Runtime) Descriptor {
	return Descriptor{Name: name, Concern: c, Summary: summary, Requires: req, Runtimes: rts, Status: StatusOnRequest}
}

// ErrNotBuilt is the error for an adapter that is only planned.
var ErrNotBuilt = errors.New("port: adapter is planned (on request) and not built")
