package sluispulumi

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// The control plane of a KMS key is what changes WHO may use it: the key
// policy, grants, the key's state, its deletion and its aliases. A key policy that hands
// the account's root the whole of kms:* hands it to every IAM principal that
// has an allow for it (an operator's power role, an automation role, a
// StackSet role), and any of them can rewrite the policy, give itself the data
// plane and decrypt what the key wraps or sign what the key signs. These
// statements close that: the actions below are denied to every principal
// but the named administrators.
//
// The administrators are role ARNs (patterns for an SSO role with a hash
// suffix), so the key can be made before they exist. The role that applies the
// stack is one of them: it changes the policy on every apply.
const (
	sidKeyControlPlaneAdmins = "SluisKeyControlPlaneAdmins"
	sidKeyControlPlaneDeny   = "SluisKeyControlPlaneAdminsOnly"
)

// KeyControlPlaneActions are the actions only the administrators hold on a key
// that sluis uses: the policy, the grants, disabling the key, scheduling its
// deletion, and the aliases. An alias is how a writer names the key (SSM
// SecureStrings by `alias/...`), so retargeting one sends new writes to a key of
// someone else's choosing: creating, updating and deleting an alias of the key
// needs the action on the key too, and only the administrators have it.
var KeyControlPlaneActions = []string{
	"kms:CreateAlias", "kms:CreateGrant", "kms:DeleteAlias", "kms:DisableKey", "kms:PutKeyPolicy", "kms:RevokeGrant",
	"kms:ScheduleKeyDeletion", "kms:UpdateAlias",
}

// keyAdministerActions are what the account root delegates to IAM for the key's
// upkeep without changing who may use it: describing and listing, tags, the
// description, rotation and cancelling a deletion. None changes the key policy,
// a grant or an alias.
var keyAdministerActions = []string{
	"kms:CancelKeyDeletion", "kms:DescribeKey", "kms:DisableKeyRotation", "kms:EnableKey", "kms:EnableKeyRotation",
	"kms:Get*", "kms:List*", "kms:TagResource", "kms:UntagResource", "kms:UpdateKeyDescription",
}

// KeyControlPlaneStatements are the two statements that hold a key's control
// plane to its administrators, for the key's policy. The first allows the
// account root, narrowed to the administrators by aws:PrincipalArn, the
// actions of KeyControlPlaneActions; the second denies them to everyone else
// (AWS services acting on their own are not named by the condition). admins
// are role ARNs, a `*` allowed in the role's name only (see CheckKeyAdmin), at
// least one.
//
// The account's own root user is not exempt: reaching the control plane in an
// emergency is the break-glass role's, which belongs in admins.
func KeyControlPlaneStatements(account string, admins []string) ([]map[string]any, error) {
	if !accountPattern.MatchString(account) {
		return nil, fmt.Errorf("sluispulumi: the account %q is not a 12-digit account id", account)
	}
	exempt, err := keyAdminSet(admins)
	if err != nil {
		return nil, err
	}
	return []map[string]any{
		{
			"Sid": sidKeyControlPlaneAdmins, "Effect": "Allow", "Principal": map[string]any{"AWS": arnPrefix + "iam::" + account + ":root"},
			"Action": append([]string(nil), KeyControlPlaneActions...), "Resource": "*",
			"Condition": map[string]any{condArnLike: map[string]any{condPrincipalArn: exempt}},
		},
		KeyControlPlaneDeny(exempt),
	}, nil
}

// KeyControlPlaneDeny is the second of KeyControlPlaneStatements for administrators
// already validated: a Deny of KeyControlPlaneActions to every principal not
// matched by admins. It is for an estate that writes a key policy of its own.
func KeyControlPlaneDeny(admins []string) map[string]any {
	return statement{
		"Sid": sidKeyControlPlaneDeny, "Effect": "Deny", "Principal": map[string]any{"AWS": "*"},
		"Action": append([]string(nil), KeyControlPlaneActions...), "Resource": "*",
		"Condition": map[string]any{
			"ArnNotLike": map[string]any{condPrincipalArn: sortedStrings(admins)},
			"Bool":       map[string]any{"aws:PrincipalIsAWSService": "false"},
		},
	}
}

// keyAdminSet validates admins and returns them sorted, without repeats. An
// empty list is refused: a key whose control plane nobody holds cannot be
// changed again, not even by the stack that made it.
func keyAdminSet(admins []string) ([]string, error) {
	if len(admins) == 0 {
		return nil, errors.New("sluispulumi: no key administrator is named: the control plane of the key (its policy, grants, disabling and deletion) " +
			"is held to the named administrators, and with none the key could not be changed again; name the role that applies the stack and " +
			"the break-glass role (KeyAdmins, Breakglass)")
	}
	for _, a := range admins {
		if err := CheckKeyAdmin(a); err != nil {
			return nil, err
		}
	}
	return dedupe(sortedStrings(admins)), nil
}

var (
	accountPattern = regexp.MustCompile(`^[0-9]{12}$`)
	// adminArnPattern is a role ARN: `arn:aws:iam::<account>:role/[path/]name`.
	adminArnPattern = regexp.MustCompile(`^arn:` + `aws[a-z-]*:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_*/-]+$`)
)

// ssoRegionPath is the one path with a wildcard: an SSO role's, whose region
// segment an estate may leave open.
const ssoRegionPath = "aws-reserved/sso.amazonaws.com/*/"

// minLiteralAdminName is how much of a role's name a pattern keeps literal.
const minLiteralAdminName = 4

// CheckKeyAdmin holds a principal that a key policy names as an operator's or
// administrator's role to what it can safely be: the ARN of an IAM ROLE (never
// the account's root, a user, or `*`), with the account and the role path
// literal. The wildcards allowed are in the role's NAME (the last segment), for
// an SSO role whose name ends in a hash (`AWSReservedSSO_Admin_*`), which keeps
// at least four literal characters, and for the region of an SSO role's path
// (`aws-reserved/sso.amazonaws.com/*/`). `?` is refused everywhere.
func CheckKeyAdmin(arn string) error {
	if !adminArnPattern.MatchString(arn) {
		return fmt.Errorf("sluispulumi: %q is not the ARN of an IAM role (arn:aws:iam::<account>:role/<name>): "+
			"the account's root, a user, a wildcard account or a bare `*` is not an administrator", arn)
	}
	_, rolePart, _ := strings.Cut(arn, ":role/")
	path, name := "", rolePart
	if i := strings.LastIndex(rolePart, "/"); i >= 0 {
		path, name = rolePart[:i+1], rolePart[i+1:]
	}
	if strings.Contains(path, "*") && path != ssoRegionPath {
		return fmt.Errorf("sluispulumi: %q has a wildcard in the role's path: a wildcard is for the hash of an SSO role's name, "+
			"and for the region in %s, only", arn, ssoRegionPath)
	}
	if name == "" {
		return fmt.Errorf("sluispulumi: %q names no role", arn)
	}
	if strings.Contains(name, "*") && len(strings.ReplaceAll(name, "*", "")) < minLiteralAdminName {
		return fmt.Errorf("sluispulumi: %q keeps fewer than %d literal characters of the role's name: the pattern would name too many roles",
			arn, minLiteralAdminName)
	}
	return nil
}

// checkKeyAdmins is CheckKeyAdmin for each, naming the field.
func checkKeyAdmins(field string, arns []string) error {
	var errs []error
	for _, a := range arns {
		if err := CheckKeyAdmin(a); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", field, err))
		}
	}
	return errors.Join(errs...)
}
