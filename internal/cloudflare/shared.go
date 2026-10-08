package cloudflare

import shared "github.com/truvity/sluis/storage/cloudflare"

// The vocabulary below lives in the storage module (github.com/truvity/sluis/
// storage/cloudflare), the one place both the root module and audit may import:
// audit mints R2 credentials for itself with the same prototype rules and the
// same refusal list, and a list that existed twice would diverge. This package
// re-exports it so that the minter and its callers keep one import.

type (
	// Token is an account token as Cloudflare describes it.
	Token = shared.Token
	// NewToken is a token to create.
	NewToken = shared.NewToken
	// Created is a token Cloudflare made.
	Created = shared.Created
	// PrototypeError is a prototype that must not be cloned.
	PrototypeError = shared.PrototypeError
)

// Status of a token in Cloudflare.
const (
	StatusActive   = shared.StatusActive
	StatusDisabled = shared.StatusDisabled
	StatusExpired  = shared.StatusExpired
)

// The reasons a prototype is refused, and the condition's client-IP key.
const (
	ReasonPrototypeActive    = shared.ReasonPrototypeActive
	ReasonPrototypeForbidden = shared.ReasonPrototypeForbidden
	ReasonPrototypeMissing   = shared.ReasonPrototypeMissing
	ConditionIPKey           = shared.ConditionIPKey
)

// ErrNotFound is a token Cloudflare does not have.
var ErrNotFound = shared.ErrNotFound

// The functions of the shared vocabulary.
var (
	R2Secret           = shared.R2Secret
	Prefix             = shared.Prefix
	StoredName         = shared.StoredName
	OnDemandName       = shared.OnDemandName
	SplitName          = shared.SplitName
	IsOwn              = shared.IsOwn
	IsStored           = shared.IsStored
	Caller             = shared.Caller
	ValidateInstance   = shared.ValidateInstance
	ForbiddenGroup     = shared.ForbiddenGroup
	ForbiddenGroupWith = shared.ForbiddenGroupWith
	GroupIDs           = shared.GroupIDs
	CheckPrototype     = shared.CheckPrototype
	IsPrototypeError   = shared.IsPrototypeError
	ClonePolicies      = shared.ClonePolicies
	CloneCondition     = shared.CloneCondition
)
