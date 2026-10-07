package issuer

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// How says what produced a session, because "revoke Ada's kubectl login"
// is a different act from "revoke the console she left open", and an
// operator can only tell them apart if the issuer remembers which was
// which.
type How string

// The ways a session begins.
const (
	// HowCode is a browser sign-in: OIDC code + PKCE.
	HowCode How = "code"
	// HowDevice is a device-code flow: a CLI on a machine with no browser.
	HowDevice How = "device"
	// HowExchange is a workload or a CI job trading a token it already had.
	HowExchange How = "exchange"
)

// Session is one refresh token, described. The token itself is not here:
// this is the index that makes sessions listable and revocable, which is
// what turns "the issuer holds some state" into something an operator can
// act on and a person can audit.
//
// A session belongs to one identity and one client. That pairing is the
// unit of revocation: ending someone's ArgoCD session must not end their
// kubectl session, or an operator dealing with one incident would cut off
// work they never meant to touch.
type Session struct {
	ID       string `json:"id"`
	Identity string `json:"identity"`
	ClientID string `json:"client_id"`
	How      How    `json:"how"`
	// Method is how the person was proved at the sign-in this session was
	// opened from: a provider's kind, or [RecoveryHow]. It decides how the
	// subject is evaluated at every refresh -- a recovery sign-in by the
	// policy's ServiceAccount matchers, anybody else by the directory --
	// so that what a subject merely LOOKS like decides nothing. Empty for
	// an exchange, and for a session recorded before the field existed,
	// which is evaluated as a person.
	Method string `json:"method,omitempty"`
	// Scopes are what this session was granted at sign-in. A session
	// recorded before this field existed has none, and a refresh on one
	// of those is answered with the scopes it asks for or with none —
	// which is what the code did for every session until now.
	Scopes []string `json:"scopes,omitempty"`
	// Resource is what this session's tokens are FOR, when the sign-in
	// named one (RFC 8707). Empty means the client itself, which is every
	// session recorded before resources existed and every client that
	// names none.
	//
	// It lives here because a refresh an hour from now carries a token and
	// nothing else: without it the renewed token would be minted for the
	// CLIENT while the original was minted for the resource, silently
	// changing its audience mid-session -- and the resource's own gate
	// would go unchecked for the rest of the session's life.
	Resource string `json:"resource,omitempty"`
	// SSO is the browser session this one was opened from, empty for a
	// flow with no browser (an exchange, a device code redeemed by a
	// CLI). It is what makes *sign out everywhere* one operation on the
	// parent rather than a loop the console has to get right.
	SSO string `json:"sso,omitempty"`
	// AuthTime is when the person authenticated, carried down from the
	// SSO session so that a refresh an hour from now mints the same
	// `auth_time` and a fresh `iat` — without a second store read on the
	// hottest path.
	AuthTime      time.Time `json:"auth_time,omitempty"`
	IssuedAt      time.Time `json:"issued_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	LastRefreshed time.Time `json:"last_refreshed,omitempty"`
	// Involved says the session's client is recorded among the clients of
	// the sign-in it was opened under (see [SSO.Involve]), so a refresh
	// need not record it again. False for a session under no sign-in, and
	// for one recorded before the field existed, whose next refresh
	// records it.
	Involved bool `json:"involved,omitempty"`
	// IndexedUntil is when the session's membership of the index sets
	// written last runs out (see [Sessions.indexed]). Zero for a session
	// recorded before it existed, which is re-added at its next refresh.
	IndexedUntil time.Time `json:"indexed_until,omitempty"`
}

// serviceAccount reports whether the session's subject was proved AS a
// ServiceAccount, and so is evaluated by the policy's matchers rather
// than asked of the directory: a recovery sign-in, by the method its
// sign-in recorded, or an exchange, whose subject this issuer minted
// itself from the verified proof (a cluster's ServiceAccount token reads
// as one; a person's address never does, whatever else it contains, and
// neither does a GitHub job). A browser sign-in
// through an identity provider is a person, whatever its subject looks
// like.
func (s Session) serviceAccount() bool {
	switch {
	case s.Method == RecoveryHow:
		return true
	case s.How == HowExchange:
		// A person can open an exchange's session too, trading their own
		// sign-in ([Storage.signInProof]), and their address is theirs to
		// choose: `k8s:ns:x@corp.example` parses as a ServiceAccount. Every
		// subject an exchange mints for a ServiceAccount (`k8s:<ns>:<name>`,
		// `<cluster>:k8s:<ns>:<name>`, `system:serviceaccount:...`) has no
		// `@`, and every person's has one.
		if strings.Contains(s.Identity, "@") {
			return false
		}
		_, ok := serviceAccountSubject(s.Identity)
		return ok
	default:
		return false
	}
}

// Live reports whether the session is still usable at now. An expired
// session stays in the index until it is swept, so that "it expired" and
// "it never existed" remain different answers.
func (s Session) Live(now time.Time) bool { return now.Before(s.ExpiresAt) }

// Sessions is the per-identity index beside the refresh tokens.
//
// It lives in the SHARED state, not in the process. A browser signs in at
// one replica and refreshes at another; an operator's listing hits
// whichever the gateway picked. An index in memory would answer with the
// sessions that one pod happened to record — a list that is not wrong so
// much as arbitrary, and a revocation that reports success while the
// session goes on working at the pod next door. For a control whose whole
// job is to end access, that is the worst possible failure, so the index
// is shared or it is not worth having.
//
// Four kinds of key, and expiry belongs to exactly one of them. The
// session record `session:<id>` carries the TTL; the sets that make it
// findable hold ids and no lifetime of their own. A listing that meets an
// id whose record is gone drops it from the set as it goes, so the sets
// repair themselves and the record's expiry stays the single answer to
// "is this session still alive".
type Sessions struct {
	state    State
	now      func() time.Time
	newID    func() string
	lifetime time.Duration
	// absolute is the global timeout: no per-client session outlives
	// auth_time by more than this, no matter how often it is refreshed.
	// Zero or negative means no such limit -- the deployment's own choice,
	// distinct from "not configured", which [Config.withDefaults] never
	// produces (see [DefaultAbsoluteLifetime]) but a caller that builds a
	// [Sessions] directly, as every existing test does, still can.
	absolute time.Duration
	// resolve narrows or lengthens absolute for the resources a session
	// has been used for. Nil means every session gets absolute, which is
	// what a caller that never declares a resource wants and what every
	// existing test builds.
	resolve func(touched []string) time.Duration
}

// NewSessions returns the index over a shared store. lifetime is how long
// a refresh token lives when nothing shorter applies; absolute is the
// global timeout measured from auth_time, or zero for none.
func NewSessions(state State, lifetime, absolute time.Duration) *Sessions {
	return &Sessions{
		state:    state,
		now:      time.Now,
		newID:    uuid.NewString,
		lifetime: lifetime,
		absolute: absolute,
	}
}

// SetAbsoluteResolver sets how a session's absolute limit is decided from
// the resources it has been used for, in place of the one global limit.
// The issuer sets it from the policy, so a read-only resource that
// declares a longer limit can have one ([policy.EffectiveAbsolute]).
func (s *Sessions) SetAbsoluteResolver(resolve func(touched []string) time.Duration) {
	s.resolve = resolve
}

// absoluteOf is the absolute limit of a chain that has touched these
// resources. A chain is bound to the resource it was opened for -- a
// refresh never changes it -- so this is a set of one today; it is a set
// because the rule is stated over everything a chain has been used for.
func (s *Sessions) absoluteOf(touched ...string) time.Duration {
	if s.resolve == nil {
		return s.absolute
	}

	return s.resolve(touched)
}

// pastLimit reports whether a session has outlived the absolute limit its
// resources allow as the policy stands NOW. A record's own ExpiresAt was
// decided when it was last written; if the policy has since withdrawn an
// extension, the chain must end at its next refresh rather than keep the
// longer end it was given.
func (s *Sessions) pastLimit(session Session) bool {
	absolute := s.absoluteOf(session.Resource)
	if session.AuthTime.IsZero() || absolute <= 0 {
		return false
	}

	return !s.now().Before(session.AuthTime.Add(absolute))
}

// SetClock replaces the clock, for tests.
func (s *Sessions) SetClock(now func() time.Time) { s.now = now }

// SetIDs replaces the id source, for tests.
func (s *Sessions) SetIDs(newID func() string) { s.newID = newID }

// The keys. A refresh token is a bearer secret, so it is HASHED into its
// key rather than written into the keyspace: an index that can be read
// must not be an index that can be replayed.
func sessionKey(id string) string          { return "issuer:session:" + id }
func sessionOfKey(identity string) string  { return "issuer:sessions-of:" + strings.ToLower(identity) }
func sessionForKey(clientID string) string { return "issuer:sessions-for:" + clientID }
func sessionTokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "issuer:session-token:" + hex.EncodeToString(sum[:])
}

// sessionRotatedKey is where the version before this one recorded what a
// spent refresh token became, for [refreshGrace] after it was spent. A
// rotation now records that in the spent token's own pointer (see
// [spentPrefix]); this key is still READ, on the path a spent or unknown
// token takes, so that a token rotated by the previous version during a
// rollout is still answered inside its grace window. It can go one
// release after this one.
func sessionRotatedKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "issuer:session-rotated:" + hex.EncodeToString(sum[:])
}

// capEnd is a session's end: the sliding refresh window from now, or the
// absolute session limit from auth_time, whichever comes first.
//
// A session with no auth_time is a workload or a machine exchange, which
// authenticated nobody -- there is no "since sign-in" to measure the limit
// from, so only the refresh window applies, exactly as before this limit
// existed. That is the whole of how [Sessions.Record] and
// [Sessions.Refreshed] leave a machine's session alone: this is the one
// place either of them decides an end, and here it simply has nothing to
// cap against.
func capEnd(now, authTime time.Time, refresh, absolute time.Duration) time.Time {
	end := now.Add(refresh)
	if authTime.IsZero() || absolute <= 0 {
		return end
	}
	if limit := authTime.Add(absolute); limit.Before(end) {
		return limit
	}
	return end
}

// spentPrefix marks a token pointer whose token has been spent. The value
// is the prefix, then when it was spent, the successor the spending
// refresh produced -- sealed under the spent token -- and the session it
// belonged to:
//
//	spent:<unix milliseconds>:<sealed successor>:<session id>
//
// It is the spent token's own key rather than one beside it because a
// rotation has to do two things to that token at once -- stop it naming
// the session, and say what it became -- and doing both in one write is
// one write fewer on every refresh.
//
// The mark is kept until the family's absolute deadline
// ([Sessions.spentLifetime]), not for the grace window alone, so that a spent token presented after the
// window is still KNOWN to be spent, and to be whose: that is how a reuse
// is told apart from a token that never existed ([spentMark.inGrace]).
// Keeping it longer is the TTL of the write the rotation already makes,
// so it costs no request.
//
// The successor is a bearer secret, and it is the session's live one
// until the next refresh. Held in plain for as long as the mark is kept,
// anybody who can read the keyspace could use it; the token pointers hash
// their tokens into their keys precisely so that an index that can be
// read is not one that can be replayed. So it is sealed with a key only
// the spent token's holder can derive ([sealSuccessor]): the replay that
// needs it presents that token, and nothing else does. A value that names
// no session id where a pointer does can never be read as one.
const spentPrefix = "spent:"

// spentMark is a spent token's pointer, read.
type spentMark struct {
	at      time.Time
	sealed  string
	session string
}

// isSpent reports whether a token pointer's value is the mark of a spent
// token, well formed or not.
func isSpent(raw []byte) bool { return strings.HasPrefix(string(raw), spentPrefix) }

// readSpent reads a token pointer's value as a spent token's mark. It is
// false for a live token's pointer and for a mark it cannot read, which
// is then a token that resolves to nothing.
func readSpent(raw []byte) (spentMark, bool) {
	rest, spent := strings.CutPrefix(string(raw), spentPrefix)
	if !spent {
		return spentMark{}, false
	}

	parts := strings.SplitN(rest, ":", 3)
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return spentMark{}, false
	}

	ms, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return spentMark{}, false
	}

	return spentMark{at: time.UnixMilli(ms), sealed: parts[1], session: parts[2]}, true
}

// markSpent is the value a rotation writes over the token it spends.
func markSpent(token, successor, session string, at time.Time) ([]byte, error) {
	mark := spentMark{at: time.UnixMilli(at.UnixMilli()), session: session}

	sealed, err := sealSuccessor(token, successor, mark.bound())
	if err != nil {
		return nil, err
	}

	return []byte(spentPrefix + strconv.FormatInt(mark.at.UnixMilli(), 10) + ":" + sealed + ":" + session), nil
}

// bound is what the sealed successor is bound to: the rest of the mark, so
// that a successor cannot be moved into another session's mark or another
// time.
func (m spentMark) bound() string {
	return strconv.FormatInt(m.at.UnixMilli(), 10) + ":" + m.session
}

// inGrace reports whether a spent token presented at now is a replay of
// the refresh that spent it, inside [refreshGrace]. A mark from a clock a
// little ahead of this one reads as inside it.
func (m spentMark) inGrace(now time.Time) bool { return now.Sub(m.at) < refreshGrace }

// reusedAt reports whether a spent token presented at now is a reuse that
// ends its session: past the grace window by more than the clock
// tolerance, so that a replica whose clock runs a little ahead of the one
// that spent the token cannot turn a client's own burst of refreshes,
// spread across replicas, into a revocation.
func (m spentMark) reusedAt(now time.Time) bool {
	return now.Sub(m.at) > refreshGrace+markAheadTolerance
}

// successor opens the successor sealed in the mark, with the spent token
// that was presented. False when it does not open, which a mark written
// for this token always does.
func (m spentMark) successor(token string) (string, bool) {
	sealed, err := base64.RawURLEncoding.DecodeString(m.sealed)
	if err != nil {
		return "", false
	}

	aead, err := successorCipher(token)
	if err != nil || len(sealed) < aead.NonceSize() {
		return "", false
	}

	nonce, box := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, box, []byte(m.bound()))
	if err != nil {
		return "", false
	}

	return string(plain), true
}

// sealSuccessor seals a successor under the token it replaces, bound to
// the rest of the mark, as base64url of nonce and ciphertext.
func sealSuccessor(token, successor, bound string) (string, error) {
	aead, err := successorCipher(token)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", fmt.Errorf("issuer: seal a successor: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(successor), []byte(bound))), nil
}

// successorCipher is AES-256-GCM under a key derived from a refresh token
// with a label of its own, so that it is never the hash the token's
// pointer is keyed by.
func successorCipher(token string) (cipher.AEAD, error) {
	key := sha256.Sum256([]byte("sluis/issuer/spent-successor/v1\x00" + token))

	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("issuer: successor cipher: %w", err)
	}

	return cipher.NewGCM(block)
}

// spentLifetime is how long a rotation keeps the mark of the token it
// spent: until the absolute deadline of the refresh family the token
// belongs to -- auth_time plus the absolute limit its resource allows, the
// same limit [capEnd] caps every end at -- and never less than the grace
// window. A session that keeps refreshing slides its own end forward, so
// a mark kept only until the end the rotation set would lapse while the
// family it could end was still alive; kept until the deadline, it lasts
// exactly as long as there is a family to end, and never longer.
//
// A session with no such deadline (a workload's exchange, which nobody
// authenticated, or a deployment with no absolute limit) keeps it until
// the end the rotation set: one refresh lifetime, as long as the spent
// token itself could have lived unspent.
func (s *Sessions) spentLifetime(session Session, now time.Time) time.Duration {
	end := session.ExpiresAt
	if absolute := s.absoluteOf(session.Resource); !session.AuthTime.IsZero() && absolute > 0 {
		end = session.AuthTime.Add(absolute)
	}

	return max(end.Sub(now), refreshGrace)
}

// refreshGrace is how long a refresh token that has just been rotated
// still answers, with the successor it produced. It is a property of the
// protocol rather than of an estate, so it is not configurable: long
// enough to cover one page's burst of concurrent calls, short enough that
// a stolen token is worth nothing by the time anyone could use it.
//
// Past it, presenting the spent token is a reuse, and it ends the session
// ([Storage.refuseReuse]).
const refreshGrace = 30 * time.Second

// sessionAllKey is every session, for the operator's "what is open right
// now". It is a set of ids and nothing more; the records it points at are
// what expire.
const sessionAllKey = "issuer:sessions"

// Opened is one session about to be recorded. It is a struct rather than
// a row of arguments because the last three are easy to swap by mistake
// and impossible to notice afterwards: a session filed under the wrong
// client is one an operator cannot find and cannot end.
type Opened struct {
	Identity string
	ClientID string
	// Resource is what the tokens are for, when the request named one.
	Resource string
	How      How
	// Method is how the person was proved; see [Session.Method].
	Method string
	// Token is the refresh token; it is hashed into its key, never stored.
	Token string
	// Scopes are what was consented to.
	Scopes []string
	// SSO is the browser session this was opened from, if any.
	SSO string
	// AuthTime is when the person authenticated; zero for a flow where
	// nobody did.
	AuthTime time.Time
	// Involved says the client is already recorded among SSO's clients.
	Involved bool
}

// Record files a newly issued refresh token and returns the session it
// created.
//
// The scopes are the ones the person consented to, and the session is
// the only place they survive: a refresh arrives an hour later carrying
// a token and nothing else, and what a relying party may ask for then is
// what it was granted at sign-in, not what it asks for now.
func (s *Sessions) Record(ctx context.Context, o Opened) (Session, error) {
	now := s.now()
	session := Session{
		ID:       s.newID(),
		Identity: strings.ToLower(o.Identity),
		ClientID: o.ClientID,
		Resource: o.Resource,
		How:      o.How,
		Method:   o.Method,
		Scopes:   o.Scopes,
		SSO:      o.SSO,
		AuthTime: o.AuthTime,
		Involved: o.Involved,
		IssuedAt: now,
		// The absolute limit applies from the moment the session is
		// OPENED, not only from its first refresh: a browser session
		// carried down from hours-old SSO (a new client added to an
		// existing sign-in) must not get a fresh 24 hours of its own.
		ExpiresAt:    capEnd(now, o.AuthTime, s.lifetime, s.absoluteOf(o.Resource)),
		IndexedUntil: now.Add(s.indexLifetime()),
	}

	// The record before the sets: a listing that met the id in a set
	// before its record existed would drop it as expired.
	if err := s.put(ctx, session); err != nil {
		return Session{}, err
	}

	if err := s.state.Set(ctx, sessionTokenKey(o.Token), []byte(session.ID), s.lifetime); err != nil {
		return Session{}, err
	}

	if err := s.index(ctx, session); err != nil {
		return Session{}, err
	}

	return session, nil
}

// indexLifetime is how long one Add keeps a session in the index sets:
// twice the refresh lifetime, so that a session refreshed within one
// lifetime of its last Add is still covered at its new end and needs no
// Add of its own (see [Sessions.indexed]). Every Add of these sets uses
// it, which is what keeps an engine whose expiry is the whole set's
// (memory, Valkey) from ever shortening a member's.
func (s *Sessions) indexLifetime() time.Duration { return 2 * s.lifetime }

// indexed reports whether a session's membership of the index sets, as
// its record says it was last written, is far enough from running out
// that adding it again at now would be a write that changes nothing
// anybody can see.
//
// Far enough is a full refresh lifetime past now. That covers the end the
// session is about to be given (never later than now plus one lifetime),
// and it bounds how long a membership that was LOST -- evicted by an
// engine short of memory, or dropped by a listing that read the record as
// expired while a refresh was rotating it -- stays lost: a session that
// keeps refreshing is added again within one lifetime of its last Add, so
// a revocation by person, client or everything finds it again. A record
// with no IndexedUntil (written before it existed) is added at once.
//
// With the default lifetimes (a 12-hour refresh window inside a 24-hour
// absolute limit) that is one Add per twelve hours of refreshing rather
// than one per refresh. A set holding an ended session's id a while
// longer than before is the cost; the listing that meets it drops it, as
// it always has.
func (s *Sessions) indexed(session Session, now time.Time) bool {
	return !session.IndexedUntil.Before(now.Add(s.lifetime)) && !session.IndexedUntil.Before(session.ExpiresAt)
}

// index adds a session to the sets that make it findable.
func (s *Sessions) index(ctx context.Context, session Session) error {
	for _, key := range []string{sessionAllKey, sessionOfKey(session.Identity), sessionForKey(session.ClientID)} {
		if err := s.state.Add(ctx, key, session.ID, s.indexLifetime()); err != nil {
			return err
		}
	}

	return nil
}

// presented is a refresh token as one refresh found it: the session it
// names, and the revisions of what was read, so that the rotation that
// follows writes over exactly that and reads none of it again.
type presented struct {
	token   string
	session Session
	// successor is set when the token was spent moments ago and this is a
	// replay of the refresh that spent it, inside the grace window: the
	// token that refresh produced, which is the answer.
	successor string
	// reused is set, with the answer false, when the token was spent
	// longer ago than [refreshGrace] in a session that is still live: its
	// id, and session is its record -- the one a reuse ends, once the
	// client presenting the token has been matched to the session's own
	// ([Storage.endReuse]).
	reused string
	// pointer and record are the revisions of the token's pointer and of
	// the session record as read, "" from a State that keeps none.
	pointer, record string
}

// present resolves a refresh token for a refresh: to its live session, or
// -- for a token rotated within [refreshGrace] -- to the successor that
// rotation produced. Its answer is what [Sessions.rotate] acts on, so a
// refresh reads the token and the session once.
//
// A token spent longer ago than that resolves to nothing, and says which
// session it was spent in ([presented.reused]), so that the caller can end
// it. The read that tells is the one every refresh makes anyway.
func (s *Sessions) present(ctx context.Context, token string) (presented, bool, error) {
	raw, pointer, found, err := getVersion(ctx, s.state, sessionTokenKey(token))
	if err != nil {
		return presented{}, false, err
	}

	if !found {
		// Spent by the version before this one, which recorded the
		// successor under a key of its own.
		raw, found, err = s.state.Get(ctx, sessionRotatedKey(token))
		if err != nil || !found {
			return presented{}, false, err
		}

		return s.replayedTo(ctx, token, string(raw))
	}

	if isSpent(raw) {
		mark, ok := readSpent(raw)
		if !ok {
			return presented{}, false, nil
		}

		// The seal is opened on both paths, not only the one that needs the
		// successor. It is bound to the mark's time and session id, so a
		// mark whose time or session was edited does not open, and ends
		// nothing. It is no defence against somebody who can write the
		// store, who could delete the session outright anyway; it keeps a
		// mark from being read as anything other than what its rotation
		// wrote.
		successor, ok := mark.successor(token)
		if !ok {
			return presented{}, false, nil
		}

		now := s.now()
		if ahead := mark.at.Sub(now); ahead > markAheadTolerance {
			// Spent by a replica whose clock runs ahead of this one's. It
			// is still read as inside the window, which is the safe side;
			// the skew is what an operator needs to see.
			recordMarkAhead(ctx)
			slog.WarnContext(ctx, "a spent refresh token's mark is dated ahead of this replica's clock; "+
				"the replicas' clocks disagree", "ahead", ahead.String())
		}

		switch {
		case mark.inGrace(now):
			return s.replayedTo(ctx, token, successor)
		case mark.reusedAt(now):
			return s.reusedIn(ctx, token, mark.session)
		default:
			// Between the grace window and the tolerance past it: refused,
			// and nothing ended, in case this replica's clock is the one
			// that runs ahead.
			return presented{}, false, nil
		}
	}

	session, record, live, err := s.byIDVersion(ctx, string(raw))
	if err != nil || !live {
		return presented{}, false, err
	}

	return presented{token: token, session: session, pointer: pointer, record: record}, true, nil
}

// markAheadTolerance is how far the replicas' clocks may disagree before
// it matters: a spent mark dated further ahead of this replica's clock than
// this is reported as skew, and a reuse is acted on only this far past the
// grace window ([spentMark.reusedAt]). The replicas are expected to keep
// their clocks with NTP; the tolerance is for its residual error, not a
// substitute for it.
const markAheadTolerance = 2 * time.Second

// reusedIn answers a token spent longer ago than the grace window with the
// session it was spent in, when that is still live: one read, of the
// record, whose revision is kept so that ending it is conditional on it
// ([Sessions.endReused]). A session already ended, by a revocation, its
// expiry or an earlier reuse of the same token, leaves nothing to end, and
// the token is then simply refused.
func (s *Sessions) reusedIn(ctx context.Context, token, id string) (presented, bool, error) {
	session, record, live, err := s.byIDVersion(ctx, id)
	if err != nil || !live {
		return presented{}, false, err
	}

	return presented{token: token, session: session, reused: session.ID, record: record}, false, nil
}

// reuseEndAttempts bounds how many conditional deletes ending a reused
// session makes, each at the revision just read, before it ends the
// session unconditionally.
const reuseEndAttempts = 3

// endReused ends the session a reuse was detected in, and reports whether
// this call is the one that ended it: the one that then audits it and
// announces it. Of several presentations of one spent token at once,
// exactly one is.
//
// The record goes first, and conditionally, at the revision read: that one
// write ends the session, since every token resolves through it. What
// happens when it does not land depends on why:
//
//   - The record was written since (errMoved). A refresh of the live
//     successor does that, and a thief who holds the successor and keeps
//     refreshing it must not dodge the revocation that way. The record is
//     read again and, while it is still live, deleted at its new revision,
//     [reuseEndAttempts] times in all; a record that keeps moving is then
//     deleted unconditionally, and this call is still the one that ended
//     it. A rotation that loses to the delete is refused by its own
//     conditional write ([Sessions.writeRotated]).
//   - The record is gone (errGone), or read again it is gone or no longer
//     live. Another presentation of the same token ended it -- or this
//     request's own delete did, and a retry of it below the State reported
//     it as missing. The two cannot be told apart. The session is ended
//     either way, and this call reports that it did not end it: a second
//     audit record would be worse than a missing one. It says so in a
//     warning naming the session.
//   - The delete failed otherwise (an unavailable store, a timeout). It may
//     have landed. The record is read again: gone, it is the case above;
//     still live, the error is returned and the client's retry meets the
//     reuse again.
//
// The three index sets follow the record; a failure there leaves ids the
// next listing drops, and the session ended. Four writes when nothing
// races it; each refresh it races adds one failed conditional delete and
// one read.
func (s *Sessions) endReused(ctx context.Context, p presented) (bool, error) {
	id, version := p.session.ID, p.record

	for attempt := 0; ; attempt++ {
		var err error
		if attempt < reuseEndAttempts {
			err = deleteVersion(ctx, s.state, sessionKey(id), version)
		} else {
			err = s.state.Delete(ctx, sessionKey(id))
		}

		if err == nil {
			break
		}

		if !errors.Is(err, errMoved) && !errors.Is(err, errGone) {
			// Ambiguous: it may have landed. Read again before saying so.
			_, _, live, readErr := s.byIDVersion(ctx, id)
			if readErr != nil || live {
				return false, err
			}

			err = errGone
		}

		if errors.Is(err, errMoved) {
			var live bool
			_, version, live, err = s.byIDVersion(ctx, id)
			if err != nil {
				return false, err
			}

			if live {
				continue
			}
		}

		slog.WarnContext(ctx, "a reused session was already gone when it was to be ended; "+
			"another presentation of the token, or this one's own retried delete, ended it, "+
			"and it is not audited a second time", "session", id)

		return false, nil
	}

	for _, key := range []string{sessionAllKey, sessionOfKey(p.session.Identity), sessionForKey(p.session.ClientID)} {
		if err := s.state.Remove(ctx, key, id); err != nil {
			return true, err
		}
	}

	return true, nil
}

// replayedTo answers a token that was rotated moments ago with the
// successor that rotation produced, so the loser of a race ends up
// holding exactly what the winner holds.
//
// A browser does not refresh once. A page that opens with several calls
// at once, behind a gateway that refreshes PER REQUEST rather than per
// session, presents one spent token several times within the same second
// -- and across replicas, so no in-process lock would see it. Refusing
// those is refusing the legitimate holder: the gateway treats the
// refusal as a dead session and sends the person back to sign in,
// intermittently and for no reason they can see.
//
// The window is deliberately short and does not mint anything. Outside
// it, reuse is refused and ends the session, which is the detection this
// rotation exists for; inside it, the replay is answered with the one
// credential already in flight rather than a second one.
//
// The successor must still resolve to a live session. It may not: the
// session can have been ended, or refreshed again, between the rotation
// and this replay -- and an ended session must not be handed back a
// working credential.
func (s *Sessions) replayedTo(ctx context.Context, token, successor string) (presented, bool, error) {
	session, live, err := s.ByToken(ctx, successor)
	if err != nil || !live {
		return presented{}, false, err
	}

	return presented{token: token, session: session, successor: successor}, true, nil
}

// Refreshed moves a session onto a new token, which is what a refresh
// does: the old token is spent and must stop working, and the session it
// belongs to carries on with its identity, its client and its history.
// It reports whether the old token named a live session.
//
// A replay of a refresh that has just happened is handed the SAME
// successor the winner got, not a second live credential; any other spent
// token is refused.
func (s *Sessions) Refreshed(ctx context.Context, oldToken, newToken string) (Session, string, bool, error) {
	p, found, err := s.present(ctx, oldToken)
	if err != nil || !found {
		return Session{}, "", false, err
	}

	return s.rotate(ctx, p, newToken)
}

// rotate is [Sessions.Refreshed] for a token [Sessions.present] has
// already resolved: three writes, each over what was read or new.
//
//  1. The new token's pointer. First, so that by the time anything says
//     what the old token became, what it became already resolves.
//  2. The old token's pointer, replaced by the mark of a spent token and
//     its successor ([spentPrefix]), kept until the family's absolute
//     deadline so that a later reuse is known for one -- only if it is
//     still what was read. If another
//     refresh spent it first this one is a replay and gets that one's
//     successor; if it was revoked or has expired, this one is refused.
//  3. The session record, with its new end -- only over what is there
//     ([Sessions.writeRotated]). A session ended while this refresh ran
//     stays ended; the refresh is refused rather than writing the record
//     back.
//
// The index sets are written between the last two only when the
// session's membership is within a refresh lifetime of running out
// ([Sessions.indexed]).
func (s *Sessions) rotate(ctx context.Context, p presented, newToken string) (Session, string, bool, error) {
	if p.successor != "" {
		return p.session, p.successor, true, nil
	}

	session := p.session
	now := s.now()
	session.LastRefreshed = now
	// Capped exactly as at open: a sliding refresher plateaus at
	// auth_time+absolute rather than sliding forever, because every
	// rotation recomputes the SAME limit from the SAME auth_time.
	session.ExpiresAt = capEnd(now, session.AuthTime, s.lifetime, s.absoluteOf(session.Resource))

	// The policy may have withdrawn the extension this chain was opened
	// under. The token is spent and the chain ends here, as it would at
	// any other limit, rather than being written back already expired.
	if !session.ExpiresAt.After(now) {
		if err := s.state.Delete(ctx, sessionTokenKey(p.token)); err != nil {
			return Session{}, "", false, err
		}

		if err := s.deleteSession(ctx, session); err != nil {
			return Session{}, "", false, err
		}

		return Session{}, "", false, nil
	}

	mark, err := markSpent(p.token, newToken, session.ID, now)
	if err != nil {
		return Session{}, "", false, err
	}

	if err = s.state.Set(ctx, sessionTokenKey(newToken), []byte(session.ID), s.lifetime); err != nil {
		return Session{}, "", false, err
	}

	err = replace(ctx, s.state, sessionTokenKey(p.token), mark, s.spentLifetime(session, now), p.pointer)
	if errors.Is(err, errMoved) || errors.Is(err, errGone) {
		// Lost: the new token was never handed out, so its pointer goes.
		s.discard(ctx, newToken)

		if errors.Is(err, errGone) {
			// Revoked, expired -- or spent by a replica of the version
			// before this one, which deletes the pointer and records the
			// successor under a key of its own. That last is a replay
			// during a rollout, answered as [Sessions.present] answers it.
			raw, found, err := s.state.Get(ctx, sessionRotatedKey(p.token))
			if err != nil || !found {
				return Session{}, "", false, err
			}

			again, live, err := s.replayedTo(ctx, p.token, string(raw))
			if err != nil || !live {
				return Session{}, "", false, err
			}

			return again.session, again.successor, true, nil
		}

		// Another refresh of this same token got there first. This is
		// its replay, and the answer is its successor.
		again, found, err := s.present(ctx, p.token)
		if err != nil || !found || again.successor == "" {
			return Session{}, "", false, err
		}

		return again.session, again.successor, true, nil
	}

	if err != nil {
		return Session{}, "", false, err
	}

	// The sets carry the session forward when its membership is close to
	// running out ([Sessions.indexed]) -- before the record is written, so that a
	// record never claims a membership that a failed Add left unwritten.
	// The record still holds the id while they are written, so a listing
	// in between finds it rather than dropping it.
	if !s.indexed(session, now) {
		session.IndexedUntil = now.Add(s.indexLifetime())
		if err = s.index(ctx, session); err != nil {
			return Session{}, "", false, err
		}
	}

	written, live, err := s.writeRotated(ctx, session, p.record)
	if err != nil {
		return Session{}, "", false, err
	}

	if !live {
		// Ended while this refresh ran. It stays ended.
		s.discard(ctx, newToken)

		return Session{}, "", false, nil
	}

	return written, newToken, true, nil
}

// rotatedAttempts bounds how many times a rotation re-reads a session
// record that keeps moving under it before it gives up with an error.
const rotatedAttempts = 3

// writeRotated writes a rotated session's record over the revision read,
// and reports false when the session turned out to have ended.
//
// A record written by something else since it was read is read again and
// this rotation's part -- its new end (never later than the end there
// now), when it refreshed, its index and sign-in membership -- applied to
// what is there now, never written blind
// over it: the something else may have been a revocation, and a plain
// write would bring the revoked session back. A record that is gone, or
// no longer live, when read again ends the rotation as a revoked one.
func (s *Sessions) writeRotated(ctx context.Context, session Session, version string) (Session, bool, error) {
	for range rotatedAttempts {
		raw, err := json.Marshal(session)
		if err != nil {
			return Session{}, false, fmt.Errorf("issuer: encode session %s: %w", session.ID, err)
		}

		err = replace(ctx, s.state, sessionKey(session.ID), raw, s.lifetime, version)
		switch {
		case err == nil:
			return session, true, nil
		case errors.Is(err, errGone):
			return Session{}, false, nil
		case !errors.Is(err, errMoved):
			return Session{}, false, err
		}

		fresh, freshVersion, live, err := s.byIDVersion(ctx, session.ID)
		if err != nil || !live {
			return Session{}, false, err
		}

		fresh.LastRefreshed = session.LastRefreshed
		// Never later than what is there now: whatever wrote the record
		// since may have shortened it, and a refresh must not undo that.
		if session.ExpiresAt.Before(fresh.ExpiresAt) {
			fresh.ExpiresAt = session.ExpiresAt
		}
		if fresh.IndexedUntil.Before(session.IndexedUntil) {
			fresh.IndexedUntil = session.IndexedUntil
		}
		fresh.Involved = fresh.Involved || session.Involved
		session, version = fresh, freshVersion
	}

	return Session{}, false, fmt.Errorf("issuer: session %s kept changing while it was refreshed", session.ID)
}

// discard removes the pointer of a token a rotation minted and then did
// not hand out. Best effort: the token was never disclosed, so a pointer
// left behind grants nothing to anyone, and it expires on its own.
func (s *Sessions) discard(ctx context.Context, token string) {
	if err := s.state.Delete(ctx, sessionTokenKey(token)); err != nil {
		slog.WarnContext(ctx, "the issuer could not remove an unused refresh token's pointer", "error", err)
	}
}

// ByRefreshToken resolves a refresh token to its session, including one
// that was rotated within the grace window.
//
// The library reads the token before it asks for new tokens, so a replay
// that [Sessions.Refreshed] would have answered must resolve here too or
// it is refused before the rotation ever runs.
//
// Only the refresh path uses this. [ByToken] stays exact, because the
// other callers -- revocation, listing -- are asking whether this token
// is the live one, which is a different question.
func (s *Sessions) ByRefreshToken(ctx context.Context, token string) (Session, bool, error) {
	p, ok, err := s.present(ctx, token)

	return p.session, ok, err
}

// ByToken resolves a live refresh token to its session. A spent one,
// inside its grace window or not, names none.
func (s *Sessions) ByToken(ctx context.Context, token string) (Session, bool, error) {
	raw, found, err := s.state.Get(ctx, sessionTokenKey(token))
	if err != nil || !found {
		return Session{}, false, err
	}

	if isSpent(raw) {
		return Session{}, false, nil
	}

	return s.byID(ctx, string(raw))
}

// Query narrows a listing. The zero value lists every live session, which
// is what an operator looking for what is open right now asks for.
type Query struct {
	Identity string
	ClientID string
	// SSO narrows to the sessions one BROWSER opened. Ending those is
	// only half of signing that browser out — the sign-in itself has to
	// go with them, which [SessionsService.RevokeSessions] does.
	SSO string
	// Contains reads Identity and ClientID as SUBSTRINGS -- prefix,
	// suffix and middle, one rule -- rather than as exact values.
	//
	// It is a LISTING affordance and deliberately not a revoking one:
	// [Sessions.Revoke] takes the same Query, and a revoke whose scope
	// is "anything containing this" is the control that ends more than
	// its caller meant. Revoke refuses it.
	Contains bool
}

// set is the narrowest set that can answer this query. Asking for one
// person's sessions must not read every session in the installation.
func (q Query) set() string {
	switch {
	// A substring cannot know which per-identity index holds a match, so
	// there is no narrower set than all of them. This is the cost of the
	// affordance, and it is the read the unfiltered listing already does.
	case q.Contains:
		return sessionAllKey
	case q.Identity != "":
		return sessionOfKey(q.Identity)
	case q.ClientID != "":
		return sessionForKey(q.ClientID)
	default:
		return sessionAllKey
	}
}

// List returns the live sessions a query selects, newest first.
func (s *Sessions) List(ctx context.Context, q Query) ([]Session, error) {
	sessions, err := s.collect(ctx, q)
	if err != nil {
		return nil, err
	}

	slices.SortFunc(sessions, func(a, b Session) int {
		if d := b.IssuedAt.Compare(a.IssuedAt); d != 0 {
			return d
		}

		return strings.Compare(a.ID, b.ID)
	})

	return sessions, nil
}

// Revoke ends every session a query selects and returns how many. It is
// the only write the console has against the issuer, and it can only ever
// remove: an empty query would end everything, so a caller that means
// "this person" must say so.
func (s *Sessions) Revoke(ctx context.Context, q Query) (int, error) {
	// Listing by substring is a convenience; revoking by one is a way to
	// end far more than was meant. "kar" would take kargo and karma with
	// it, and there is no undo.
	if q.Contains {
		return 0, errors.New("a substring names sessions to LIST, never sessions to end")
	}

	sessions, err := s.collect(ctx, q)
	if err != nil {
		return 0, err
	}

	ended := 0

	for i := range sessions {
		gone, err := s.RevokeID(ctx, sessions[i].ID)
		if err != nil {
			// Say how many actually ended. A revocation that stops
			// halfway must not report the number it hoped for.
			return ended, err
		}

		if gone {
			ended++
		}
	}

	return ended, nil
}

// Refile moves one identity's sessions from one browser sign-in to
// another, and reports how many it moved. Nothing is ended and nobody is
// told: the sessions carry on exactly as they were, filed under the
// sign-in that replaced the one they were opened under, so that ending
// the new one ends them too.
//
// It is what a person signing in again in the same browser -- a step-up,
// `prompt=login`, `max_age` -- does to what they opened: the old sign-in
// ends, and a session still filed under it would be one no sign-out could
// reach any more.
//
// Each record is written over the revision read, so a refresh or a
// revocation racing it is never undone: a session revoked meanwhile stays
// revoked, and a refresh that wrote first is read again. The record keeps
// the store lifetime it had (see [Sessions.put]), so that a session at its
// own end is still told apart from one cut at the absolute limit.
//
// A session that cannot be moved does not stop the others: each is tried,
// and the errors are returned together once all have been. A State that
// keeps no revisions moves nothing, since a plain write there could bring
// back a session revoked between the read and the write.
//
// One read of the identity's index set and one of each session in it, and
// one write per session moved.
func (s *Sessions) Refile(ctx context.Context, identity, from, to string) (int, error) {
	if from == "" || to == "" || from == to {
		return 0, nil
	}

	ids, err := s.state.Members(ctx, sessionOfKey(identity))
	if err != nil {
		return 0, err
	}

	identity = strings.ToLower(strings.TrimSpace(identity))
	moved := 0

	var errs []error

	for _, id := range ids {
		done, err := s.refile(ctx, id, identity, from, to)
		if err != nil {
			errs = append(errs, fmt.Errorf("session %s: %w", id, err))

			continue
		}

		if done {
			moved++
		}
	}

	return moved, errors.Join(errs...)
}

// refile moves one session, when it is still live and still filed under
// from, and reports whether it did.
func (s *Sessions) refile(ctx context.Context, id, identity, from, to string) (bool, error) {
	for range rotatedAttempts {
		session, version, live, err := s.byIDVersion(ctx, id)
		if err != nil || !live || session.Identity != identity || session.SSO != from || version == "" {
			return false, err
		}

		// The horizon the last write gave it: put and writeRotated keep a
		// record for one refresh lifetime from when they wrote it.
		written := session.IssuedAt
		if session.LastRefreshed.After(written) {
			written = session.LastRefreshed
		}

		ttl := written.Add(s.lifetime).Sub(s.now())
		if ttl <= 0 {
			return false, nil
		}

		session.SSO = to

		raw, err := json.Marshal(session)
		if err != nil {
			return false, fmt.Errorf("issuer: encode session %s: %w", session.ID, err)
		}

		err = replace(ctx, s.state, sessionKey(session.ID), raw, ttl, version)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, errGone):
			return false, nil
		case !errors.Is(err, errMoved):
			return false, err
		}
	}

	return false, fmt.Errorf("issuer: session %s kept changing while it was refiled", id)
}

// RevokeID ends one session by its id, which is what "sign this one out"
// on a person's page does. It reports whether there was one to end.
//
// The token index is NOT walked to find the token that pointed here:
// there is no way to go from a session id back to the hash of its token,
// which is the price of not keeping the secret in the keyspace. The
// pointer expires on its own, and it resolves through the record — which
// is gone — so it grants nothing in the meantime.
func (s *Sessions) RevokeID(ctx context.Context, id string) (bool, error) {
	session, found, err := s.byID(ctx, id)
	if err != nil || !found {
		return false, err
	}

	return true, s.deleteSession(ctx, session)
}

// deleteSession removes a session's record and its membership in every
// index set, given the record already in hand.
//
// It exists separately from RevokeID because RevokeID resolves its target
// through byID, and byID's whole job is to treat a session whose
// [Session.Live] has passed as though it were never there -- which is
// right for every ordinary caller, and wrong for the one case where a
// session is deliberately being cleaned up BECAUSE it is no longer live:
// [Sessions.endedByAbsoluteLimit] already has the record in hand
// precisely because it is not live, and asking RevokeID to end it would
// find "not found" and delete nothing.
func (s *Sessions) deleteSession(ctx context.Context, session Session) error {
	if err := s.state.Delete(ctx, sessionKey(session.ID)); err != nil {
		return err
	}

	for _, key := range []string{sessionAllKey, sessionOfKey(session.Identity), sessionForKey(session.ClientID)} {
		if err := s.state.Remove(ctx, key, session.ID); err != nil {
			return err
		}
	}

	return nil
}

// RevokeToken ends whichever session holds this refresh token. It is what
// RFC 7009 calls at the revocation endpoint, and what a proxy's sign-out
// reaches when it hands back the token it held.
func (s *Sessions) RevokeToken(ctx context.Context, token string) (bool, error) {
	raw, found, err := s.state.Get(ctx, sessionTokenKey(token))
	if err != nil || !found {
		return false, err
	}

	// A spent token holds no session of its own; what it became is
	// revoked through the session, as it always was.
	if isSpent(raw) {
		return false, nil
	}

	if err = s.state.Delete(ctx, sessionTokenKey(token)); err != nil {
		return false, err
	}

	return s.RevokeID(ctx, string(raw))
}

// Sweep drops the ids whose records have expired from every set it can
// reach, and returns how many. Expiry itself needs no sweeper — the
// record carries the TTL and a listing repairs what it walks — so this
// exists for the sets nobody has listed lately.
func (s *Sessions) Sweep(ctx context.Context) (int, error) {
	ids, err := s.state.Members(ctx, sessionAllKey)
	if err != nil {
		return 0, err
	}

	gone := 0

	for _, id := range ids {
		if _, found, err := s.byID(ctx, id); err != nil {
			return gone, err
		} else if !found {
			gone++
		}
	}

	return gone, nil
}

// collect reads a query's sessions, dropping ids whose records are gone
// from the set as it goes.
func (s *Sessions) collect(ctx context.Context, q Query) ([]Session, error) {
	key := q.set()

	ids, err := s.state.Members(ctx, key)
	if err != nil {
		return nil, err
	}

	identity := strings.ToLower(strings.TrimSpace(q.Identity))
	out := make([]Session, 0, len(ids))

	for _, id := range ids {
		session, found, err := s.byID(ctx, id)
		if err != nil {
			return nil, err
		}

		if !found {
			// Self-repair: the record expired, so the id is not a session
			// any more and the set should stop saying it is.
			if err = s.state.Remove(ctx, key, id); err != nil {
				return nil, err
			}

			continue
		}

		// The narrowest set still needs the other half of a two-part
		// query applied: "Ada's ArgoCD sessions" reads Ada's set and then
		// keeps the ArgoCD ones.
		switch {
		case identity != "" && !matches(session.Identity, identity, q.Contains):
		case q.ClientID != "" && !matches(session.ClientID, q.ClientID, q.Contains):
		case q.SSO != "" && session.SSO != q.SSO:
		default:
			out = append(out, session)
		}
	}

	return out, nil
}

// matches is the one place the two filter shapes differ. Both sides are
// already lowered by their callers, so this is a comparison and not a
// second normalisation.
func matches(have, want string, contains bool) bool {
	if contains {
		return strings.Contains(have, want)
	}

	return have == want
}

// ByID reads one session. Exported because a caller acting on a session
// id must be able to check whose it is BEFORE ending it: an id alone is
// otherwise enough to end somebody else's.
func (s *Sessions) ByID(ctx context.Context, id string) (Session, bool, error) {
	return s.byID(ctx, id)
}

// byID reads one record. An expired record is absent, which is what makes
// the TTL the whole of expiry.
func (s *Sessions) byID(ctx context.Context, id string) (Session, bool, error) {
	session, _, live, err := s.byIDVersion(ctx, id)
	return session, live, err
}

// byIDVersion is [Sessions.byID] with the revision of the record read.
func (s *Sessions) byIDVersion(ctx context.Context, id string) (Session, string, bool, error) {
	raw, version, found, err := getVersion(ctx, s.state, sessionKey(id))
	if err != nil || !found {
		return Session{}, "", false, err
	}

	var session Session
	if err = json.Unmarshal(raw, &session); err != nil {
		return Session{}, "", false, fmt.Errorf("issuer: %s is not readable: %w", sessionKey(id), err)
	}

	if !session.Live(s.now()) {
		return Session{}, "", false, nil
	}

	return session, version, true, nil
}

// put writes a record, refusing one that is already expired.
//
// It is kept in the store for s.lifetime from now -- the SAME horizon as
// the token pointer ([sessionTokenKey]), not derived from ExpiresAt (the
// index sets are kept longer: [Sessions.indexLifetime]). The two used to be the same duration always, because
// ExpiresAt was always exactly now+s.lifetime; now that the absolute
// session limit can cap ExpiresAt short of that, [Session.Live] is what
// decides whether the record still answers, and this is a different
// question. Keeping the record around a little past its own logical life
// is what lets [Sessions.endedByAbsoluteLimit] tell a refresh refused BY
// THE LIMIT apart from one refused because the session is simply gone --
// otherwise both look identical the moment the record disappears.
func (s *Sessions) put(ctx context.Context, session Session) error {
	if !session.ExpiresAt.After(s.now()) {
		return fmt.Errorf("issuer: session %s has already expired", session.ID)
	}

	return setJSON(ctx, s.state, sessionKey(session.ID), session, s.lifetime)
}

// endedByAbsoluteLimit resolves a refresh token to the session it named
// even past that session's own end, so a refresh refused BECAUSE of the
// absolute session limit can be told apart from one refused because the
// session is simply gone (an ordinary sliding-window timeout, or an
// explicit revoke, which deletes the record outright and is never found
// here) -- and given its own reason in the log and the audit record
// instead of the generic "not live" both would otherwise share.
//
// It answers true only when the record is still IN THE STORE (see [put])
// and no longer live. Given how [put] keeps it -- for s.lifetime from the
// write that set ExpiresAt, regardless of what ExpiresAt itself is -- a
// record found here with ExpiresAt already passed can only be one whose
// ExpiresAt was capped short of that horizon, which is exactly what the
// absolute limit does and nothing else does.
func (s *Sessions) endedByAbsoluteLimit(ctx context.Context, token string) (Session, bool, error) {
	raw, found, err := s.state.Get(ctx, sessionTokenKey(token))
	if err != nil || !found {
		return Session{}, false, err
	}

	if isSpent(raw) {
		return Session{}, false, nil
	}

	session, err := getJSON[Session](ctx, s.state, sessionKey(string(raw)))
	if err != nil || session == nil || session.Live(s.now()) {
		return Session{}, false, err
	}

	return *session, true, nil
}
