package audit

import (
	"context"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1/transportv1alpha1connect"
	"github.com/truvity/audit/emitter"

	keyv2alpha1 "github.com/truvity/sluis/v2/gen/go/sluis/key/v2alpha1"
	"github.com/truvity/sluis/v2/gen/go/sluis/key/v2alpha1/keyv2alpha1connect"
	"github.com/truvity/sluis/v2/kits/go/rpc"
)

const (
	// Product is sluis's name in audit's neutral model: metadata.product.name, and the product object unmapped.sluis.
	Product = "sluis"
	// PseudonymLabel is sluis's domain label of a pseudonym, which key@ binds into the MAC. A changed label changes every
	// pseudonym, so it stays as it was before the emitter moved to audit.
	PseudonymLabel = "sluis-pseudonym-v1"
)

// The types of audit's emitter, under the names sluis's callers use.
type (
	// Action is audit's emitter.Action.
	Action = emitter.Action
	// Activity is audit's emitter.Activity.
	Activity = emitter.Activity
	// Decision is audit's emitter.Decision.
	Decision = emitter.Decision
	// Emitter is audit's emitter.Emitter.
	Emitter = emitter.Emitter
	// Entity is audit's emitter.Entity.
	Entity = emitter.Entity
	// Event is audit's emitter.Event.
	Event = emitter.Event
	// PolicyRef is audit's emitter.PolicyRef.
	PolicyRef = emitter.PolicyRef
	// RecordInput is audit's emitter.RecordInput.
	RecordInput = emitter.RecordInput
	// Row is audit's emitter.Row.
	Row = emitter.Row
	// SchemaError is audit's emitter.SchemaError.
	SchemaError = emitter.SchemaError
	// Schemas is audit's emitter.Schemas.
	Schemas = emitter.Schemas
	// Status is audit's emitter.Status.
	Status = emitter.Status
	// Subject is audit's emitter.Subject.
	Subject = emitter.Subject
	// Table is audit's emitter.Table.
	Table = emitter.Table
)

// The constants of audit's emitter.
const (
	StringMax       = emitter.StringMax
	RecordMax       = emitter.RecordMax
	DefsName        = emitter.DefsName
	PolicyVersion   = emitter.PolicyVersion
	StatusSuccess   = emitter.StatusSuccess
	StatusFailure   = emitter.StatusFailure
	DecisionAllowed = emitter.DecisionAllowed
	DecisionDenied  = emitter.DecisionDenied
)

var (
	// Canonical is audit's emitter.Canonical.
	Canonical = emitter.Canonical
	// CheckPolicy is audit's emitter.CheckPolicy.
	CheckPolicy = emitter.CheckPolicy
	// MaxCost is audit's emitter.MaxCost.
	MaxCost = emitter.MaxCost
	// NewTable is audit's emitter.NewTable.
	NewTable = emitter.NewTable
	// ParseSchemas is audit's emitter.ParseSchemas.
	ParseSchemas = emitter.ParseSchemas
	// SourceName is audit's emitter.SourceName.
	SourceName = emitter.SourceName
	// ValidRelease is audit's emitter.ValidRelease.
	ValidRelease = emitter.ValidRelease
)

var (
	sluisOnce  sync.Once
	sluisTable *Table
)

// Sluis is the action table of sluis itself, generated from docs/architecture/audit-actions.yaml (zz_table.go). An emitter
// with no Table in its Config uses it.
func Sluis() *Table {
	sluisOnce.Do(func() {
		t, err := NewTable(sluisRows, sluisSchemas)
		if err != nil {
			panic("audit: the generated action table is broken: " + err.Error())
		}
		sluisTable = t
	})
	return sluisTable
}

// Config configures an Emitter in sluis's words: an actor of a boundary. New maps them to audit's neutral model, the
// actor to the component and the boundary to the scope.
type Config struct {
	Actor, Boundary, Release string // Release is version.Version
	// Transport is the client of the emit socket: rpc.Dial("/run/sluis/audit/<boundary>/emit.sock").
	Transport transportv1alpha1connect.TransportServiceClient
	// Keys is the client of key@; nil for an actor with no pseudonym rule, which then cannot send a Subject.
	Keys keyv2alpha1connect.KeyServiceClient
	// Now is the clock; nil means time.Now. A test injects one.
	Now func() time.Time
	// Table is the action table of the emitting project's release; nil means Sluis(), the table of sluis itself.
	Table *Table
}

// New returns an Emitter of sluis: the product sluis, the actor as the component, the boundary as the scope, the
// correlation id of kits/go/rpc, and pseudonyms made by key@ under sluis's label.
func New(cfg Config) (*Emitter, error) {
	table := cfg.Table
	if table == nil {
		table = Sluis()
	}
	var pseudonyms *emitter.Pseudonyms
	if cfg.Keys != nil {
		pseudonyms = &emitter.Pseudonyms{Mac: keyMac{cfg.Keys}, Label: PseudonymLabel}
	}
	return emitter.New(emitter.Config{
		Product: Product, Release: cfg.Release, Scope: cfg.Boundary, Component: cfg.Actor,
		Table: table, Transport: cfg.Transport, Pseudonyms: pseudonyms, Correlation: rpc.CorrelationID, Now: cfg.Now,
	})
}

// BuildRecord is audit's pure serialiser, with the product sluis when the input names none.
func BuildRecord(in RecordInput) ([]byte, error) {
	if in.Product == "" {
		in.Product = Product
	}
	return emitter.BuildRecord(in)
}

// keyMac is the Mac of the pseudonym purpose over key@: the emitter's request, as key@'s MacRequest.
type keyMac struct {
	keys keyv2alpha1connect.KeyServiceClient
}

func (k keyMac) Mac(ctx context.Context, r emitter.MacRequest) ([]byte, error) {
	resp, err := k.keys.Mac(ctx, connect.NewRequest(&keyv2alpha1.MacRequest{
		Purpose: r.Purpose, Tenant: r.Tenant, Label: r.Label, Input: r.Input, Epoch: r.Epoch,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetMac(), nil
}
