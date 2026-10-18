package srp

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// Credential is an account as a server stores it: the salt and the verifier.
// It is not the password and cannot sign anybody in, but it lets whoever holds
// it test guesses offline, so it is kept as a secret.
type Credential struct {
	Salt     []byte
	Verifier []byte
}

type credentialDocument struct {
	Salt     string `json:"salt"`
	Verifier string `json:"verifier"`
}

// NewCredential derives the credential of a username and password.
//
// The salt is derived too, from the password and the username, so the same
// input gives the same credential: an infrastructure program that recomputes it
// on every run does not rewrite what it stored. A salt exists to make two
// accounts' verifiers unrelated, and a derived one does that for a password
// that is machine-generated and not shared between accounts; it adds nothing
// against a guess of the password itself, and a random salt would not either.
func (p Params) NewCredential(username, password string) Credential {
	mac := hmac.New(sha256.New, []byte(password))
	mac.Write([]byte("sluis srp salt"))
	mac.Write([]byte{0})
	mac.Write([]byte(username))
	salt := mac.Sum(nil)
	return Credential{Salt: salt, Verifier: p.Verifier(salt, username, password)}
}

// Document is the credential as it is stored: {"salt":"<hex>","verifier":"<hex>"}.
func (c Credential) Document() []byte {
	out, _ := json.Marshal(credentialDocument{Salt: hex.EncodeToString(c.Salt), Verifier: hex.EncodeToString(c.Verifier)})
	return out
}

// ParseCredential reads a stored credential and refuses one that is not for
// these parameters: a verifier of another width, no salt, or a verifier that is
// zero modulo N.
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
	if !p.valid(new(big.Int).SetBytes(verifier)) {
		return Credential{}, errors.New("srp: credential: the verifier is zero")
	}
	return Credential{Salt: salt, Verifier: verifier}, nil
}
