package tenant

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// The org section of a fragment (ADR 0080). A company states, for one GitHub
// organisation it holds, the Actions variables and secrets and the code
// security configurations the organisation should have. Unlike the rows of
// ADR 0078, the section is not merged into the installation's model: the
// GitHub controller reads it at run time from the organisation's own account
// and applies it, held to the owner's [OrgCeiling].

type (
	// OrgSection is what a company states for one GitHub organisation.
	OrgSection struct {
		// Variables are the organisation's Actions variables, by name.
		Variables map[string]OrgVariable `yaml:"variables"`
		// Secrets are the organisation's Actions secrets, by name. A secret
		// carries a reference to a SecureString parameter and never a value.
		Secrets map[string]OrgSecret `yaml:"secrets"`
		// SecurityConfigurations are the organisation's code security
		// configurations, by name.
		SecurityConfigurations map[string]SecurityConfiguration `yaml:"security_configurations"`
	}

	// OrgVariable is one Actions variable.
	OrgVariable struct {
		// Value is the variable's value. It is empty for a name the ceiling
		// lists in PlatformValued: the installation sets that value.
		Value string `yaml:"value"`
		// Visibility is `all`, `private` or `selected`.
		Visibility string `yaml:"visibility"`
		// Repositories are the repositories of the organisation that see the
		// variable, by name, when Visibility is `selected`.
		Repositories []string `yaml:"repositories"`
	}

	// OrgSecret is one Actions secret.
	OrgSecret struct {
		// Parameter is the name of the SecureString parameter, in the
		// organisation's own account, that holds the value.
		Parameter string `yaml:"parameter"`
		// Visibility is `all`, `private` or `selected`.
		Visibility string `yaml:"visibility"`
		// Repositories are the repositories that see the secret, by name,
		// when Visibility is `selected`.
		Repositories []string `yaml:"repositories"`
	}

	// SecurityConfiguration is one code security configuration and where it
	// is attached.
	SecurityConfiguration struct {
		// Description is one line about the configuration.
		Description string `yaml:"description"`
		// Settings are GitHub's own setting names and values: `secret_scanning:
		// enabled`. A name or value GitHub's API does not have is refused.
		Settings map[string]string `yaml:"settings"`
		// Attach says which repositories hold the configuration.
		Attach Attachment `yaml:"attach"`
	}

	// Attachment is the repositories a configuration is attached to.
	Attachment struct {
		// Scope is `all` or `selected`.
		Scope string `yaml:"scope"`
		// Repositories are the repositories, by name, when Scope is `selected`.
		Repositories []string `yaml:"repositories"`
	}

	// OrgCeiling is how far an owner's org section may reach. The installation
	// authors it beside the organisation's other entries.
	OrgCeiling struct {
		// PlatformVariables are path.Match patterns of the variable names the
		// platform owns: `CI_RUNNER_LABEL_*`. A section that declares one is
		// refused.
		PlatformVariables []string `yaml:"platform_variables"`
		// PlatformValued are variable names whose value the installation sets
		// and whose visibility and repositories the organisation chooses. The
		// section may declare them without a value, and is refused when it
		// gives one.
		PlatformValued []string `yaml:"platform_valued"`
		// SecretPrefix is the prefix every secret's parameter must have, in
		// the organisation's own parameter store: `/acme/sluis/credentials/`.
		// Empty puts secrets out of scope.
		SecretPrefix string `yaml:"secret_prefix"`
	}
)

// The visibilities of a variable or a secret, and the scopes of an attachment.
const (
	VisibilityAll      = "all"
	VisibilityPrivate  = "private"
	VisibilitySelected = "selected"

	ScopeAll      = "all"
	ScopeSelected = "selected"
)

