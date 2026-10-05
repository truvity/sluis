package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path"

	policyconfig "github.com/truvity/policy/config"
	yaml "go.yaml.in/yaml/v3"

	sluis "github.com/truvity/sluis"
	"github.com/truvity/sluis/policy"
)

// schemaFor reads the committed schema of one document: the one embedded in the
// release, which is the one the chart's tests and a deployer's CI validate
// against.
func schemaFor(name string) []byte {
	b, err := sluis.ConfigSchemas.ReadFile(path.Join("schemas/config", name+".schema.json"))
	if err != nil {
		// Unreachable: the files are embedded at build time, so a missing one
		// fails to compile rather than at run time.
		panic(err)
	}
	return b
}

// schemaV1For is a v1 service document's schema, frozen as v1.61 wrote it.
func schemaV1For(name string) []byte {
	b, err := sluis.ConfigSchemas.ReadFile(path.Join("schemas/config/v1", name+".schema.json"))
	if err != nil {
		panic(err)
	}
	return b
}

// Validate checks a decoded v2 document against its schema. The chart's tests
// call it on what the chart renders, which is what stops the two drifting.
func Validate(name string, doc any) error {
	return policyconfig.Validate(doc, schemaFor(name))
}

// Document is what [Load] reads: a service document or the policy document.
type Document interface {
	Sluis | Serve | ControllerGitHub | ControllerSlack | PolicyDocument
}

// Service is a service document: what a process is started with.
type Service interface {
	Sluis | Serve | ControllerGitHub | ControllerSlack
}

