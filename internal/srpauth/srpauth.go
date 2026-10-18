// Package srpauth is the server side of the password sign-in that is not a
// password form: SRP-6a (package srp, docs/decisions/0076) as two requests,
// with the half of the exchange that has to outlive the first kept in the
// State, so that any container may answer the second.
//
// The package knows nothing about the issuer or the console. A door gives it
// where the request came from, a binding that ties the exchange to the browser
// that began it, and a lookup of the accounts that door offers; it answers
// with the account that proved itself, or a refusal that says nothing about
// why.
package srpauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/throttle"
	"github.com/truvity/sluis/srp"
)

// The two accounts there are. Nothing else signs in this way.
const (
	// RecoveryUser is the recovery account: the break-glass operator, behind the
	// recovery gate (docs/decisions/0075).
	RecoveryUser = "recovery"
	// ConformanceUser is the conformance harness's test subject.
	ConformanceUser = "conformance"
)

// ExchangeTTL is how long between the two requests of an exchange.
const ExchangeTTL = 2 * time.Minute

// maxUsername bounds what is looked at; a longer name is no account.
const maxUsername = 64

var (
	// ErrRefused is every way an exchange fails: an unknown handle or one
	// already used, another browser's, a username with no account, a wrong
	// proof. One error, so that nothing is told which.
	ErrRefused = errors.New("srpauth: not accepted")
	// ErrThrottled is a source that has spent its refusals for the window, or
	// every source together have.
	ErrThrottled = errors.New("srpauth: too many attempts")
	// ErrUnavailable is the State or a secret that could not be read. It is the
	// service's fault and not the caller's, and is never turned into a refusal.
	ErrUnavailable = errors.New("srpauth: unavailable")
)

// Lookup finds the credential of an account for one request. It reports false
// for a username that is no account, and for an account this request may not
// use: that is how the other account, the recovery gate and a client that is
// not a harness's are kept out without the answer differing.
type Lookup func(ctx context.Context, username string) (cred srp.Credential, ok bool, err error)

// Record is the half of an exchange the State keeps between its two requests.
type Record struct {
	// User is the username asked for, lower-cased.
	User string `json:"user"`
	// Private is the server's ephemeral value b.
	Private []byte `json:"b"`
	// Decoy says the username had no account to offer: the exchange runs on a
	// made-up credential and ends in a refusal.
	Decoy bool `json:"decoy,omitempty"`
	// Bind is a digest of what ties the exchange to the browser that began it.
	Bind []byte `json:"bind"`
}

// Store keeps the records of exchanges, each for one use.
type Store interface {
	// Put keeps a record under a handle for at most ttl.
	Put(ctx context.Context, handle string, rec Record, ttl time.Duration) error
	// Take returns the record and removes it, once, whichever container asks:
	// ErrRefused when there is none (absent, expired, or taken already).
	Take(ctx context.Context, handle string) (Record, error)
}

// Service runs the exchanges.
type Service struct {
	Params srp.Params
	Store  Store
	// DecoyKey is the secret the made-up credentials of unknown usernames are
	// derived from. Every container has the same, or an unknown username would
	// get a different salt each time it asked, and a known one would not.
	DecoyKey func() ([]byte, error)
	// Random is the source of randomness; nil is crypto/rand.
	Random io.Reader
	// Refusals counts the exchanges that failed, per source and overall, as the
	// recovery proofs always were counted; Starts counts the starts, which cost
	// a State write each and which anyone who can load the page can make.
	Refusals *throttle.Limiter
	Starts   *throttle.Limiter

	decoys decoyCache
}

// decoyCache keeps the made-up verifiers, so that an unknown username costs the
// server what a real one does (a verifier is one modular exponentiation to make).
// It is bounded: a caller who tries a million names fills a thousand places, and
// the rest are made each time.
type decoyCache struct {
	mu sync.Mutex
	m  map[string]decoyEntry
}

type decoyEntry struct{ salt, verifier []byte }

const decoyCacheSize = 1024

func (c *decoyCache) get(key string) (decoyEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	return e, ok
}

func (c *decoyCache) put(key string, e decoyEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]decoyEntry{}
	}
	if len(c.m) < decoyCacheSize {
		c.m[key] = e
	}
}

// decoy is the made-up salt and verifier of a username.
func (s *Service) decoy(username string) (salt, verifier []byte, err error) {
	key, err := s.DecoyKey()
	if err != nil {
		return nil, nil, err
	}
	// The cache is keyed by the key too: a rotated key is a new set of decoys.
	cacheKey := string(key) + "\x00" + username
	if e, ok := s.decoys.get(cacheKey); ok {
		return e.salt, e.verifier, nil
	}
	salt, verifier = s.Params.Decoy(key, username)
	s.decoys.put(cacheKey, decoyEntry{salt, verifier})
	return salt, verifier, nil
}

// NewStarts is the limiter of starts: thirty a source and six hundred in all,
// a minute.
func NewStarts(now func() time.Time) *throttle.Limiter {
	return throttle.New(30, 600, time.Minute, 0, now)
}

// Begun is the answer to a start.
type Begun struct {
	Handle string
	// Username is the username as the server spells it (lower-cased and trimmed),
	// which the client must use: it is part of x and of the proof.
	Username string
	Salt     []byte
	B        []byte
	// Iterations is the number of PBKDF2 rounds x is stretched with.
	Iterations int
}

func (s *Service) random() io.Reader {
	if s.Random != nil {
		return s.Random
	}
	return rand.Reader
}

