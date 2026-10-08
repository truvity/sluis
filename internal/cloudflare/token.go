package cloudflare

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// Status of a token in Cloudflare.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusExpired  = "expired"
)

// Token is an account token as Cloudflare describes it. Policies and Condition
// are kept as the JSON Cloudflare sent so a clone copies them exactly: sluis
// does not interpret a policy's resources, it only reads which permission
// groups it names.
type Token struct {
	ID        string
	Name      string
	Status    string
	ExpiresOn time.Time
	// Policies is the JSON array of the token's policies.
	Policies json.RawMessage
	// Condition is the JSON object of the token's condition, or empty.
	Condition json.RawMessage
}

// NewToken is a token to create.
type NewToken struct {
	Name      string
	ExpiresOn time.Time
	// Policies is the JSON array to send, as [ClonePolicies] made it.
	Policies json.RawMessage
	// Condition is the JSON object to send, as [CloneCondition] made it; empty
	// for none.
	Condition json.RawMessage
}

// Created is a token Cloudflare made. Value is shown once and is a secret.
type Created struct {
	ID        string
	Name      string
	ExpiresOn time.Time
	Value     string
}

// R2Secret is the secret access key of an R2 credential made from a token: the
// hex SHA-256 of the token's value. The access key id is the token's id.
func R2Secret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// ErrNotFound is a token Cloudflare does not have: an API returns it for a
// missing token, whatever its transport said.
var ErrNotFound = errors.New("cloudflare: not found")
