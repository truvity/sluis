package srp

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
)

// Credential is an account as a server stores it: the salt, the verifier and
// the number of PBKDF2 rounds x was stretched with. It is not the password and
// cannot sign anybody in, but it lets whoever holds it test guesses offline (at
// [Iterations] hashes a guess), so it is kept as a secret. The salt is public:
// the server hands it to anyone who starts an exchange.
type Credential struct {
	Salt       []byte
	Verifier   []byte
	Iterations int
}

type credentialDocument struct {
	Salt       string `json:"salt"`
	Verifier   string `json:"verifier"`
	Iterations int    `json:"iterations,omitempty"`
}

// NewCredential derives the credential of a username and password under a
// salt. The salt MUST be random ([NewSalt]) and independent of the password: it
// is served to anybody who starts an exchange, so a salt derived from the
// password would let that anybody test guesses against it with no verifier at
// all. Keep the salt that goes with a password (a program that recomputes the
// credential on every run keeps its salt with the password it belongs to).
func (p Params) NewCredential(salt []byte, username, password string) Credential {
	return Credential{Salt: salt, Verifier: p.Verifier(salt, username, password), Iterations: p.iterations}
}

// Document is the credential as it is stored:
// {"salt":"<hex>","verifier":"<hex>","iterations":<n>}.
func (c Credential) Document() []byte {
	out, _ := json.Marshal(credentialDocument{
		Salt: hex.EncodeToString(c.Salt), Verifier: hex.EncodeToString(c.Verifier), Iterations: c.Iterations,
	})
	return out
}

// ParseCredential reads a stored credential and refuses one that is not for
// these parameters: a verifier of another width, a short salt, a verifier that is
// zero modulo N or not below it, or rounds outside [MinIterations, MaxIterations]
// (or none, for parameters that stretch nothing).
func (p Params) ParseCredential(data []byte) (Credential, error) {
	var doc credentialDocument
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return Credential{}, fmt.Errorf("srp: credential: %w", err)
	}
	salt, err := hex.DecodeString(doc.Salt)
	if err != nil || len(salt) < 16 {
		return Credential{}, errors.New("srp: credential: the salt is not at least 16 bytes of hexadecimal")
	}
	verifier, err := hex.DecodeString(doc.Verifier)
	if err != nil || len(verifier) != p.size {
		return Credential{}, fmt.Errorf("srp: credential: the verifier is not %d bytes of hexadecimal", p.size)
	}
	v := new(big.Int).SetBytes(verifier)
	if !p.valid(v) || v.Cmp(p.n) >= 0 {
		return Credential{}, errors.New("srp: credential: the verifier is zero or not below N")
	}
	switch {
	case p.iterations == 0 && doc.Iterations != 0:
		return Credential{}, errors.New("srp: credential: these parameters stretch nothing, and the credential names rounds")
	case p.iterations > 0 && (doc.Iterations < MinIterations || doc.Iterations > MaxIterations):
		return Credential{}, fmt.Errorf("srp: credential: %d rounds, outside %d..%d", doc.Iterations, MinIterations, MaxIterations)
	}
	return Credential{Salt: salt, Verifier: verifier, Iterations: doc.Iterations}, nil
}

// MinPasswordBits is the strength a password must be estimated at to be given
// to [CheckPassword]'s callers: a stolen verifier is an offline target, and
// only a long, random password survives that.
const MinPasswordBits = 128

// CheckPassword refuses a password that is too weak to sit behind a verifier: not
// printable ASCII (the page and the tools would have to agree on a Unicode
// normalisation), shorter than 20 characters, of fewer than 10 distinct
// characters, or estimated below [MinPasswordBits]. The estimate is the length
// times log2 of the pool of the character classes it uses (lower case, upper
// case, digits, the rest of printable ASCII), which over-rates a phrase a person
// chose: use a generated password. It is a floor, not a guarantee.
func CheckPassword(password string) error {
	if password == "" {
		return errors.New("the password is empty")
	}
	var lower, upper, digit, other bool
	distinct := map[rune]bool{}
	for _, r := range password {
		switch {
		case r < 0x20 || r > 0x7e:
			return errors.New("the password has a character that is not printable ASCII")
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			other = true
		}
		distinct[r] = true
	}
	pool := 0
	for _, c := range []struct {
		used bool
		size int
	}{{lower, 26}, {upper, 26}, {digit, 10}, {other, 33}} {
		if c.used {
			pool += c.size
		}
	}
	switch {
	case len(password) < 20:
		return errors.New("the password is shorter than 20 characters")
	case len(distinct) < 10:
		return errors.New("the password has fewer than 10 distinct characters")
	case float64(len(password))*math.Log2(float64(pool)) < MinPasswordBits:
		return fmt.Errorf("the password is estimated below %d bits: use a longer, generated one", MinPasswordBits)
	case strings.TrimSpace(password) != password:
		return errors.New("the password starts or ends with a space")
	}
	return nil
}
