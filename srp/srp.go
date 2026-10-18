// Package srp is SRP-6a, the password-authenticated key exchange of RFC 2945
// as RFC 5054 fixes its arithmetic, over the 3072-bit group of RFC 5054
// Appendix A and SHA-256 (docs/decisions/0076).
//
// The server stores a salt and a verifier and never the password. A client
// proves it knows the password without sending it, and the server proves it
// holds the verifier. A guess is one run of the protocol against the live
// server: the transcript of a failed run lets nobody test passwords offline,
// which a stolen verifier does (the verifier is a password hash with a fast
// function, so the password it stands for must be machine-generated).
//
// Three things differ from RFC 5054 and are spelled out in the decision:
// the hash is SHA-256 where the RFC says SHA-1; the session key and the two
// proofs are RFC 2945's with fixed-width group elements; and the private key x
// is stretched with PBKDF2 ([Iterations] rounds, which the server tells the
// client with the salt). [Params] takes the hash, the group and the rounds
// (none is the RFC's own x), so the RFC's test vectors (SHA-1, the 1024-bit
// group) run through the same code the 3072-bit SHA-256 default does.
//
// Every group element crosses the wire as lower-case hexadecimal of its
// fixed-width big-endian form (384 bytes for the default group).
package srp

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
)

// Errors. A caller maps every one of them to one refusal: the party on the
// other end is not told which check failed.
var (
	// ErrBadPublic is a public value (A or B) that is zero modulo N, which would
	// fix the shared secret to zero whatever the password is.
	ErrBadPublic = errors.New("srp: invalid public value")
	// ErrBadProof is a proof (M1 or M2) that does not match.
	ErrBadProof = errors.New("srp: proof does not match")
)

// PrivateBytes is the size of the ephemeral private values a and b: 256 bits,
// the floor RFC 5054 asks for.
const PrivateBytes = 32

// Iterations is the number of PBKDF2-HMAC-SHA256 rounds the default parameters
// stretch the password with (OWASP's figure for SHA-256, 2023). A credential
// records the number it was made with, and the server hands it to the client with
// the salt, so it can be raised later without a flag day.
const Iterations = 600_000

// The bounds a client accepts for the number a server names: a server cannot
// turn the stretching off, and cannot make the page spin for minutes.
const (
	MinIterations = 100_000
	MaxIterations = 10_000_000
)

// Params is a group, a hash and the stretching of x.
type Params struct {
	n, g       *big.Int
	h          func() hash.Hash
	size       int // the width of a group element in bytes: |N|
	iterations int // PBKDF2 rounds; zero is RFC 5054's x, unstretched
}

// New returns the parameters for the prime n, the generator g and the hash h.
func New(n, g *big.Int, h func() hash.Hash) Params {
	return Params{n: new(big.Int).Set(n), g: new(big.Int).Set(g), h: h, size: (n.BitLen() + 7) / 8}
}

// WithIterations returns the parameters stretching x with PBKDF2 for n rounds.
func (p Params) WithIterations(n int) Params {
	p.iterations = n
	return p
}

// Iterations is the number of rounds new credentials are made with.
func (p Params) Iterations() int { return p.iterations }

// Default is what sluis uses: the 3072-bit group of RFC 5054 with SHA-256, and x
// stretched with [Iterations] rounds.
var Default = func() Params {
	n, ok := new(big.Int).SetString(group3072N, 16)
	if !ok {
		panic("srp: the group constant does not parse")
	}
	return New(n, big.NewInt(group3072G), sha256.New).WithIterations(Iterations)
}()

// Modulus is N, in its fixed-width form.
func (p Params) Modulus() []byte { return p.pad(p.n) }

// Size is the width, in bytes, of a group element on the wire.
func (p Params) Size() int { return p.size }

// pad is PAD() of RFC 5054: the big-endian form of x, left-padded with zeros
// to the width of N.
func (p Params) pad(x *big.Int) []byte { return x.FillBytes(make([]byte, p.size)) }

func (p Params) hash(parts ...[]byte) []byte {
	h := p.h()
	for _, part := range parts {
		h.Write(part)
	}
	return h.Sum(nil)
}

func (p Params) hashInt(parts ...[]byte) *big.Int { return new(big.Int).SetBytes(p.hash(parts...)) }

// k is the multiplier of SRP-6a, H(N | PAD(g)) (RFC 5054 section 2.5.3).
func (p Params) k() *big.Int { return p.hashInt(p.pad(p.n), p.pad(p.g)) }

// u is the scrambling parameter, H(PAD(A) | PAD(B)) (RFC 5054 section 2.6).
func (p Params) u(a, b *big.Int) *big.Int { return p.hashInt(p.pad(a), p.pad(b)) }

