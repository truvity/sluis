package compile

import (
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/policy"
)

// An installation's own programs, as identities. A workload is one
// ServiceAccount, on one cluster, in one namespace, or one IAM role in one
// registered account, that becomes one internal group. Nothing else: no
// wildcard, no "every ServiceAccount in this namespace". A group is what
// everything downstream reasons about, so a machine that holds one is
// described in the same model, in the same vocabulary, as the people who
// hold the others.

type (
	// Workload is one program's identity.
	Workload struct {
		// Cluster is the cluster whose ServiceAccount tokens this admits.
		// Required: without it a workload of the same name in ANY federated
		// cluster would be admitted.
		Cluster string `yaml:"cluster"`
		// Namespace and Name are the ServiceAccount.
		Namespace string `yaml:"namespace"`
		Name      string `yaml:"name"`
		// Why is one line on what this program does with the group.
		// Required: a machine rule nobody can explain is one nobody can
		// safely remove.
		Why string `yaml:"why"`
		// AWS is the alternative to the three fields above: an IAM role in a
		// registered AWS account, proving itself with its
		// outbound-federation token. A workload is one or the other, never
		// both. One mapping is one role; a list is several roles holding the
		// same group. Every entry is exact.
		AWS WorkloadAWSRoles `yaml:"aws,omitempty"`
	}

	// WorkloadAWSRoles is the `aws` key: one role (a mapping) or several (a
	// list of mappings), read into a list either way.
	WorkloadAWSRoles []WorkloadAWS

	// WorkloadAWS is one IAM role, exactly.
	WorkloadAWS struct {
		// Account is the 12-digit AWS account id, a registered account.
		Account string `yaml:"account"`
		// Role is the role's name, without its path. Exact: a grant for
		// "every role like x" is one the role's owner can widen by naming a
		// new role.
		Role string `yaml:"role"`
		// Path is the role's IAM path as AWS spells it: `/` for none.
		// Required, so that a row never silently admits any path.
		Path string `yaml:"path"`
	}

	// AWSAccount is one AWS account whose roles may exchange: one row of the
	// issuer's `exchange.aws.accounts`.
	AWSAccount struct {
		// Name is the installation's word for the account, for the logs.
		Name string `yaml:"name"`
		// Issuer is the account's outbound-federation issuer URL.
		Issuer string `yaml:"issuer"`
		// OrgID, when set, requires the account to belong to this AWS
		// Organization.
		OrgID string `yaml:"org_id,omitempty"`
		// Algs are the accepted signing algorithms: ES384 and/or RS256.
		Algs []string `yaml:"algs,omitempty"`
	}
)

// UnmarshalYAML reads `aws:` as one mapping or a list of mappings.
func (r *WorkloadAWSRoles) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.SequenceNode {
		var list []WorkloadAWS
		if err := node.Decode(&list); err != nil {
			return err //nolint:wrapcheck // the decoder's message carries the line
		}

		*r = list

		return nil
	}

	var one WorkloadAWS
	if err := node.Decode(&one); err != nil {
		return err //nolint:wrapcheck // the decoder's message carries the line
	}

	*r = WorkloadAWSRoles{one}

	return nil
}

