// Package orgconfig is what an installation decides for one GitHub
// organisation's settings (ADR 0080): the mode, where the organisation's
// document is read from, how far the document may reach, and the variables
// whose value the installation sets.
//
// It is the policy document's `controllers.github.orgSettings`, a light
// package so that the documents' loaders and validators need none of the
// controller's dependencies.
package orgconfig

import (
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/truvity/sluis/policy/tenant"
)

// Mode is what the controller does with a difference.
type Mode string

// The modes. Report is the default: an organisation changes nothing until
// the installation says enforce.
const (
	ModeReport  Mode = "report"
	ModeEnforce Mode = "enforce"
)

// Valid reports whether the mode is one of the two.
func (m Mode) Valid() bool { return m == ModeReport || m == ModeEnforce }

// Config is what the installation decides for one organisation. The mode is
// here and not in the organisation's document, so the platform can force
// `report` whatever the document says: the document has no key for it.
type Config struct {
	// Mode is `report` or `enforce`. Unset is `report`.
	Mode Mode `yaml:"mode,omitempty" json:"mode,omitempty"`
	// Read says where the organisation's document is and as whom it is read.
	Read Read `yaml:"read" json:"read"`
	// PlatformVariables are path.Match patterns of the variable names the
	// platform owns: `CI_RUNNER_LABEL_*`. The document may not declare one.
	PlatformVariables []string `yaml:"platformVariables,omitempty" json:"platformVariables,omitempty"`
	// PlatformValued are variable names whose value the platform sets and
	// whose visibility and repositories the organisation chooses.
	PlatformValued []string `yaml:"platformValued,omitempty" json:"platformValued,omitempty"`
	// SecretPrefix is the prefix every secret's parameter must have in the
	// organisation's own parameter store, ending in a slash. Unset puts
	// secrets out of scope.
	SecretPrefix string `yaml:"secretPrefix,omitempty" json:"secretPrefix,omitempty"`
	// Variables are the variables the installation sets, by name: each name
	// must be one the platform owns, by pattern or by value.
	Variables map[string]Variable `yaml:"variables,omitempty" json:"variables,omitempty"`
}

// Read is where an organisation's document is.
type Read struct {
	// RoleARN is the role the controller assumes in the organisation's own
	// account. It can read the document and the secrets' parameters and
	// nothing else, and it trusts only the controller's role.
	RoleARN string `yaml:"roleArn" json:"roleArn"`
	// Region is where the account's parameter store is read.
	Region string `yaml:"region" json:"region"`
	// Parameter is the name of the parameter that holds the document.
	Parameter string `yaml:"parameter" json:"parameter"`
}

// Variable is a variable the installation sets.
type Variable struct {
	// Value is the value.
	Value string `yaml:"value" json:"value"`
	// Visibility is `all`, `private` or `selected`. Unset is `all`. For a
	// platform-valued name the organisation's document may choose its own.
	Visibility string `yaml:"visibility,omitempty" json:"visibility,omitempty"`
	// Repositories are the repositories that see it, when selected.
	Repositories []string `yaml:"repositories,omitempty" json:"repositories,omitempty"`
}

var (
	roleARN       = regexp.MustCompile(`^arn:[a-z-]+:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]{1,512}$`)
	regionName    = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)
	parameterName = regexp.MustCompile(`^/[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`)
)

// Ceiling is how far the organisation's document may reach.
func (c Config) Ceiling() tenant.OrgCeiling {
	return tenant.OrgCeiling{PlatformVariables: c.PlatformVariables, PlatformValued: c.PlatformValued, SecretPrefix: c.SecretPrefix}
}

// EffectiveMode is the mode the pass runs in: report unless the
// installation says enforce.
func (c Config) EffectiveMode() Mode {
	if c.Mode == ModeEnforce {
		return ModeEnforce
	}

	return ModeReport
}

// Validate refuses a configuration the controller could not act on.
func (c Config) Validate() error {
	var errs []error

	if c.Mode != "" && !c.Mode.Valid() {
		errs = append(errs, fmt.Errorf("mode %q is not report or enforce", c.Mode))
	}

	switch {
	case !roleARN.MatchString(c.Read.RoleARN):
		errs = append(errs, fmt.Errorf("read.roleArn %q is not an IAM role ARN", c.Read.RoleARN))
	case !regionName.MatchString(c.Read.Region):
		errs = append(errs, fmt.Errorf("read.region %q is not an AWS region", c.Read.Region))
	case !parameterName.MatchString(c.Read.Parameter):
		errs = append(errs, fmt.Errorf("read.parameter %q is not a parameter name", c.Read.Parameter))
	}

	if err := c.Ceiling().Validate(); err != nil {
		errs = append(errs, err)
	}

	ceiling := c.Ceiling()

	for _, name := range sortedNames(c.Variables) {
		v := c.Variables[name]

		if !ceiling.PlatformOwns(name) {
			errs = append(errs, fmt.Errorf(
				"variables: %s is named by neither platformVariables nor platformValued, so the organisation's document could declare it too", name))
		}

		if v.Value == "" {
			errs = append(errs, fmt.Errorf("variables: %s has no value", name))
		}

		section := tenant.OrgSection{Variables: map[string]tenant.OrgVariable{
			name: {Value: v.Value, Visibility: orDefault(v.Visibility, tenant.VisibilityAll), Repositories: v.Repositories},
		}}
		for _, violation := range tenant.CheckOrg(tenant.Owner{Name: "installation"}, section) {
			if violation.Code == tenant.OwnerOrg {
				errs = append(errs, fmt.Errorf("variables: %s: %s", name, violation.Detail))
			}
		}
	}

	return errors.Join(errs...)
}

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}

	return fallback
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}