// Load reads one document from a file, holds it to its schema and decodes it,
// through truvity/policy's versioned loader (config.LoadKind): a binary reads
// its documents' version N and N-1.
//
// A v2 document is validated and decoded as it stands, after a key v2 retired
// is refused with a message saying where it went. A v1 document (no
// apiVersion) is held to the v1 schema it was written against and converted:
// what v1 kept in a service document and v2 keeps in the policy document is
// remembered, and [PolicyOf] reads it from the files v1 named.
func Load[T Document](file string) (*T, error) {
	var out T
	var err error
	switch v := any(&out).(type) {
	case *PolicyDocument:
		err = loadPolicyDocument(file, v)
	case *Sluis:
		err = loadSluis(file, v)
	case *Serve:
		err = loadService(file, "serve", v, func(l *legacyPolicy) { v.legacy = l })
	case *ControllerGitHub:
		err = loadService(file, "controller-github", v, func(l *legacyPolicy) { v.legacy = l })
	case *ControllerSlack:
		err = loadService(file, "controller-slack", v, func(l *legacyPolicy) { v.legacy = l })
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Config is one process's whole configuration, by section: the service
// document it was started with and the policy document that names. Nothing is
// overlaid: each section is one document.
type Config[S Service] struct {
	Service *S
	// Policy is nil when the service document names none and no fallback was
	// given.
	Policy *PolicyDocument
}

// LoadConfig reads a process's service document and the policy document it
// names. fallback is the access model's tables a process decides by when the
// document names no policy (the service's built-in two groups); nil leaves the
// policy nil.
func LoadConfig[S Service](file string, fallback *policy.Policy) (Config[S], error) {
	svc, err := Load[S](file)
	if err != nil {
		return Config[S]{}, err
	}
	p, err := PolicyOf(svc, fallback)
	if err != nil {
		return Config[S]{}, err
	}
	return Config[S]{Service: svc, Policy: p}, nil
}

// PolicyOf reads the policy document a service document names: its
// `policy.file`, or, for a converted v1 document, the files v1 named. With
// neither, fallback's tables, or nil.
func PolicyOf[S Service](svc *S, fallback *policy.Policy) (*PolicyDocument, error) {
	var ref *PolicyRef
	var legacy *legacyPolicy
	switch v := any(svc).(type) {
	case *Sluis:
		ref, legacy = v.Policy, v.legacy
	case *Serve:
		ref, legacy = v.Policy, v.legacy
	case *ControllerGitHub:
		ref, legacy = v.Policy, v.legacy
	case *ControllerSlack:
		ref, legacy = v.Policy, v.legacy
	}
	switch {
	case legacy != nil:
		return legacy.document(fallback)
	case ref != nil && ref.File != "":
		return Load[PolicyDocument](ref.File)
	case fallback != nil:
		p := NewPolicyDocument(*fallback)
		if err := p.Validate(); err != nil {
			return nil, err
		}
		return p, nil
	}
	return nil, nil //nolint:nilnil // no policy declared and none to fall back on
}

// IsSluis reports whether the file is the one service document (v3), by its
// apiVersion alone. A file that cannot be read is not: the loader reports it.
func IsSluis(file string) bool {
	doc, err := read(file)
	return err == nil && doc != nil && doc[policyconfig.APIVersionKey] == APIVersion("sluis")
}

// loadSluis reads the one service document. A document of this build's version
// (`sluis/v3`) is validated against schemas/config/sluis.schema.json and decoded
// as it stands. Anything else is the v2 `serve` document, or a v1 one, which
// loads as it always did and runs no controllers: a deployment moves its
// documents on its own schedule.
func loadSluis(file string, into *Sluis) error {
	doc, err := read(file)
	if err != nil {
		return err
	}
	if doc != nil && doc[policyconfig.APIVersionKey] == APIVersion("sluis") {
		kind := policyconfig.Kind{Name: Group + "/sluis", Version: 3, Schema: schemaFor("sluis")}
		return policyconfig.LoadKind(file, kind, into)
	}
	// Anything that says it is a different kind of document is the loader's to
	// refuse, naming the key: a controller document is not a service document.
	return loadService(file, "serve", &into.Serve, func(l *legacyPolicy) { into.legacy = l })
}

// LegacySecrets is, for a document converted from v1, where v1 said each
// secret it names was (by the name v2 gives it), and v1's clientSecretsDir.
// Both are empty for a v2 document.
func (s *Serve) LegacySecrets() (map[string]SecretLocation, string) {
	if s.legacy == nil {
		return nil, ""
	}
	return s.legacy.secrets, s.legacy.clientDir
}

// Converted reports whether the document was v1, converted as it was loaded.
func (s *Serve) Converted() bool { return s.legacy != nil }

// Converted reports whether the document was v1, converted as it was loaded.
func (r *Roster) Converted() bool { return r.legacy != nil }

// read is a document as validation sees it: YAML, normalised through JSON.
// The versioned loader reads the file itself; this is the look the loader
// takes first, for the keys v2 retired.
func read(file string) (map[string]any, error) {
	raw, err := os.ReadFile(file) //nolint:gosec // the path is the process's own configuration
	if err != nil {
		return nil, &policyconfig.Error{File: file, Err: err}
	}
	var parsed any
	if err = yaml.Unmarshal(raw, &parsed); err != nil {
		return nil, &policyconfig.Error{File: file, Err: fmt.Errorf("not valid YAML: %w", err)}
	}
	asJSON, err := json.Marshal(parsed)
	if err != nil {
		return nil, &policyconfig.Error{File: file, Err: fmt.Errorf("cannot be represented as JSON: %w", err)}
	}
	var m map[string]any
	_ = json.Unmarshal(asJSON, &m)
	return m, nil
}

// refuseRetired is the look before the versioned loader: a document of this
// build's version that names a key v2 retired is refused with where the key
// went, not the schema's bare "not a key this service reads".
func refuseRetired(file, document string) error {
	doc, err := read(file)
	if err != nil || doc == nil || doc[policyconfig.APIVersionKey] != APIVersion(document) {
		// The versioned loader reports what is wrong with the file.
		return nil
	}
	if err = refuseRetiredKeys(document, doc); err != nil {
		return &policyconfig.Error{File: file, Err: err}
	}
	return nil
}

func loadService(file, name string, into any, keep func(*legacyPolicy)) error {
	if err := refuseRetired(file, name); err != nil {
		return err
	}
	var legacy *legacyPolicy
	kind := policyconfig.Kind{
		Name: Group + "/" + name, Version: 2,
		Schema: schemaFor(name), Previous: schemaV1For(name),
		Upgrade: func(doc map[string]any) (map[string]any, error) {
			l, err := convertV1(name, doc)
			legacy = l
			return doc, err
		},
	}
	if err := policyconfig.LoadKind(file, kind, into); err != nil {
		return err
	}
	if legacy != nil {
		keep(legacy)
	}
	return nil
}

// policyV1 is what a v1 policy file is held to before it is converted: an
// object. Its own parser (policy.Parse, strict) is the v1 schema.
var policyV1 = []byte(`{"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object"}`)

// policyKind is the policy document, v2, and the v1 policy file (`version: 1`,
// or an access document), which is one layer of tables and is converted to a
// document of tables alone.
func policyKind() policyconfig.Kind {
	return policyconfig.Kind{
		Name: Group + "/policy", Version: 2,
		Schema: schemaFor("policy"), Previous: policyV1,
		Upgrade: func(doc map[string]any) (map[string]any, error) {
			delete(doc, policyconfig.APIVersionKey)
			raw, err := yaml.Marshal(doc)
			if err != nil {
				return nil, err
			}
			layer, err := policy.ParseLayer(raw)
			if err != nil {
				return nil, err
			}
			encoded, err := NewPolicyDocument(layer).Encode()
			if err != nil {
				return nil, err
			}
			var out any
			if err = yaml.Unmarshal(encoded, &out); err != nil {
				return nil, err
			}
			asJSON, err := json.Marshal(out)
			if err != nil {
				return nil, err
			}
			var m map[string]any
			err = json.Unmarshal(asJSON, &m)
			return m, err
		},
	}
}

// loadPolicyDocument reads the policy document and holds it to its checks.
func loadPolicyDocument(file string, into *PolicyDocument) error {
	if err := parsePolicyDocument(file, into); err != nil {
		return err
	}
	if err := into.Validate(); err != nil {
		return &policyconfig.Error{File: file, Err: err}
	}
	return nil
}

// parsePolicyDocument decodes one policy file, v2 or v1, without the semantic
// checks: a fragment a render merges need not stand alone.
func parsePolicyDocument(file string, into *PolicyDocument) error {
	if err := refuseRetired(file, "policy"); err != nil {
		return err
	}
	return policyconfig.LoadKind(file, policyKind(), into)
}

// Secret reads the environment variable the configuration names. An unset or
// empty variable is an error naming the variable, never quoting anything.
func Secret(name string) (string, error) { return policyconfig.Secret(name) }

// EnvConfig is the variable that names the service document when no --config
// is given.
const EnvConfig = "SLUIS_CONFIG"

// Group is the group of this repository's document kinds: an apiVersion is
// `sluis.truvity.github.io/<document>/v<N>`.
const Group = "sluis.truvity.github.io"

// APIVersion is the apiVersion this build writes for one document: `sluis` (v3),
// or `serve`, `controller-github`, `controller-slack` or `policy` (v2).
func APIVersion(document string) string {
	if document == "sluis" {
		return Group + "/sluis/v3"
	}
	return Group + "/" + document + "/v2"
}