var (
	// kubernetesName is a Kubernetes object name: a DNS label, lower case.
	kubernetesName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,251}[a-z0-9])?$`)
	awsAccountID   = regexp.MustCompile(`^\d{12}$`)
	// awsRoleName is what IAM allows in a role name; no glob character.
	awsRoleName = regexp.MustCompile(`^[A-Za-z0-9_+=,.@-]{1,64}$`)
	// awsRolePath is `/` or `/seg/seg/`, as IAM spells a path.
	awsRolePath = regexp.MustCompile(`^/([A-Za-z0-9_+=,.@-]+/)*$`)
)

// WorkloadTables names the tables in ValidateWorkloads' messages.
type WorkloadTables struct {
	// Workloads prefixes every row's message: `<Workloads>[<group>]: ...`.
	Workloads string
	// Scopes and Accounts name the scopes matrix and the registered
	// accounts where a message points at them.
	Scopes, Accounts string
}

// ValidateWorkloads refuses a workload that names no program, or one whose
// group is a name the matrix itself derives. clusterThing is the cluster
// tier's thing, which only the matrix may grant; scopes are the scopes of
// the matrix. The accounts themselves are [ValidateAWSAccounts]'.
//
// That last rule is the important one. A workload sharing a group with a
// rung would widen that rung to a machine silently: the group would simply
// gain a matcher, and the model would read as though only people were in
// it.
func ValidateWorkloads(
	tables WorkloadTables, workloads map[string]Workload, clusterThing string, scopes map[string]bool, accounts map[string]AWSAccount,
) []error {
	var errs []error

	for _, name := range sortedKeys(workloads) {
		workload := workloads[name]
		where := fmt.Sprintf("%s[%s]", tables.Workloads, name)

		scope, thing, role, ok := policy.SplitGroup(name)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: the key must be an internal group name, <scope>:<thing>:<role>", where))

			continue
		}

		if clusterThing != "" && thing == clusterThing {
			errs = append(errs, fmt.Errorf("%s: a cluster tier group comes from %s, never a workload", where, tables.Scopes))
		}

		if scope != policy.ScopeAll && !scopes[scope] {
			errs = append(errs, fmt.Errorf("%s: scope %q is not in %s", where, scope, tables.Scopes))
		}

		if strings.TrimSpace(role) == "" {
			errs = append(errs, fmt.Errorf("%s: the group names no role", where))
		}

		if len(workload.AWS) > 0 {
			errs = append(errs, validateWorkloadAWS(where, tables.Accounts, workload, accounts)...)
		} else {
			for _, field := range [][2]string{
				{"cluster", workload.Cluster},
				{"namespace", workload.Namespace},
				{"name", workload.Name},
			} {
				if !kubernetesName.MatchString(field[1]) {
					errs = append(errs, fmt.Errorf("%s: %s %q is not a Kubernetes name", where, field[0], field[1]))
				}
			}
		}

		if strings.TrimSpace(workload.Why) == "" {
			errs = append(errs, fmt.Errorf("%s: say what it does with the group; "+
				"a machine rule nobody can explain is one nobody can safely remove", where))
		}
	}

	return errs
}

// validateWorkloadAWS refuses an AWS workload that is also a ServiceAccount,
// names an account the issuer is not told about (its token would never
// verify), or writes a role or path as a pattern.
func validateWorkloadAWS(where, accountsTable string, workload Workload, accounts map[string]AWSAccount) []error {
	var errs []error

	if workload.Cluster != "" || workload.Namespace != "" || workload.Name != "" {
		errs = append(errs, fmt.Errorf("%s: aws and cluster/namespace/name are alternatives, never both", where))
	}

	seen := map[WorkloadAWS]bool{}

	for _, aws := range workload.AWS {
		if seen[aws] {
			errs = append(errs, fmt.Errorf("%s: role %s%s in account %s is listed twice", where, aws.Path, aws.Role, aws.Account))
		}

		seen[aws] = true

		if _, known := accounts[aws.Account]; !known {
			errs = append(errs, fmt.Errorf("%s: account %q is not in %s, "+
				"so the issuer would never verify its tokens", where, aws.Account, accountsTable))
		}

		if !awsRoleName.MatchString(aws.Role) {
			errs = append(errs, fmt.Errorf("%s: role %q is not an exact IAM role name (no glob)", where, aws.Role))
		}

		if !awsRolePath.MatchString(aws.Path) {
			errs = append(errs, fmt.Errorf("%s: path %q must be `/` or `/seg/.../`, written out", where, aws.Path))
		}
	}

	return errs
}

// ValidateAWSAccounts checks the rows the issuer will refuse to start on.
// where names the table in the messages.
func ValidateAWSAccounts(where string, accounts map[string]AWSAccount) []error {
	var errs []error

	names := map[string]bool{}
	issuers := map[string]bool{}

	for _, id := range sortedKeys(accounts) {
		account := accounts[id]
		row := fmt.Sprintf("%s[%s]", where, id)

		if !awsAccountID.MatchString(id) {
			errs = append(errs, fmt.Errorf("%s: the key must be a 12-digit account id (quote it)", row))
		}

		if strings.TrimSpace(account.Name) == "" || names[account.Name] {
			errs = append(errs, fmt.Errorf("%s: name %q must be set and unique", row, account.Name))
		}

		names[account.Name] = true

		if !strings.HasPrefix(account.Issuer, "https://") || issuers[account.Issuer] {
			errs = append(errs, fmt.Errorf("%s: issuer %q must be a unique https URL", row, account.Issuer))
		}

		issuers[account.Issuer] = true

		for _, alg := range account.Algs {
			if alg != "ES384" && alg != "RS256" {
				errs = append(errs, fmt.Errorf("%s: algorithm %q is not ES384 or RS256", row, alg))
			}
		}
	}

	return errs
}

// WorkloadGroups are the internal groups ServiceAccount workloads hold,
// sorted. An IAM-role workload is not here.
func WorkloadGroups(workloads map[string]Workload) []string {
	out := make([]string, 0, len(workloads))

	for _, name := range sortedKeys(workloads) {
		if len(workloads[name].AWS) == 0 {
			out = append(out, name)
		}
	}

	return out
}

// Fields is the matcher as a renderer writes it: the set keys, in the order
// the policy's schema lists them.
func (m ServiceAccountMatcher) Fields() [][2]string {
	all := [][2]string{
		{"cluster", m.Cluster},
		{"namespace", m.Namespace},
		{"name", m.Name},
	}

	out := make([][2]string, 0, len(all))

	for _, field := range all {
		if field[1] != "" {
			out = append(out, field)
		}
	}

	return out
}

// Fields is the matcher as a renderer writes it.
func (m AWSMatcher) Fields() [][2]string {
	return [][2]string{{"account", m.Account}, {"role", m.Role}, {"path", m.Path}}
}