// x is the private key derived from the password. RFC 5054 section 2.6 has
// H(s | H(I | ":" | P)), the username part of it; with rounds > 0 the inner hash is
// stretched, H(s | PBKDF2(H(I | ":" | P), s, rounds)), so that a stolen verifier
// costs rounds hashes a guess and not one.
func (p Params) x(salt []byte, username, password string, rounds int) (*big.Int, error) {
	inner := p.hash([]byte(username), []byte(":"), []byte(password))
	if rounds > 0 {
		stretched, err := pbkdf2.Key(p.h, string(inner), salt, rounds, len(inner))
		if err != nil {
			return nil, err
		}
		inner = stretched
	}
	return p.hashInt(salt, inner), nil
}

// Verifier is g^x mod N, in its fixed-width form: what a server stores beside
// the salt in place of the password. The salt is random and has nothing to do
// with the password: the server hands it to anyone who asks.
func (p Params) Verifier(salt []byte, username, password string) []byte {
	x, err := p.x(salt, username, password, p.iterations)
	if err != nil {
		panic("srp: " + err.Error()) // only an unusable hash or a rounds count below zero
	}
	return p.pad(new(big.Int).Exp(p.g, x, p.n))
}

// NewSalt returns a random salt.
func NewSalt() []byte {
	salt := make([]byte, 32)
	_, _ = rand.Read(salt) // crypto/rand.Read does not fail: it stops the program if the system source is broken
	return salt
}

// valid reports whether v is a usable public value: not zero modulo N.
func (p Params) valid(v *big.Int) bool {
	return v.Sign() > 0 && new(big.Int).Mod(v, p.n).Sign() != 0
}

// private returns a random exponent of [PrivateBytes] bytes that is not zero.
func private(random io.Reader) (*big.Int, error) {
	buf := make([]byte, PrivateBytes)
	for {
		if _, err := io.ReadFull(random, buf); err != nil {
			return nil, fmt.Errorf("srp: no randomness: %w", err)
		}
		if v := new(big.Int).SetBytes(buf); v.Sign() != 0 {
			return v, nil
		}
	}
}

// key is K = H(PAD(S)), the session key both sides end with.
func (p Params) key(s *big.Int) []byte { return p.hash(p.pad(s)) }

// m1 is the client's proof, RFC 2945's
// H(H(N) xor H(g) | H(I) | s | A | B | K), with N, g, A and B in their
// fixed-width form.
func (p Params) m1(username string, salt []byte, a, b *big.Int, key []byte) []byte {
	hn, hg := p.hash(p.pad(p.n)), p.hash(p.pad(p.g))
	for i := range hn {
		hn[i] ^= hg[i]
	}
	return p.hash(hn, p.hash([]byte(username)), salt, p.pad(a), p.pad(b), key)
}

// m2 is the server's proof, RFC 2945's H(A | M1 | K).
func (p Params) m2(a *big.Int, m1, key []byte) []byte { return p.hash(p.pad(a), m1, key) }

// Server is the server's half of one run. What has to outlive the first
// request is the private value b ([Server.Private]); [Params.ResumeServer]
// rebuilds the rest, so a run can continue on another process.
type Server struct {
	p        Params
	username string
	salt     []byte
	v        *big.Int
	b, bPub  *big.Int
}

// NewServer starts a run for a stored salt and verifier.
func (p Params) NewServer(username string, salt, verifier []byte, random io.Reader) (*Server, error) {
	b, err := private(random)
	if err != nil {
		return nil, err
	}
	return p.resume(username, salt, verifier, b), nil
}

// ResumeServer continues a run whose private value was kept.
func (p Params) ResumeServer(username string, salt, verifier, private []byte) *Server {
	return p.resume(username, salt, verifier, new(big.Int).SetBytes(private))
}

func (p Params) resume(username string, salt, verifier []byte, b *big.Int) *Server {
	v := new(big.Int).SetBytes(verifier)
	// B = k*v + g^b mod N
	pub := new(big.Int).Exp(p.g, b, p.n)
	pub.Add(pub, new(big.Int).Mul(p.k(), v))
	pub.Mod(pub, p.n)
	return &Server{p: p, username: username, salt: salt, v: v, b: b, bPub: pub}
}

// Private is b, the value to keep between the two requests of a run.
func (s *Server) Private() []byte { return s.b.FillBytes(make([]byte, PrivateBytes)) }

// Public is B, to send with the salt.
func (s *Server) Public() []byte { return s.p.pad(s.bPub) }

