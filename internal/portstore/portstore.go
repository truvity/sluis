// Package portstore keeps the domain stores (workspaces and their
// credentials, GitHub organisations and their Apps, a person's GitHub link,
// the Slack records) on the ports of internal/port: State for the records and
// Secrets for every credential.
//
// Each type here implements the interface the business code already names (a
// hub.Store, a server.GitHubLinks, and so on), so nothing above it changes;
// internal/app chooses between these and the ConfigMap and Secret stores of
// internal/kube by the adapter `ports.adapter` names. The layout is the one of
// docs/concepts/sluis/ports.md:
//
//	ws.dir.<provider>.<id>  a directory workspace: record, credential in Secrets
//	ws.slack.<workspace>    a Slack workspace: record, bot token in Secrets
//	gh.org.<org>            a GitHub organisation: record, App key in Secrets
//	gh.link.<account>       a person's GitHub link, tokens in Secrets
//	app.gh.link             the link App: record, client secret in Secrets
//	app.gh.runner.<tier>.<org>   a runner App: record, key in Secrets
//	app.gh.cat.<id>         a catalogue GitHub App: record, key in Secrets
//	app.slack.cat.<id>      a catalogue Slack App: record, secrets in Secrets
//	rec.slack.shared.<name> a Slack Connect channel definition
//	rec.slack.channel.<workspace>.<name>   a console channel's record
//	rec.console.session-key the console's session-signing key, in Secrets
//	gate.github.<org>.confirm | .pass      held-once ledger entries
//	gate.slack.<workspace>.confirm.<channel> | .pass
//
// A credential is never written to State: it is a secret under
// `credentials/<kind>/<id>/<ref>` in the Secrets port (an SSM parameter in production),
// and the item in State names it by its ref. A secret is written before the
// item that names it, under a fresh ref, so a reader never finds a name without
// its secret and a writer that loses the swap never replaces the winner's
// secret; one it wrote for nothing is removed. Every update of State is a compare-and-swap on the
// key's revision, retried against what a concurrent writer left; there are no
// multi-key writes, and a flow that spans keys (a person claiming an address
// another account held) is idempotent steps with a marker.
package portstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
)

// ErrBusy is a record that kept changing under every retry of a
// compare-and-swap.
var ErrBusy = errors.New("portstore: the record kept changing; try again")

// errKeep is what a change function returns to leave the key as it is: no
// write, no new revision, and no error.
var errKeep = errors.New("portstore: keep")

// attempts bounds a compare-and-swap loop.
const attempts = 16

// Base is what every store here shares: the State to write records into, Secrets
// for what must not be readable there, and the clock.
type Base struct {
	State   port.State
	Secrets port.Secrets
	Now     func() time.Time

	// Module is the module State belongs to, and Peers are the read-only
	// views of the modules this process may read (see [port.Set]).
	Module port.Module
	Peers  map[port.Module]port.StateReader

	// v4 and exportApp put the Apps' exported credentials on layout v4 (see
	// [Base.WithV4]).
	v4        *secretstore.Stores
	exportApp func(id string) bool

	// declared is what the configuration says of the GitHub Apps (see
	// [Base.DeclareGitHubApps]).
	declared DeclaredGitHubApps

	// v5 puts the GitHub Apps' credentials and exports on layout v5 (see
	// [Base.WithV5]).
	v5 *secretstore.StoresV5
}

// New returns the base over a set of ports.
func New(set port.Set) *Base {
	return &Base{State: set.State, Secrets: set.Secrets, Now: time.Now, Module: set.Module, Peers: set.Peers}
}

// Peer is the read-only view of another module's State, if the process holds
// one.
func (b *Base) Peer(m port.Module) (port.StateReader, bool) {
	r, ok := b.Peers[m]
	return r, ok && r != nil
}

// item is a record with, beside it, the name of one secret kept in Secrets
// under the item's key. The name and the record change in one write; the secret
// is written first, under a name of its own (see [Base.newSecret]).
type item struct {
	Version int             `json:"v"`
	Record  json.RawMessage `json:"record,omitempty"`
	// Secret names the credential of the item in Secrets (see [Base.newSecret]);
	// empty is none.
	Secret string `json:"secret,omitempty"`
}

const itemVersion = 1

func decodeItem(raw []byte) (*item, error) {
	var it item
	if err := json.Unmarshal(raw, &it); err != nil {
		return nil, errors.New("portstore: a stored item does not decode")
	}
	if it.Version != itemVersion {
		return nil, fmt.Errorf("portstore: a stored item is version %d", it.Version)
	}
	return &it, nil
}

func encodeItem(it *item) []byte {
	it.Version = itemVersion
	raw, _ := json.Marshal(it) // strings and bytes only
	return raw
}