// SecuritySettings are the settings of a code security configuration the
// section may state, each with the values GitHub accepts.
// https://docs.github.com/en/rest/code-security/configurations
var SecuritySettings = map[string][]string{
	"advanced_security":                               {"enabled", "disabled", "code_security", "secret_protection"},
	"code_security":                                   {"enabled", "disabled", "not_set"},
	"dependency_graph":                                {"enabled", "disabled", "not_set"},
	"dependency_graph_autosubmit_action":              {"enabled", "disabled", "not_set"},
	"dependabot_alerts":                               {"enabled", "disabled", "not_set"},
	"dependabot_security_updates":                     {"enabled", "disabled", "not_set"},
	"code_scanning_default_setup":                     {"enabled", "disabled", "not_set"},
	"secret_protection":                               {"enabled", "disabled", "not_set"},
	"secret_scanning":                                 {"enabled", "disabled", "not_set"},
	"secret_scanning_push_protection":                 {"enabled", "disabled", "not_set"},
	"secret_scanning_validity_checks":                 {"enabled", "disabled", "not_set"},
	"secret_scanning_non_provider_patterns":           {"enabled", "disabled", "not_set"},
	"secret_scanning_generic_secrets":                 {"enabled", "disabled", "not_set"},
	"secret_scanning_generic_secrets_push_protection": {"enabled", "disabled", "not_set"},
	"private_vulnerability_reporting":                 {"enabled", "disabled", "not_set"},
	"enforcement":                                     {"enforced", "unenforced"},
}

// Limits GitHub holds the section to.
const (
	// MaxVariableValue is the longest value of an Actions variable, in bytes.
	MaxVariableValue = 48 * 1024
	maxConfigName    = 100
	maxDescription   = 255
)

var (
	// actionsName is what GitHub allows as the name of an Actions variable or
	// secret: letters, digits and underscores, not starting with a digit.
	actionsName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// repositoryName is what GitHub allows in a repository's name.
	repositoryName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	// parameterName is an SSM parameter name: a path of segments.
	parameterName = regexp.MustCompile(`^/[A-Za-z0-9_.\-]+(/[A-Za-z0-9_.\-]+)*$`)
	// secretPrefix is the SecretPrefix of a ceiling.
	secretPrefix = regexp.MustCompile(`^/([A-Za-z0-9_.\-]+/)+$`)
)

// IsZero reports whether the ceiling names nothing.
func (c OrgCeiling) IsZero() bool {
	return len(c.PlatformVariables) == 0 && len(c.PlatformValued) == 0 && c.SecretPrefix == ""
}

// Validate refuses a ceiling a check could not apply: a pattern that is not a
// path.Match pattern, a name GitHub would refuse, and a prefix that is not a
// parameter path ending in a slash.
func (c OrgCeiling) Validate() error {
	var errs []error

	for _, p := range c.PlatformVariables {
		if _, err := path.Match(p, ""); err != nil || p == "" {
			errs = append(errs, fmt.Errorf("org.platform_variables: %q is not a path.Match pattern", p))
		}
	}

	for _, n := range c.PlatformValued {
		if !ValidActionsName(n) {
			errs = append(errs, fmt.Errorf("org.platform_valued: %q is not a variable name", n))
		}
	}

	if c.SecretPrefix != "" && !secretPrefix.MatchString(c.SecretPrefix) {
		errs = append(errs, fmt.Errorf("org.secret_prefix: %q is not a parameter path ending in a slash", c.SecretPrefix))
	}

	return errors.Join(errs...)
}

// FoldName is the name as GitHub keeps it: GitHub reads the names of Actions
// variables and secrets without regard to case and stores them in upper case.
// Every comparison of a name in this package and in the controller is made on
// the folded name, so `ci_runner_label_x` is the platform's `CI_RUNNER_LABEL_*`.
func FoldName(name string) string { return strings.ToUpper(name) }

// ValidActionsName reports whether a name is one GitHub accepts for an Actions
// variable or secret: letters, digits and underscores, not starting with a
// digit or `GITHUB_`.
func ValidActionsName(name string) bool {
	return actionsName.MatchString(name) && !strings.HasPrefix(FoldName(name), "GITHUB_")
}

// PlatformOwns reports whether the platform owns the variable name, in whole
// or in its value, whatever the case it is written in. Whether the name is
// [OrgCeiling.ValueOnly] says which.
func (c OrgCeiling) PlatformOwns(name string) bool {
	if c.ValueOnly(name) {
		return true
	}

	folded := FoldName(name)

	for _, p := range c.PlatformVariables {
		if ok, err := path.Match(FoldName(p), folded); err == nil && ok {
			return true
		}
	}

	return false
}

