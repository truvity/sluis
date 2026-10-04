package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/port"
)

// issuerPrefix is where the issuer keeps everything it holds about a login: the
// requests and codes in flight, the sessions and the index of whose they are,
// the refresh tokens and the markers of the spent ones, the browser SSO, the
// minted tokens' records and the signing keys' schedule. They are the keys the
// issuer has always used (internal/issuer), and every adapter carries them under
// the same names, so a copy keeps each key as it is.
const issuerPrefix = "issuer:"

// shownKey is how a report names a key. The issuer's keys carry bearer values
// (a code, a session id, a person's address), so a report names them by kind and
// a short hash, which is enough to find one on either side and safe to keep.
func shownKey(domain, id string) string {
	if domain != DomainIssuer {
		return id
	}
	kind, rest := id, ""
	// issuer:<kind>:<rest>, and the index and entry forms of the keyring
	// (issuer:keyring:entry:<alg>:<kid>) keep their first two segments.
	if parts := strings.SplitN(id, ":", 3); len(parts) == 3 {
		kind, rest = parts[0]+":"+parts[1], parts[2]
	}
	if rest == "" {
		return kind
	}
	sum := sha256.Sum256([]byte(rest))
	return kind + ":#" + hex.EncodeToString(sum[:4])
}

// keyringPrefix is the key ring's schedule: the entries, the index and the
// tombstones of retired keys. Copying them is what keeps the old key's public
// half published through its overlap, so that a token issued before the move
// keeps verifying.
const keyringPrefix = "issuer:keyring:"

// kmsPrefix is what a KMS-signed installation records about its own key
// (`issuer:kms:state-secret-fingerprint`). It is never copied: the new
// installation's state secret differs, and a fingerprint of the old one would
// stop it from starting.
const kmsPrefix = "issuer:kms:"

func issuerSteps(sessions bool) []step {
	prefix, name := keyringPrefix, "keyring"
	if sessions {
		prefix, name = issuerPrefix, "state"
	}
	steps := []step{{
		domain: DomainIssuer, name: name, lifetimes: true,
		read: func(ctx context.Context, k kit) ([]entry, error) {
			ex, ok := k.ports.State.(port.StateExporter)
			if !ok {
				return nil, fmt.Errorf("%w: this State cannot say how long a record has left", port.ErrUnsupported)
			}
			var out []entry
			err := ex.ExportState(ctx, prefix, func(x port.Exported) error {
				if strings.HasPrefix(x.Key, kmsPrefix) {
					return nil
				}
				out = append(out, entry{id: x.Key, canon: x.Value, ttl: x.TTL})
				return nil
			})
			return out, err
		},
		check: func(e entry) string {
			if e.ttl <= 0 {
				// The issuer writes a lifetime on everything; a record with
				// none would sit on the destination for good.
				return "the record has no lifetime, and the issuer's state always has one"
			}
			return ""
		},
		write: func(ctx context.Context, k kit, e entry, _ *entry) error {
			_, err := k.ports.State.Put(ctx, e.id, e.canon, e.ttl)
			return err
		},
	}}
	if !sessions {
		return steps
	}
	return append(steps, step{
		domain: DomainIssuer, name: "index", lifetimes: true,
		read: func(ctx context.Context, k kit) ([]entry, error) {
			ex, ok := k.ports.Index.(port.IndexExporter)
			if !ok {
				return nil, fmt.Errorf("%w: this Index cannot list its sets", port.ErrUnsupported)
			}
			var out []entry
			err := ex.ExportIndex(ctx, issuerPrefix, func(x port.Exported) error {
				members := slices.Clone(x.Members)
				slices.Sort(members)
				raw, _ := json.Marshal(members)
				out = append(out, entry{id: x.Key, canon: raw, ttl: x.TTL, val: members})
				return nil
			})
			return out, err
		},
		write: func(ctx context.Context, k kit, e entry, old *entry) error {
			members := e.val.([]string)
			// A set that differs is made equal: what the destination has
			// beyond the source's is taken out.
			if old != nil {
				if have, ok := old.val.([]string); ok {
					for _, m := range have {
						if !slices.Contains(members, m) {
							if err := k.ports.Index.Remove(ctx, e.id, m); err != nil {
								return err
							}
						}
					}
				}
			}
			for _, m := range members {
				if err := k.ports.Index.Add(ctx, e.id, m, ttlOrDay(e.ttl)); err != nil {
					return err
				}
			}
			return nil
		},
	})
}

// ttlOrDay is the lifetime a set is written with: the source's, or, for a set
// that had none, a day, since an Index set must expire (a set with no lifetime
// would outlive the sessions it indexes).
func ttlOrDay(ttl time.Duration) time.Duration {
	if ttl > 0 {
		return ttl
	}
	return 24 * time.Hour
}
