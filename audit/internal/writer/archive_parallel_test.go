package writer_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvity/sluis/audit/internal/writer"
	"github.com/truvity/sluis/audit/sdk/catalogue"
	"github.com/truvity/sluis/audit/store"
	"github.com/truvity/sluis/audit/store/storetest"
)

// A writer ensures every catalogue it runs with before it takes a record. On
// a function platform that is inside the platform's start-up limit, so the
// schemas are written several at a time, after the catalogue document.

// manySchemas is a catalogue of n actions, each with an extension schema of
// its own.
func manySchemas(t *testing.T, n int) *catalogue.Catalogue {
	t.Helper()
	var doc strings.Builder
	doc.WriteString(`
source: wide
version: "1.0.0"
locales: [en]
actor_kinds:
  operator: { category: internal }
target_types:
  thing: { description: "A thing." }
actions:
`)
	schemas := make([][]byte, 0, n)
	for i := range n {
		id := fmt.Sprintf("https://schemas.example/wide/action-%02d.json", i)
		fmt.Fprintf(&doc, `  wide.thing.changed%02d:
    summary: A thing changed.
    operation: modify
    categories: [credential_lifecycle]
    profiles: [security]
    target_types: [thing]
    data_schema: %s
    message:
      en: "{actor} changed a thing"
`, i, id)
		schemas = append(schemas, fmt.Appendf(nil, `{
  "$id": %q,
  "type": "object",
  "additionalProperties": false,
  "properties": {"note": {"type": "string", "x-audit-class": "audit", "x-audit-pii": "none"}}
}`, id))
	}
	c, err := catalogue.Load([]byte(doc.String()), schemas)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(c.Schemas()); got != n {
		t.Fatalf("the catalogue has %d schemas, want %d", got, n)
	}
	return c
}

// gated is the test store with a hook on every put: it counts the puts in
// flight and the most there ever were, keeps the order puts began and ended
// in, and lets a test hold or fail a put.
type gated struct {
	*storetest.Memory
	// hold runs inside a put, before it writes; nil holds nothing.
	hold func(ctx context.Context, key string) error

	inFlight, peak atomic.Int64
	mu             sync.Mutex
	events         []string
}

func (g *gated) Put(ctx context.Context, o store.Object) error {
	g.event("begin " + o.Key)
	defer g.event("end " + o.Key)
	n := g.inFlight.Add(1)
	defer g.inFlight.Add(-1)
	for {
		p := g.peak.Load()
		if n <= p || g.peak.CompareAndSwap(p, n) {
			break
		}
	}
	if g.hold != nil {
		if err := g.hold(ctx, o.Key); err != nil {
			return err
		}
	}
	return g.Memory.Put(ctx, o)
}

func (g *gated) event(e string) {
	g.mu.Lock()
	g.events = append(g.events, e)
	g.mu.Unlock()
}

func (g *gated) log() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.events...)
}

// untilFull holds every schema put until limit puts are in flight together,
// so a test sees the most the ensure ever sends at once. An ensure that sent
// fewer would wait out the bound here and then fail on the peak.
func untilFull(g *gated, limit int64) func(context.Context, string) error {
	full := make(chan struct{})
	var once sync.Once
	return func(ctx context.Context, key string) error {
		if !strings.HasPrefix(key, "schema/") {
			return nil
		}
		if g.inFlight.Load() >= limit {
			once.Do(func() { close(full) })
		}
		select {
		case <-full:
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}
}

func gatedArchive(t *testing.T, parallel int) (*writer.SchemaArchive, *gated) {
	t.Helper()
	a, mem := archive(t)
	g := &gated{Memory: mem}
	a.Store = g
	a.Parallel = parallel
	return a, g
}

// The schemas go several at a time, never more than the limit, and only once
// the catalogue document has been written and compared.
func TestEnsureCatalogueWritesSchemasInParallelWithinTheLimit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		parallel int
		want     int64
	}{
		{"configured", 4, 4},
		{"default", 0, writer.DefaultParallel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, g := gatedArchive(t, tc.parallel)
			g.hold = untilFull(g, tc.want)
			c := manySchemas(t, 20)

			n, err := a.EnsureCatalogueCounted(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			if got := g.peak.Load(); got != tc.want {
				t.Fatalf("at most %d puts were in flight at once, want exactly the limit %d", got, tc.want)
			}
			if n.Objects != 21 || n.Existed != 0 {
				t.Fatalf("counted %+v, want 21 objects written and none already there", n)
			}

			// The catalogue document is the first put, and ends before any
			// schema begins.
			events := g.log()
			if events[0] != "begin catalogue/wide/1.0.0" || events[1] != "end catalogue/wide/1.0.0" {
				t.Fatalf("the catalogue document was not written alone and first: %v", events[:4])
			}
			if got := len(g.Keys()); got != 21 {
				t.Fatalf("the archive holds %d objects, want the document and 20 schemas", got)
			}
		})
	}
}

