package auditpulumi

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The control plane of a KMS key is what changes WHO may use it: the key
// policy, grants, the key's state and its deletion. A key policy that hands the
// account's root the whole of kms:* hands it to every IAM principal that has an
// allow for it (a power role, an automation role, a StackSet role), and any of
// them can rewrite the policy, give itself kms:Sign on the seal key and forge
// audit seals. The seal key's and the digest key's policies hold the five
// actions below to the named administrators, and deny them to everyone else.
//
// This is the audit library's own copy of what deploy/pulumi has for the keys it
// writes policies of (KeyControlPlaneStatements there): the two modules do not
// share code, and the actions and the rule for who is an administrator are the
// same.
const (
	sidControlPlaneAdmins = "AdministerTheKey"
	sidControlPlaneDeny   = "ControlPlaneAdminsOnly"
)

// KeyControlPlaneActions are the actions only the administrators hold on a key.
var KeyControlPlaneActions = []string{
	"kms:CreateGrant", "kms:DisableKey", "kms:PutKeyPolicy", "kms:RevokeGrant", "kms:ScheduleKeyDeletion",
}

// keyAdministerActions are what the account root delegates to IAM for the key's
// upkeep without changing who may use it: describing and listing, tags, the
// description, aliases, rotation and cancelling a deletion.
var keyAdministerActions = []string{
	"kms:CancelKeyDeletion", "kms:CreateAlias", "kms:DeleteAlias", "kms:DescribeKey", "kms:DisableKeyRotation",
	"kms:EnableKey", "kms:EnableKeyRotation", "kms:Get*", "kms:List*", "kms:TagResource", "kms:UntagResource",
	"kms:UpdateAlias", "kms:UpdateKeyDescription",
}

var adminArnRE = regexp.MustCompile(`^arn:` + `aws[a-z-]*:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_*/-]+$`)

const (
	ssoRegionPath       = "aws-reserved/sso.amazonaws.com/*/"
	minLiteralAdminName = 4
)

// CheckKeyAdmin holds a principal that a key policy names as an administrator to
// what it can safely be: the ARN of an IAM ROLE (never the account's root, a
// user, or `*`), with the account and the role path literal. The wildcards
// allowed are in the role's NAME, for an SSO role whose name ends in a hash
// (`AWSReservedSSO_Admin_*`), which keeps at least four literal characters, and
// for the region of an SSO role's path (`aws-reserved/sso.amazonaws.com/*/`).
func CheckKeyAdmin(arn string) error {
	if !adminArnRE.MatchString(arn) {
		return fmt.Errorf("auditpulumi: %q is not the ARN of an IAM role (arn:aws:iam::<account>:role/<name>): "+
			"the account's root, a user, a wildcard account or a bare `*` is not an administrator", arn)
	}
	_, rolePart, _ := strings.Cut(arn, ":role/")
	path, name := "", rolePart
	if i := strings.LastIndex(rolePart, "/"); i >= 0 {
		path, name = rolePart[:i+1], rolePart[i+1:]
	}
	if strings.Contains(path, "*") && path != ssoRegionPath {
		return fmt.Errorf("auditpulumi: %q has a wildcard in the role's path: a wildcard is for the hash of an SSO role's name, "+
			"and for the region in %s, only", arn, ssoRegionPath)
	}
	if name == "" {
		return fmt.Errorf("auditpulumi: %q names no role", arn)
	}
	if strings.Contains(name, "*") && len(strings.ReplaceAll(name, "*", "")) < minLiteralAdminName {
		return fmt.Errorf("auditpulumi: %q keeps fewer than %d literal characters of the role's name: the pattern would name too many roles",
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

// keyAdminSet validates admins and returns them sorted, without repeats. None is
// refused: a key whose control plane nobody holds cannot be changed again.
func keyAdminSet(field string, admins []string) ([]string, error) {
	if len(admins) == 0 {
		return nil, fmt.Errorf("auditpulumi: %s is required: the control plane of the key (its policy, grants, disabling and deletion) is held to the "+
			"named administrators, and with none the key could not be changed again; name the role that applies the stack and the break-glass role", field)
	}
	if err := checkKeyAdmins(field, admins); err != nil {
		return nil, err
	}
	return dedupe(sortedCopy(admins)), nil
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// controlPlane are the two statements that hold a key's control plane to its
// administrators: an allow of the five actions for the account root, narrowed to
// them, and a deny to everyone else (AWS services acting on their own are not
// named by the condition).
func controlPlane(rootArn string, admins []string) []statement {
	return []statement{
		{
			"Sid": sidControlPlaneAdmins, "Effect": "Allow", "Principal": map[string]any{"AWS": rootArn},
			"Action": append([]string(nil), KeyControlPlaneActions...), "Resource": "*",
			"Condition": map[string]any{"ArnLike": map[string]any{"aws:PrincipalArn": admins}},
		},
		{
			"Sid": sidControlPlaneDeny, "Effect": "Deny", "Principal": map[string]any{"AWS": "*"},
			"Action": append([]string(nil), KeyControlPlaneActions...), "Resource": "*",
			"Condition": map[string]any{
				"ArnNotLike": map[string]any{"aws:PrincipalArn": admins},
				"Bool":       map[string]any{"aws:PrincipalIsAWSService": "false"},
			},
		},
	}
}
