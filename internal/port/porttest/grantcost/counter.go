package grantcost

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/port"
)

// The ports an [Op] crossed, and the one seam that is not a port: a
// directory resolution, which is what the hub's ResolveUser answers.
const (
	PortState     = "state"
	PortIndex     = "index"
	PortBlob      = "blob"
	PortDirectory = "directory"
)

// Op is one call that crossed a port. It names the key's KIND from the
// layout (internal/port/keys.go) and never the key: a key names a person or
// hashes a bearer token, and a test log is read by people who should see
// neither.
type Op struct {
	Port  string
	Call  string
	Kind  string
	Write bool
}

func (o Op) String() string { return o.Port + " " + o.Call + " " + o.Kind }

// Counter is a decorator over the storage ports that writes down every call
// it passes on, in order. It changes nothing: every call goes to the inner
// adapter with the same arguments, and its answer comes back unchanged.
//
// internal/port/observe is the production decorator and counts the same
// calls into a histogram; this one keeps the sequence instead, because what
// a budget failure needs is WHICH call was added, not that one was.
type Counter struct {
	mu  sync.Mutex
	ops []Op
}

func (c *Counter) add(op Op) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ops = append(c.ops, op)
}

// Reset forgets what was counted.
func (c *Counter) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ops = nil
}

// Ops is what was counted since the last [Counter.Reset], in order.
func (c *Counter) Ops() []Op {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.ops)
}

func stateKind(key string) string {
	if a, err := port.Locate(key); err == nil {
		return a.Kind
	}
	return "?"
}

func prefixKind(prefix string) string {
	if kind, _, ok := port.LocatePrefix(prefix); ok {
		return kind
	}
	return "?"
}

func setKind(set string) string {
	if a, err := port.LocateSet(set); err == nil {
		return a.Kind
	}
	return "?"
}

// blobKind is a blob name's family: `google/` of `google/<workspace>`.
func blobKind(name string) string {
	if family, _, ok := strings.Cut(name, "/"); ok {
		return family + "/"
	}
	return "?"
}

// State counts a [port.State]. Watch is passed through uncounted: it is a
// stream a process holds open, not a per-request cost.
func (c *Counter) State(inner port.State) port.State { return countedState{inner, c} }

type countedState struct {
	port.State
	c *Counter
}

func (s countedState) Get(ctx context.Context, key string) (port.Record, error) {
	s.c.add(Op{Port: PortState, Call: "get", Kind: stateKind(key)})
	return s.State.Get(ctx, key)
}

// PeekRevision counts a [port.RevisionPeeker] read as a read, passed to
// the adapter's own when it has one, so that what is measured is the call
// production makes (an eventually consistent read on DynamoDB).
func (s countedState) PeekRevision(ctx context.Context, key string) (port.Revision, error) {
	s.c.add(Op{Port: PortState, Call: "peek_revision", Kind: stateKind(key)})
	if peeker, ok := s.State.(port.RevisionPeeker); ok {
		return peeker.PeekRevision(ctx, key)
	}
	record, err := s.State.Get(ctx, key)
	return record.Revision, err
}

func (s countedState) Put(ctx context.Context, key string, value []byte, ttl time.Duration) (port.Revision, error) {
	s.c.add(Op{Port: PortState, Call: "put", Kind: stateKind(key), Write: true})
	return s.State.Put(ctx, key, value, ttl)
}

func (s countedState) Create(ctx context.Context, key string, value []byte, ttl time.Duration) (port.Revision, error) {
	s.c.add(Op{Port: PortState, Call: "create", Kind: stateKind(key), Write: true})
	return s.State.Create(ctx, key, value, ttl)
}

func (s countedState) Update(
	ctx context.Context, key string, value []byte, ttl time.Duration, rev port.Revision,
) (port.Revision, error) {
	s.c.add(Op{Port: PortState, Call: "update", Kind: stateKind(key), Write: true})
	return s.State.Update(ctx, key, value, ttl, rev)
}

func (s countedState) Delete(ctx context.Context, key string) error {
	s.c.add(Op{Port: PortState, Call: "delete", Kind: stateKind(key), Write: true})
	return s.State.Delete(ctx, key)
}

func (s countedState) DeleteIfRevision(ctx context.Context, key string, rev port.Revision) error {
	s.c.add(Op{Port: PortState, Call: "delete_if_revision", Kind: stateKind(key), Write: true})
	return s.State.DeleteIfRevision(ctx, key, rev)
}

func (s countedState) List(ctx context.Context, prefix, page string, limit int) (port.Page, error) {
	s.c.add(Op{Port: PortState, Call: "list", Kind: prefixKind(prefix)})
	return s.State.List(ctx, prefix, page, limit)
}

// Index counts a [port.Index].
func (c *Counter) Index(inner port.Index) port.Index { return countedIndex{inner, c} }

type countedIndex struct {
	port.Index
	c *Counter
}

func (i countedIndex) Add(ctx context.Context, key, member string, ttl time.Duration) error {
	i.c.add(Op{Port: PortIndex, Call: "add", Kind: setKind(key), Write: true})
	return i.Index.Add(ctx, key, member, ttl)
}

func (i countedIndex) Remove(ctx context.Context, key, member string) error {
	i.c.add(Op{Port: PortIndex, Call: "remove", Kind: setKind(key), Write: true})
	return i.Index.Remove(ctx, key, member)
}

