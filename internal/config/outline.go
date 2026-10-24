package config

import (
	"errors"

	policyconfig "github.com/truvity/policy/config"
)

// SluisOutline is the Outline module's own document, `sluis-outline/v1`: the
// controller that keeps each Outline workspace's groups, roles and collections
// as the policy's outline table says (docs/decisions/0077). It is a process of
// its own on Kubernetes and a function of its own on Lambda; the issuer only
// calls it for the console's Outline pages.
//
// It shares the shape of a controller document: where the console is and how
// the controller proves itself to it, the policy it decides by, the ports and
// adapters, the audit installation, the secrets and the probes. What is its
// own is under `outline`.
type SluisOutline struct {
	Roster
	Outline *OutlineModule `json:"outline,omitempty"`
}

// OutlineModule is the document's `outline` section.
type OutlineModule struct {
	// Serve makes the module answer the console's calls on Kubernetes, as
	// `slack.serve` does for the Slack module.
	Serve *ModuleServe `json:"serve,omitempty"`
	// ShareScan is how often the single-document shares are read. Unset is an
	// hour.
	ShareScan *Duration `json:"shareScan,omitempty"`
}

// OutlineDocument is the Outline module's document as the installation's
// shared keys (the release, the policy, the log level, the audit installation)
// and the module's own section say it. The ports, adapters, platform and
// secrets are the caller's to set: a module function names its own.
func (s *Sluis) OutlineDocument(own ControllerCommon, shareScan *Duration) *SluisOutline {
	r := s.roster(&own)
	r.APIVersion = APIVersion("sluis-outline")
	r.RecordsDir, r.GitHub, r.Slack, r.Google = "", nil, nil, nil
	doc := &SluisOutline{Roster: r}
	if shareScan != nil {
		doc.Outline = &OutlineModule{ShareScan: shareScan}
	}
	return doc
}

// OutlineTokenFile is where the module's projected ServiceAccount token, which
// it presents to the console, is mounted unless the document says otherwise.
const OutlineTokenFile = "/var/run/secrets/outline-roster/token"

// LoadOutline reads the Outline module's document and holds it to its schema.
func LoadOutline(file string) (*SluisOutline, error) {
	var out SluisOutline
	kind := policyconfig.Kind{Name: Group + "/sluis-outline", Version: 1, Schema: schemaFor("sluis-outline")}
	if err := policyconfig.LoadKind(file, kind, &out); err != nil {
		return nil, err
	}
	if err := out.Validate(); err != nil {
		return nil, &policyconfig.Error{File: file, Err: err}
	}
	return &out, nil
}

// Validate is what the schema cannot say.
func (s *SluisOutline) Validate() error {
	var errs []error
	if s.Policy == nil || s.Policy.File == "" {
		errs = append(errs, errors.New("policy.file is required: the bindings are the policy document's outline table"))
	}
	if s.Outline != nil && s.Outline.Serve != nil && len(s.Outline.Serve.Issuers) == 0 {
		errs = append(errs, errors.New("outline.serve.issuers: at least one is required"))
	}
	return errors.Join(errs...)
}
