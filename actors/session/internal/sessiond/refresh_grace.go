package sessiond

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	sessionv2alpha1 "github.com/truvity/sluis/v2/gen/go/sluis/session/v2alpha1"
	typev2alpha1 "github.com/truvity/sluis/v2/gen/go/sluis/type/v2alpha1"
	"github.com/truvity/sluis/v2/kits/go/audit"
	"github.com/truvity/sluis/v2/kits/go/store"
)

// refreshGrace is how long after a rotation the token it replaced is still answered, with the same successor. A
// client whose response was lost, or a second replica that read the chain before the first wrote it, presents the old
// token again within moments (REQ-SESS-013). It is the 30 seconds of v1, and a constant: the window is a property of
// the protocol's retry, not something an installation tunes.
const refreshGrace = 30 * time.Second

// labelRefreshTag separates the keyed hash that makes a refresh token's tag from every other use of the boundary's mac
// key. It is inside the prefix that key@ reserves to session (sluis-refresh-).
const labelRefreshTag = "sluis-refresh-tag-v1"

// refreshSaltLen is the size of the salt of a chain: 256 random bits.
const refreshSaltLen = 32

// refreshTagLen is how much of the keyed hash is the tag of a token: 192 bits, and with the 8 bytes of the generation
// the 32 bytes of a secret, 43 characters as before.
const refreshTagLen = 24

// refreshSecret is the secret half of the refresh token of a generation of a chain: the generation (8 bytes, big
// endian) and its tag, 43 characters of base64url. The tag is a keyed hash (HMAC-SHA384 at key@, under the boundary's mac
// key) of the tenant, the sid, the generation and the salt of the chain, cut to 192 bits. A token is a function of the
// chain and its generation, so every replica derives the same one (the grace window of REQ-SESS-013 answers a retry with
// the successor it would have had), and the chain holds the hash of the current token, the generation and the salt, never
// a token and never a list of spent ones: a token of any older generation is recognised by making its tag again
// (REQ-SESS-014). Nobody without key@ and the salt can make one: the salt is sealed under the tenant's chain key, so a
// copy of the store is no help, and a holder of a token cannot make another. The secret of a token never reaches key@.
func (k keys) refreshSecret(ctx context.Context, tenant, sid string, generation uint64, salt []byte) (string, error) {
	g := binary.BigEndian.AppendUint64(nil, generation)
	m, err := k.mac(ctx, "mac", "", labelRefreshTag, lengthPrefixed(tenant, sid, string(g), string(salt)), nil)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(append(g, m[:refreshTagLen]...)), nil
}

// refreshSkew is how far ahead of this clock another replica's stamp may be and still count as recent: the time a
// request takes, and the difference between the clocks of replicas that keep time (a few seconds). A stamp further
// ahead is not a recent rotation, it is a clock that is wrong, and the window is not opened for it.
const refreshSkew = 5 * time.Second

// inGrace says a rotation made at rotatedAt is recent enough for its predecessor to be answered again: not more than
// refreshGrace ago, and not more than refreshSkew ahead of now. A stamp from the future (a replica whose clock runs
// ahead, or this one running behind) is not a recent rotation: it must not open the window for as long as the clocks
// differ. It only fails to open it: the chain is not ended for the stamp's sake, and a current token still rotates.
func inGrace(now time.Time, rotatedAt *timestamppb.Timestamp) bool {
	d := now.Sub(rotatedAt.AsTime())
	return d >= -refreshSkew && d <= refreshGrace
}

// isSuccessor says the chain's current token is next: the presented token was the one the last rotation replaced.
func isSuccessor(chain *sessionv2alpha1.SessionChain, next string) bool {
	return hashEqual(secretHash(next), chain.GetRefreshHash())
}

// afterLostRace answers a rotation whose conditional write found the chain moved: another presentation got there first.
// It reads the chain again. If the chain now holds the successor of this very token, the other request was this
// token's twin, made moments ago, and this one gets the same answer (the chain is as it should be, and nothing is
// written). Anything else (the chain is gone, or moved on by more than this token) is not a retry: a chain that is gone
// is refused as an unknown token, and one that has moved on ends, because a spent token came back.
//
// It returns the chain to answer from, or the response to give.
func (s *Service) afterLostRace(ctx context.Context, tc *tokenCtx, sid, next string, person *audit.Subject, v *verdict) (*sessionv2alpha1.SessionChain, *connect.Response[typev2alpha1.HttpReply], error) {
	it, err := s.chains.Get(ctx, tc.tenant, sid)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Ended by whoever won: they recorded the end. This is an unknown token now.
		return nil, s.refuse(ctx, tc, person, v, reasonRefresh, errInvalidGrant()), nil
	case err != nil:
		return nil, nil, unavailable("store", err)
	}
	// The clock is read again, not taken from the start of the request: a request that stalled for seconds in key@ or in
	// policy while its retry won has not found a rotation from the future, it has found a recent one.
	if isSuccessor(it.Value, next) && inGrace(s.now(), it.Value.GetRefreshedAt()) {
		return it.Value, nil, nil
	}
	if derr := s.deleteChain(ctx, tc, sid, v.grant, reasonReuse); derr != nil {
		return nil, nil, derr
	}
	return nil, s.refuse(ctx, tc, person, v, reasonReuse, errInvalidGrant()), nil
}

// refreshFacts are the members of the record of a refresh that was granted. replay is present, and true, only when the
// token was a spent one answered again within the window (or lost the race to its own twin): a replay that looks like
// a normal refresh is weak evidence of theft, and the window is also a theft window.
func refreshFacts(sid string, replay bool) map[string]any {
	m := map[string]any{"grant_type": "refresh_token", "sid": sid}
	if replay {
		m["replay"] = true
	}
	return m
}