// A catalogue whose document conflicts writes none of its schemas: they
// would be locked beside a document that does not describe them.
func TestAConflictingCatalogueWritesNoSchema(t *testing.T) {
	a, g := gatedArchive(t, 4)
	c := manySchemas(t, 10)
	if err := g.Memory.Put(context.Background(), store.Object{Key: "catalogue/wide/1.0.0", Body: []byte("another document")}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.EnsureCatalogueCounted(context.Background(), c); !errors.Is(err, writer.ErrCatalogueConflict) {
		t.Fatalf("a different document under the version: %v", err)
	}
	for _, k := range g.Keys() {
		if strings.HasPrefix(k, "schema/") {
			t.Fatalf("a schema was written for a conflicting catalogue: %s", k)
		}
	}
}

// Every key already taken, as on every cold start after the first, is
// success, and is counted as such.
func TestEnsureCatalogueCountsWhatWasAlreadyThere(t *testing.T) {
	a, g := gatedArchive(t, 4)
	c := manySchemas(t, 12)
	ctx := context.Background()
	first := &writer.SchemaArchive{Store: g.Memory, Now: a.Now, RetainUntil: a.RetainUntil}
	if err := first.EnsureCatalogue(ctx, c); err != nil {
		t.Fatal(err)
	}
	// A writer starting later, which has archived nothing itself.
	before := g.Len()

	n, err := a.EnsureCatalogueCounted(ctx, c)
	if err != nil {
		t.Fatalf("keys already taken must be success: %v", err)
	}
	if n.Objects != 13 || n.Existed != 13 {
		t.Fatalf("counted %+v, want 13 objects, all already there", n)
	}
	if g.Len() != before {
		t.Fatalf("an object was written again: %d, was %d", g.Len(), before)
	}
	// A second ensure of the same catalogue does nothing at all.
	if n, err := a.EnsureCatalogueCounted(ctx, c); err != nil || n != (writer.Archived{}) {
		t.Fatalf("an ensured catalogue was ensured again: %+v, %v", n, err)
	}
}

// The first failure stops the rest: the puts still waiting see their context
// end, no more are sent, the error names the key, and the catalogue is not
// marked done, so the next ensure writes what is missing.
func TestTheFirstFailureCancelsTheRest(t *testing.T) {
	a, g := gatedArchive(t, 4)
	c := manySchemas(t, 20)
	boom := errors.New("the store said no")
	var failing string
	var cancelled atomic.Int64
	var mu sync.Mutex
	g.hold = func(ctx context.Context, key string) error {
		if !strings.HasPrefix(key, "schema/") {
			return nil
		}
		mu.Lock()
		first := failing == ""
		if first {
			failing = key
		}
		mu.Unlock()
		if first {
			// Fail once the others are waiting.
			for g.inFlight.Load() < 4 {
				time.Sleep(time.Millisecond)
			}
			return boom
		}
		select {
		case <-ctx.Done():
			cancelled.Add(1)
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return errors.New("a put was not cancelled")
		}
	}

	_, err := a.EnsureCatalogueCounted(context.Background(), c)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the first failure", err)
	}
	if !strings.Contains(err.Error(), failing) {
		t.Fatalf("the error %q does not name the key %s", err, failing)
	}
	if got := cancelled.Load(); got != 3 {
		t.Fatalf("%d of the puts in flight were cancelled, want the other 3", got)
	}
	begun := 0
	for _, e := range g.log() {
		if strings.HasPrefix(e, "begin schema/") {
			begun++
		}
	}
	if begun != 4 {
		t.Fatalf("%d schema puts were sent, want only the 4 in flight when the first failed", begun)
	}

	g.hold = nil
	n, err := a.EnsureCatalogueCounted(context.Background(), c)
	if err != nil {
		t.Fatalf("the next ensure: %v", err)
	}
	if n.Objects != 21 || n.Existed != 1 {
		t.Fatalf("the next ensure counted %+v, want 21 objects with only the document already there", n)
	}
}