// secretPrefix is where the credentials live in Secrets:
// `credentials/<kind>/<id>/<ref>`, the kind and id being the item's address in
// the storage layout (internal/port/keys.go; docs/reference/sluis/storage-layout.md).
const secretPrefix = port.CredentialsPrefix

// secretPath is the Secrets path of a credential of an item key: the item's
// address, kind and id, and the ref the item names (none for a single stable
// item such as the console's session key). A segment of the id that a secret
// path cannot hold (a `~`, an empty one, or one that would be mistaken for the
// rewritten form) becomes `u-` and its bytes in hex; the mapping is one to
// one. A key no rule names is kept under the kind `other`, whole.
func secretPath(key, ref string) string {
	addr, err := port.Locate(key)
	if err != nil {
		addr = port.Address{Kind: port.KindOther, ID: key}
	}
	segs := strings.Split(addr.ID, "/")
	if addr.Kind == port.KindOther {
		segs = []string{addr.ID}
	}
	for i, s := range segs {
		if s == "" || strings.Contains(s, "~") || strings.HasPrefix(s, "u-") || !validSecretSegment(s) {
			segs[i] = "u-" + fmt.Sprintf("%x", s)
		}
	}
	p := secretPrefix + addr.Kind + "/" + strings.Join(segs, "/")
	if ref != "" {
		p += "/" + ref
	}
	return p
}

// validSecretSegment is a segment [port.CheckSecretPath] accepts.
func validSecretSegment(s string) bool {
	return s != "." && s != ".." && port.CheckSecretPath(s) == nil
}

var errNoSecrets = errors.New("portstore: no Secrets: a credential is never written in State")

// newSecret writes a credential under a ref of its own and returns the ref,
// for the item to name. A credential is never rewritten in place: a writer that
// loses the compare-and-swap of the item has written a secret nobody names
// (which [Base.editItem] removes), and cannot have replaced the one the
// winner's item names. That is what keeps a single-use refresh token from
// being overwritten by a stale writer.
func (b *Base) newSecret(ctx context.Context, key string, plaintext []byte) (string, error) {
	app, v5 := b.appOfKey(key)
	if b.Secrets == nil && !v5 {
		return "", errNoSecrets
	}
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("%w: %w", port.ErrUnavailable, err)
	}
	ref := hex.EncodeToString(raw[:])
	if v5 {
		if _, err := b.v5.GitHub().AppCredential(app, ref).Put(ctx, plaintext, ""); err != nil {
			return "", err
		}
		return ref, nil
	}
	if _, err := b.Secrets.Put(ctx, secretPath(key, ref), plaintext); err != nil {
		return "", err
	}
	return ref, nil
}

// getSecret reads the credential an item names.
func (b *Base) getSecret(ctx context.Context, key, ref string) ([]byte, error) {
	if app, ok := b.appOfKey(key); ok {
		val, _, err := b.v5.GitHub().AppCredential(app, ref).Get(ctx)
		if errors.Is(err, state.ErrNotFound) {
			return nil, port.ErrNotFound
		}
		return val, err
	}
	if b.Secrets == nil {
		return nil, errNoSecrets
	}
	s, err := b.Secrets.Get(ctx, secretPath(key, ref))
	if err != nil {
		return nil, err
	}
	return s.Value, nil
}

// dropSecrets removes credentials no item names any more. It is a clean-up
// and best effort: a secret that is left behind is unreachable, and the next
// removal of the key's item does not know it, so a failure is not an error.
func (b *Base) dropSecrets(ctx context.Context, key string, refs ...string) {
	app, v5 := b.appOfKey(key)
	if b.Secrets == nil && !v5 {
		return
	}
	for _, ref := range refs {
		switch {
		case ref == "":
		case v5:
			_ = b.v5.GitHub().DeleteAppCredential(ctx, app, ref)
		default:
			_ = b.Secrets.Delete(ctx, secretPath(key, ref))
		}
	}
}

// deleteItem forgets an item and, after it, the credential it named.
func (b *Base) deleteItem(ctx context.Context, key string) error {
	it, _ := b.getItem(ctx, key) // unreadable: the item goes, its secret stays unreachable
	if err := b.State.Delete(ctx, key); err != nil {
		return err
	}
	if it != nil {
		b.dropSecrets(ctx, key, it.Secret)
	}
	return nil
}

