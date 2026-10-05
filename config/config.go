package config

import (
	internal "github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/config/duration"
	"github.com/truvity/sluis/policy"
)

// The documents. These are the binary's own types.
type (
	// Sluis is the one service document (v3): the issuer, the console, the
	// directory hub and, under `controllers`, the controllers the process runs.
	Sluis = internal.Sluis
	// PolicyDocument is the policy document (v2): the access model's tables and
	// whom the exchange trusts, what an operator may make, what each controller
	// may change and what is copied out.
	PolicyDocument = internal.PolicyDocument
	// Document is what [Load] reads.
	Document = internal.Document
	// Duration is a time span as a document spells it: "30s", "2m", "168h".
	Duration = duration.Duration
	// Policy is the access model's tables, which a policy document holds.
	Policy = policy.Policy
)

// The sections of the service document an installation carries through
// unchanged, under the same key.
type (
	// Log is `log`.
	Log = internal.Log
	// Lifetimes is `lifetimes`: how long what the issuer hands out lives.
	Lifetimes = internal.Lifetimes
	// Freshness is `freshness`: how the directory's snapshot is kept current.
	Freshness = internal.Freshness
	// Secrets is `secrets`: how the secrets a document names are delivered.
	Secrets = internal.Secrets
	// Recovery is `recovery`: the sign-in that needs no directory.
	Recovery = internal.Recovery
	// Login is `login`: how a person signs in to the console.
	Login = internal.Login
	// Console is `console`: where the console is published.
	Console = internal.Console
	// OAuthClient is `oauthClient`: the client registered with the directory backend.
	OAuthClient = internal.OAuthClient
	// SigningKey is `signingKey`: where the issuer's keys are and how they rotate.
	SigningKey = internal.SigningKey
	// Valkey is `valkey`: the shared store for logins in progress.
	Valkey = internal.Valkey
	// Directory is `directory`: the corporate directories declared.
	Directory = internal.Directory
	// Audit is `audit`: the audit installation the service records to.
	Audit = internal.Audit
	// AdapterChoice names the adapter of one concern and its settings.
	AdapterChoice = internal.AdapterChoice
	// Export is one copy of a secret the console keeps, made out of the service
	// into a secret store a consumer reads.
	Export = internal.Export
)

// Group is the group of the documents' kinds: an apiVersion is
// `<Group>/<document>/v<N>`.
const Group = internal.Group

// EnvConfig is the variable that names the service document when a process is
// given no --config.
const EnvConfig = internal.EnvConfig

// APIVersion is the apiVersion this build writes for one document: `sluis`
// (v3), `policy` (v2) or `installation` (v1).
func APIVersion(document string) string {
	if document == "installation" {
		return Group + "/installation/v1"
	}
	return internal.APIVersion(document)
}

// Load reads one document from a file, holds it to its schema and decodes it.
// A v2 document is validated as it stands; an older one the binary still reads
// is converted. For the policy document the semantic checks run too.
func Load[T Document](file string) (*T, error) { return internal.Load[T](file) }

// Validate checks a decoded document against its authored schema: name is
// `sluis`, `policy` or `installation`. The chart's tests call it on what the
// chart renders.
func Validate(name string, doc any) error { return internal.Validate(name, doc) }

// RenderPolicyLayers builds the one canonical policy document from a file or a
// directory of layers (a v1 policy file, an access document, or a policy
// document fragment), merged in name order and held to every check the service
// runs at start. It is what `sluisctl policy render` runs; an estate that
// writes an [Installation] has no layers to merge and calls [Render].
func RenderPolicyLayers(path string) (*PolicyDocument, error) { return internal.Render(path) }