func (i countedIndex) Members(ctx context.Context, key string) ([]string, error) {
	i.c.add(Op{Port: PortIndex, Call: "members", Kind: setKind(key)})
	return i.Index.Members(ctx, key)
}

// Blob counts a [port.Blob]. The optional capabilities are not carried:
// nothing a grant touches asks for them.
func (c *Counter) Blob(inner port.Blob) port.Blob { return countedBlob{inner, c} }

type countedBlob struct {
	port.Blob
	c *Counter
}

func (b countedBlob) Read(ctx context.Context, name string) (port.Object, error) {
	b.c.add(Op{Port: PortBlob, Call: "read", Kind: blobKind(name)})
	return b.Blob.Read(ctx, name)
}

func (b countedBlob) Write(ctx context.Context, name string, body []byte) (string, error) {
	b.c.add(Op{Port: PortBlob, Call: "write", Kind: blobKind(name), Write: true})
	return b.Blob.Write(ctx, name, body)
}

func (b countedBlob) WriteIfVersion(ctx context.Context, name string, body []byte, version string) (string, error) {
	b.c.add(Op{Port: PortBlob, Call: "write_if_version", Kind: blobKind(name), Write: true})
	return b.Blob.WriteIfVersion(ctx, name, body, version)
}

func (b countedBlob) Delete(ctx context.Context, name string) error {
	b.c.add(Op{Port: PortBlob, Call: "delete", Kind: blobKind(name), Write: true})
	return b.Blob.Delete(ctx, name)
}

func (b countedBlob) List(ctx context.Context, prefix string) ([]string, error) {
	b.c.add(Op{Port: PortBlob, Call: "list", Kind: blobKind(prefix)})
	return b.Blob.List(ctx, prefix)
}

// Resolver is the hub's ResolveUser, which internal/hublocal turns into the
// issuer's Directory. Counting here counts what the hub is asked, which is
// what costs a workspace listing and a snapshot read every time.
type Resolver interface {
	ResolveUser(ctx context.Context, email string, maxAge *time.Duration) (hub.UserResult, error)
}

// Directory counts the resolutions a [Resolver] answers.
func (c *Counter) Directory(inner Resolver) Resolver { return countedResolver{inner, c} }

type countedResolver struct {
	inner Resolver
	c     *Counter
}

func (r countedResolver) ResolveUser(ctx context.Context, email string, maxAge *time.Duration) (hub.UserResult, error) {
	r.c.add(Op{Port: PortDirectory, Call: "resolve_user", Kind: "hub"})
	return r.inner.ResolveUser(ctx, email, maxAge)
}

// Counts is what one measured step cost.
type Counts struct {
	// Writes and Reads are the State and Index calls: what the issuer's
	// table is billed for, one request each on the DynamoDB adapter.
	Writes, Reads int
	// SnapshotReads are blob reads of a directory snapshot: an S3 GET and a
	// decode each.
	SnapshotReads int
	// Resolutions are the times the hub was asked about a person.
	Resolutions int
	// Engine is the adapter's own call counts during the step, when its
	// [Env] can see them (DynamoDB: PutItem, GetItem, Query, ...).
	Engine map[string]int
	// Ops is every port call, in order.
	Ops []Op
}

// engineWrites and engineReads are the DynamoDB operations that are billed
// as a write and as a read. An engine with other names counts none of them,
// and its run asserts on the port counts alone.
var (
	engineWrites = []string{"PutItem", "DeleteItem", "UpdateItem", "BatchWriteItem", "TransactWriteItems"}
	engineReads  = []string{"GetItem", "Query", "Scan", "BatchGetItem", "TransactGetItems"}
)

// EngineWrites is the engine's write requests during the step.
func (c Counts) EngineWrites() int { return sum(c.Engine, engineWrites) }

// EngineReads is the engine's read requests during the step.
func (c Counts) EngineReads() int { return sum(c.Engine, engineReads) }

func sum(m map[string]int, names []string) int {
	n := 0
	for _, name := range names {
		n += m[name]
	}
	return n
}

func countsOf(ops []Op) Counts {
	out := Counts{Ops: ops}
	for _, op := range ops {
		switch op.Port {
		case PortState, PortIndex:
			if op.Write {
				out.Writes++
			} else {
				out.Reads++
			}
		case PortBlob:
			if !op.Write && op.Kind == hub.SnapshotBlobPrefix {
				out.SnapshotReads++
			}
		case PortDirectory:
			out.Resolutions++
		}
	}
	return out
}

// Summary is one line: the totals, then the engine's own counts.
func (c Counts) Summary() string {
	s := fmt.Sprintf("writes=%d reads=%d resolutions=%d snapshot-reads=%d",
		c.Writes, c.Reads, c.Resolutions, c.SnapshotReads)
	if c.Engine != nil {
		s += fmt.Sprintf(" | engine writes=%d reads=%d %v", c.EngineWrites(), c.EngineReads(), c.Engine)
	}
	return s
}

// Breakdown is every call in order, one per line, then the same grouped by
// call and kind with how many times each happened.
func (c Counts) Breakdown() string {
	var b strings.Builder
	grouped := map[string]int{}
	for i, op := range c.Ops {
		fmt.Fprintf(&b, "  %2d. %s\n", i+1, op)
		grouped[op.String()]++
	}
	b.WriteString("  by call:\n")
	for _, key := range slices.Sorted(maps.Keys(grouped)) {
		fmt.Fprintf(&b, "    %-60s x%d\n", key, grouped[key])
	}
	return b.String()
}