// editRaw is one compare-and-swap read-modify-write of a key: change gets the
// current value (exists false when there is none) and returns the next one, or
// keep false to delete. It runs again on every conflict against what the
// other writer left, so what it checks is what is written over. An error from
// change writes nothing and is returned as it is; a conflict that outlasts the
// retries is [ErrBusy].
func (b *Base) editRaw(
	ctx context.Context, key string, ttl time.Duration,
	change func(cur []byte, exists bool) (next []byte, keep bool, err error),
) error {
	for range attempts {
		var cur []byte
		var rev port.Revision
		rec, err := b.State.Get(ctx, key)
		exists := err == nil
		switch {
		case errors.Is(err, port.ErrNotFound):
		case err != nil:
			return err
		default:
			cur, rev = rec.Value, rec.Revision
		}
		next, keep, err := change(cur, exists)
		if errors.Is(err, errKeep) {
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case !keep && !exists:
			return nil
		case !keep:
			err = b.State.DeleteIfRevision(ctx, key, rev)
			if errors.Is(err, port.ErrNotFound) {
				return nil
			}
		case !exists:
			_, err = b.State.Create(ctx, key, next, ttl)
		default:
			_, err = b.State.Update(ctx, key, next, ttl, rev)
		}
		if errors.Is(err, port.ErrConflict) || errors.Is(err, port.ErrExists) || errors.Is(err, port.ErrNotFound) {
			continue
		}
		return err
	}
	return ErrBusy
}

// editItem is [Base.editRaw] over an item: cur is nil when the key is absent,
// and a nil next deletes it.
//
// It also looks after the credentials the items name. A next that names a
// credential its cur did not is the writer's own (fresh), written before the
// item. When the write lands, the credential it replaced is dropped; when it
// does not (an error, or nothing to write), every fresh one is, so no
// attempt leaves a credential behind and none ever removes one a landed item
// still names.
func (b *Base) editItem(ctx context.Context, key string, ttl time.Duration, change func(cur *item) (*item, error)) error {
	var prev, landed string
	var kept bool
	fresh := map[string]bool{}
	err := b.editRaw(ctx, key, ttl, func(raw []byte, exists bool) ([]byte, bool, error) {
		var cur *item
		if exists {
			var err error
			if cur, err = decodeItem(raw); err != nil {
				cur = nil // an unreadable item is replaced, as an unreadable entry is
			}
		}
		prev, landed, kept = "", "", false
		if cur != nil {
			prev = cur.Secret
		}
		next, err := change(cur)
		if err != nil || next == nil {
			kept = errors.Is(err, errKeep)
			return nil, false, err
		}
		if landed = next.Secret; landed != "" && landed != prev {
			fresh[landed] = true
		}
		return encodeItem(next), true, nil
	})
	if err != nil || kept {
		// Nothing landed: what this call wrote ahead of the item is unused.
		b.dropSecrets(ctx, key, keys(fresh)...)
		return err
	}
	// The item landed (or went): the credential it replaced is unreachable now,
	// and so is every fresh one that lost a retry.
	for ref := range fresh {
		if ref != landed {
			b.dropSecrets(ctx, key, ref)
		}
	}
	if prev != landed {
		b.dropSecrets(ctx, key, prev)
	}
	return nil
}

// keys lists a set.
func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// getItem reads one item.
func (b *Base) getItem(ctx context.Context, key string) (*item, error) {
	rec, err := b.State.Get(ctx, key)
	if errors.Is(err, port.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeItem(rec.Value)
}

// listAll reads every live record under a prefix, a page at a time.
func (b *Base) listAll(ctx context.Context, prefix string) ([]port.Record, error) {
	var out []port.Record
	page := ""
	for {
		p, err := b.State.List(ctx, prefix, page, 0)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Records...)
		if p.Next == "" {
			return out, nil
		}
		page = p.Next
	}
}

// seg writes one name as a key segment: letters, digits, '-' and '_' stay,
// every other byte (a dot above all, which would end the segment) is ~XX.
func seg(s string) string {
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out.WriteByte(c)
		default:
			out.WriteByte('~')
			out.WriteByte(hex[c>>4])
			out.WriteByte(hex[c&15])
		}
	}
	return out.String()
}

// unseg reverses [seg].
func unseg(s string) string {
	if !strings.Contains(s, "~") {
		return s
	}
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '~' && i+2 < len(s) {
			var v byte
			if _, err := fmt.Sscanf(s[i+1:i+3], "%02X", &v); err == nil {
				out.WriteByte(v)
				i += 2
				continue
			}
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

// digest is a hash of the key and revision of every live record under the
// prefixes: what a poller compares from one look to the next to see that
// something changed, without reading or opening a value.
func (b *Base) digest(ctx context.Context, prefixes ...string) ([sha256.Size]byte, error) {
	h := sha256.New()
	for _, prefix := range prefixes {
		records, err := b.listAll(ctx, prefix)
		if err != nil {
			return [sha256.Size]byte{}, err
		}
		for _, rec := range records {
			h.Write([]byte(rec.Key + "\x00" + string(rec.Revision) + "\x00"))
		}
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}