func digest(bind string) []byte {
	sum := sha256.Sum256([]byte("sluis srp bind\x00" + bind))
	return sum[:]
}

// Normalize is the username as the accounts spell it.
func Normalize(username string) string { return strings.ToLower(strings.TrimSpace(username)) }

// Start begins an exchange for username and answers with the salt and B. bind
// ties the exchange to the browser; source is where the request came from.
//
// A username that is no account (to this request) gets a made-up salt and B,
// the same every time it asks.
func (s *Service) Start(ctx context.Context, source, bind, username string, lookup Lookup) (Begun, error) {
	if s.Starts != nil {
		if s.Starts.Blocked(source) {
			return Begun{}, ErrThrottled
		}
		s.Starts.Refused(source)
	}
	if s.Refusals != nil && s.Refusals.Blocked(source) {
		return Begun{}, ErrThrottled
	}
	username = Normalize(username)
	cred, ok, err := srp.Credential{}, false, error(nil)
	if username != "" && len(username) <= maxUsername {
		if cred, ok, err = lookup(ctx, username); err != nil {
			return Begun{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}
	if !ok {
		if cred.Salt, cred.Verifier, err = s.decoy(username); err != nil {
			return Begun{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		cred.Iterations = s.Params.Iterations()
	}
	server, err := s.Params.NewServer(username, cred.Salt, cred.Verifier, s.random())
	if err != nil {
		return Begun{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	raw := make([]byte, 16)
	if _, err = io.ReadFull(s.random(), raw); err != nil {
		return Begun{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	handle := base64.RawURLEncoding.EncodeToString(raw)
	rec := Record{User: username, Private: server.Private(), Decoy: !ok, Bind: digest(bind)}
	if err = s.Store.Put(ctx, handle, rec, ExchangeTTL); err != nil {
		return Begun{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return Begun{Handle: handle, Username: username, Salt: cred.Salt, B: server.Public(), Iterations: cred.Iterations}, nil
}

// Proved is an exchange that ended well.
type Proved struct {
	// User is the account that proved itself.
	User string
	// M2 is the server's proof, for the client to check.
	M2 []byte
}

// Finish ends an exchange with the client's A and proof M1. The record is
// spent before anything is checked, so a handle is good for one try whoever
// holds it, and a try from another browser spends it too.
func (s *Service) Finish(ctx context.Context, source, bind, handle string, a, m1 []byte, lookup Lookup) (Proved, error) {
	if s.Refusals != nil && s.Refusals.Blocked(source) {
		return Proved{}, ErrThrottled
	}
	// A refusal that ran the proof check counts against the source and against the
	// ceiling across all sources, as it always did. One that did no work (a handle
	// that is unknown, spent or expired) counts against the source only, so that
	// a crowd of sources cannot use up the ceiling with requests that cost nothing
	// and lock the operator out.
	refuse := func() (Proved, error) {
		if s.Refusals != nil {
			s.Refusals.Refused(source)
		}
		return Proved{}, ErrRefused
	}
	rec, err := s.Store.Take(ctx, handle)
	switch {
	case errors.Is(err, ErrRefused):
		if s.Refusals != nil {
			s.Refusals.RefusedBySource(source)
		}
		return Proved{}, ErrRefused
	case err != nil:
		return Proved{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	bound := subtle.ConstantTimeCompare(rec.Bind, digest(bind)) == 1

	cred, ok, err := lookup(ctx, rec.User)
	if err != nil {
		return Proved{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if !ok || rec.Decoy {
		var derr error
		if cred.Salt, cred.Verifier, derr = s.decoy(rec.User); derr != nil {
			return Proved{}, fmt.Errorf("%w: %w", ErrUnavailable, derr)
		}
	}
	// The proof is checked on every path that reaches here, so that an unknown
	// username, a wrong browser and a wrong password cost the same.
	server := s.Params.ResumeServer(rec.User, cred.Salt, cred.Verifier, rec.Private)
	m2, _, verr := server.Verify(a, m1)
	if verr != nil || !bound || !ok || rec.Decoy {
		return refuse()
	}
	if s.Refusals != nil {
		s.Refusals.Accepted(source)
	}
	return Proved{User: rec.User, M2: m2}, nil
}

// StateStore keeps exchanges in the State, under `srp.<handle>`.
type StateStore struct{ State port.State }

var _ Store = StateStore{}

func stateKey(handle string) string { return "srp." + handle }

// Put implements [Store].
func (s StateStore) Put(ctx context.Context, handle string, rec Record, ttl time.Duration) error {
	value, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = s.State.Create(ctx, stateKey(handle), value, ttl)
	return err
}

// Take implements [Store]: a read, then a delete that holds only if nobody
// else moved the record, so that of two containers asked at once one gets it.
func (s StateStore) Take(ctx context.Context, handle string) (Record, error) {
	if len(handle) != 22 || strings.ContainsAny(handle, "./ ") {
		return Record{}, ErrRefused
	}
	got, err := s.State.Get(ctx, stateKey(handle))
	if errors.Is(err, port.ErrNotFound) {
		return Record{}, ErrRefused
	}
	if err != nil {
		return Record{}, err
	}
	if err = s.State.DeleteIfRevision(ctx, stateKey(handle), got.Revision); err != nil {
		if errors.Is(err, port.ErrConflict) || errors.Is(err, port.ErrNotFound) {
			return Record{}, ErrRefused
		}
		return Record{}, err
	}
	var rec Record
	if err = json.Unmarshal(got.Value, &rec); err != nil {
		return Record{}, ErrRefused
	}
	return rec, nil
}