// ValueOnly reports whether the platform owns only the value of the name.
func (c OrgCeiling) ValueOnly(name string) bool {
	folded := FoldName(name)

	return slices.ContainsFunc(c.PlatformValued, func(n string) bool { return FoldName(n) == folded })
}

// IsEmpty reports whether the section declares nothing.
func (s OrgSection) IsEmpty() bool {
	return len(s.Variables) == 0 && len(s.Secrets) == 0 && len(s.SecurityConfigurations) == 0
}

// CheckOrg returns the violations of an org section against its owner's
// ceiling, sorted by path and code, or nil when there are none. It reads the
// section and nothing else: the GitHub controller runs it on the document it
// reads at run time, and [Check] runs it on a fragment.
func CheckOrg(o Owner, s OrgSection) []Violation {
	c := &checker{owner: o.Name, ceiling: o.Ceiling}

	if o.Name == "" {
		c.add(OwnerName, "owner", "the owner has no name, so a violation could not say whose it is")
	}

	c.orgSection(s)

	return c.sorted()
}

// orgSection checks the org section of a fragment.
func (c *checker) orgSection(s OrgSection) {
	c.foldedCollisions("org.variables", sortedKeys(s.Variables))
	c.foldedCollisions("org.secrets", sortedKeys(s.Secrets))

	for _, name := range sortedKeys(s.Variables) {
		c.orgVariable(key("org.variables", name), name, s.Variables[name])
	}

	for _, name := range sortedKeys(s.Secrets) {
		c.orgSecret(key("org.secrets", name), name, s.Secrets[name])
	}

	for _, name := range sortedKeys(s.SecurityConfigurations) {
		c.securityConfiguration(key("org.security_configurations", name), name, s.SecurityConfigurations[name])
	}

	c.attachmentOverlap(s.SecurityConfigurations)
}

// foldedCollisions refuses two names that GitHub reads as one.
func (c *checker) foldedCollisions(kind string, names []string) {
	seen := map[string]struct{}{}

	for _, name := range names {
		folded := FoldName(name)
		if _, taken := seen[folded]; taken {
			c.add(OwnerOrg, key(kind, name), "the name is the same as another, which GitHub reads without regard to case")
		}

		seen[folded] = struct{}{}
	}
}

// attachmentOverlap refuses two configurations that are attached to the same
// repository. A repository holds one configuration, so two that name it would
// take it from each other on every pass.
func (c *checker) attachmentOverlap(configs map[string]SecurityConfiguration) {
	holder := map[string]string{}
	all := ""

	for _, name := range sortedKeys(configs) {
		p := key("org.security_configurations", name) + ".attach"
		a := configs[name].Attach

		switch a.Scope {
		case ScopeAll:
			if all != "" {
				c.add(OwnerOrg, p, "two configurations are both attached to every repository")
			}

			all = name
		case ScopeSelected:
			for _, repo := range a.Repositories {
				repo = strings.ToLower(repo)

				if other, taken := holder[repo]; taken && other != name {
					c.add(OwnerOrg, p, "two configurations are both attached to one repository")
				}

				holder[repo] = name
			}
		}
	}

	if all == "" {
		return
	}

	for _, name := range sortedKeys(configs) {
		if name != all && configs[name].Attach.Scope == ScopeSelected {
			c.add(OwnerOrg, key("org.security_configurations", name)+".attach",
				"a configuration attached to every repository overlaps this one")
		}
	}
}

func (c *checker) orgVariable(p, name string, v OrgVariable) {
	org := c.ceiling.Org

	switch {
	case !ValidActionsName(name):
		c.add(OwnerOrg, p, "not a name GitHub accepts for a variable: letters, digits and underscores, not starting with a digit or GITHUB_")

		return
	case org.ValueOnly(name):
		if v.Value != "" {
			c.add(OwnerOrgPlatform, p, "a platform variable: its value is the installation's, and the document sets the visibility only")
		}
	case org.PlatformOwns(name):
		c.add(OwnerOrgPlatform, p, "a platform variable: the installation sets it and the document may not declare it")

		return
	case v.Value == "":
		c.add(OwnerOrg, p, "the variable has no value")
	case len(v.Value) > MaxVariableValue:
		c.add(OwnerOrg, p, "the value is longer than %d bytes", MaxVariableValue)
	}

	// A platform-valued name may leave its visibility to the installation.
	if org.ValueOnly(name) && v.Visibility == "" {
		if len(v.Repositories) > 0 {
			c.add(OwnerOrg, p, "repositories need visibility selected")
		}

		return
	}

	c.visibility(p, v.Visibility, v.Repositories)
}