// Verify checks the client's A and proof M1. It returns the server's proof M2
// and the session key, or an error: [ErrBadPublic] for A that is zero modulo N
// (checked first, before anything is computed from it) and [ErrBadProof] for an
// M1 that does not match. The comparison takes the same time whatever the
// first differing byte.
func (s *Server) Verify(a, m1 []byte) (m2, key []byte, err error) {
	p := s.p
	aInt := new(big.Int).SetBytes(a)
	if len(a) > p.size || aInt.Cmp(p.n) >= 0 || !p.valid(aInt) {
		return nil, nil, ErrBadPublic
	}
	u := p.u(aInt, s.bPub)
	if u.Sign() == 0 {
		return nil, nil, ErrBadPublic
	}
	// S = (A * v^u)^b mod N
	base := new(big.Int).Exp(s.v, u, p.n)
	base.Mul(base, aInt)
	base.Mod(base, p.n)
	secret := base.Exp(base, s.b, p.n)
	key = p.key(secret)
	want := p.m1(s.username, s.salt, aInt, s.bPub, key)
	if subtle.ConstantTimeCompare(want, m1) != 1 {
		return nil, nil, ErrBadProof
	}
	return p.m2(aInt, m1, key), key, nil
}

// Decoy returns the salt and verifier (with the default rounds) the server answers with for a username
// it has no account for: both are derived from a secret the server keeps and
// the username, so they are the same on every request, on every process that
// has the secret, and look like a real account's. A run against them ends in
// [ErrBadProof], as a wrong password does.
func (p Params) Decoy(secret []byte, username string) (salt, verifier []byte) {
	mac := func(label string) []byte {
		m := hmac.New(sha256.New, secret)
		m.Write([]byte(label))
		m.Write([]byte{0})
		m.Write([]byte(username))
		return m.Sum(nil)
	}
	salt = mac("salt")
	x := new(big.Int).SetBytes(mac("verifier"))
	return salt, p.pad(new(big.Int).Exp(p.g, x, p.n))
}

// Client is the client's half of one run. sluis's own client is the browser's
// script; this one is the reference the script is held to, and what
// `sluisctl` and the tests use.
type Client struct {
	p                  Params
	username, password string
	a, aPub            *big.Int
	m1, key            []byte
	bPub               *big.Int
}

// NewClient starts a run for a username and password.
func (p Params) NewClient(username, password string, random io.Reader) (*Client, error) {
	a, err := private(random)
	if err != nil {
		return nil, err
	}
	return p.ResumeClient(username, password, a.FillBytes(make([]byte, PrivateBytes))), nil
}

// ResumeClient is a client with a chosen private value: for the test vectors.
func (p Params) ResumeClient(username, password string, private []byte) *Client {
	a := new(big.Int).SetBytes(private)
	return &Client{p: p, username: username, password: password, a: a, aPub: new(big.Int).Exp(p.g, a, p.n)}
}

// Public is A, to send first.
func (c *Client) Public() []byte { return c.p.pad(c.aPub) }

// secret is S = (B - k*g^x)^(a + u*x) mod N, and u.
func (c *Client) secret(salt []byte, bInt *big.Int, rounds int) (s, u *big.Int, err error) {
	p := c.p
	if bInt.Cmp(p.n) >= 0 || !p.valid(bInt) {
		return nil, nil, ErrBadPublic
	}
	u = p.u(c.aPub, bInt)
	if u.Sign() == 0 {
		return nil, nil, ErrBadPublic
	}
	x, err := p.x(salt, c.username, c.password, rounds)
	if err != nil {
		return nil, nil, err
	}
	base := new(big.Int).Exp(p.g, x, p.n)
	base.Mul(base, p.k())
	base.Sub(bInt, base)
	base.Mod(base, p.n)
	exp := new(big.Int).Mul(u, x)
	exp.Add(exp, c.a)
	return base.Exp(base, exp, p.n), u, nil
}

// Respond takes the server's salt, B and the number of PBKDF2 rounds it names,
// and returns the proof M1. A server that names fewer than [MinIterations] (but
// not none, for parameters that stretch nothing) or more than [MaxIterations] is
// refused.
func (c *Client) Respond(salt, b []byte, rounds int) ([]byte, error) {
	if c.p.iterations > 0 && (rounds < MinIterations || rounds > MaxIterations) {
		return nil, fmt.Errorf("srp: the server names %d rounds, outside %d..%d", rounds, MinIterations, MaxIterations)
	}
	bInt := new(big.Int).SetBytes(b)
	secret, _, err := c.secret(salt, bInt, rounds)
	if err != nil {
		return nil, err
	}
	c.key = c.p.key(secret)
	c.bPub = bInt
	c.m1 = c.p.m1(c.username, salt, c.aPub, bInt, c.key)
	return c.m1, nil
}

// Key is the session key, after [Client.Respond].
func (c *Client) Key() []byte { return c.key }

// VerifyServer checks the server's proof M2.
func (c *Client) VerifyServer(m2 []byte) error {
	if c.m1 == nil || subtle.ConstantTimeCompare(c.p.m2(c.aPub, c.m1, c.key), m2) != 1 {
		return ErrBadProof
	}
	return nil
}