func (c *checker) orgSecret(p, name string, s OrgSecret) {
	switch {
	case !ValidActionsName(name):
		c.add(OwnerOrg, p, "not a name GitHub accepts for a secret: letters, digits and underscores, not starting with a digit or GITHUB_")

		return
	case c.ceiling.Org.SecretPrefix == "":
		c.add(OwnerOrg, p, "the ceiling has no secret_prefix, so secrets are not in scope")

		return
	case !parameterName.MatchString(s.Parameter) || strings.Contains(s.Parameter, ".."):
		c.add(OwnerOrg, p+".parameter", "not a parameter name")
	case !strings.HasPrefix(s.Parameter, c.ceiling.Org.SecretPrefix):
		c.add(OwnerOrg, p+".parameter", "outside the prefix the ceiling allows (%s)", c.ceiling.Org.SecretPrefix)
	}

	c.visibility(p, s.Visibility, s.Repositories)
}

// visibility checks a visibility and the repositories that go with it.
func (c *checker) visibility(p, visibility string, repositories []string) {
	switch visibility {
	case VisibilityAll, VisibilityPrivate:
		if len(repositories) > 0 {
			c.add(OwnerOrg, p+".repositories", "repositories need visibility selected, not %s", visibility)
		}
	case VisibilitySelected:
		if len(repositories) == 0 {
			c.add(OwnerOrg, p+".repositories", "visibility selected names no repository")
		}

		c.repositories(p+".repositories", repositories)
	default:
		c.add(OwnerOrg, p+".visibility", "not all, private or selected")
	}
}

func (c *checker) repositories(p string, names []string) {
	seen := map[string]struct{}{}

	for i, name := range names {
		folded := strings.ToLower(name)

		switch _, dup := seen[folded]; {
		case !repositoryName.MatchString(name):
			c.add(OwnerOrg, fmt.Sprintf("%s[%d]", p, i), "not a repository name of the organisation")
		case dup:
			c.add(OwnerOrg, fmt.Sprintf("%s[%d]", p, i), "named twice, which GitHub reads without regard to case")
		}

		seen[folded] = struct{}{}
	}
}

func (c *checker) securityConfiguration(p, name string, s SecurityConfiguration) {
	switch {
	case strings.TrimSpace(name) != name || name == "" || len(name) > maxConfigName:
		c.add(OwnerOrg, p, "not a configuration name: 1 to %d characters, no space at either end", maxConfigName)
	case len(s.Description) > maxDescription:
		c.add(OwnerOrg, p+".description", "the description is longer than %d characters", maxDescription)
	}

	if s.Description == "" {
		// GitHub requires a description on a configuration.
		c.add(OwnerOrg, p+".description", "a configuration needs a description")
	}

	for _, setting := range sortedKeys(s.Settings) {
		allowed, known := SecuritySettings[setting]

		switch {
		case !known:
			c.add(OwnerOrg, key(p+".settings", setting), "not a setting GitHub's code security configurations have")
		case !slices.Contains(allowed, s.Settings[setting]):
			c.add(OwnerOrg, key(p+".settings", setting), "the value is not one of %s", strings.Join(allowed, ", "))
		}
	}

	switch s.Attach.Scope {
	case ScopeAll:
		if len(s.Attach.Repositories) > 0 {
			c.add(OwnerOrg, p+".attach.repositories", "repositories need scope selected, not all")
		}
	case ScopeSelected:
		if len(s.Attach.Repositories) == 0 {
			c.add(OwnerOrg, p+".attach.repositories", "scope selected names no repository")
		}

		c.repositories(p+".attach.repositories", s.Attach.Repositories)
	default:
		c.add(OwnerOrg, p+".attach.scope", "not all or selected")
	}
}
