package sluispulumi

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// ExternalReaderRoleType is the Pulumi type token of the ExternalReaderRole component.
const ExternalReaderRoleType = "sluis:aws:ExternalReaderRole"

var iamRoleNamePattern = regexp.MustCompile(`^[A-Za-z0-9+=,.@_-]{1,64}$`)

// ExternalReaderRoleArgs make a role that reads exact `external/<kind>/<id>`
// secrets of an installation. Who may assume it is the caller's choice: a trust
// document of their own (IRSA, EKS Pod Identity, the External Secrets Operator's
// service account) or one given principal.
type ExternalReaderRoleArgs struct {
	// Region, AccountID, Instance and Addresses: see ExternalReadPolicyArgs. The
	// addresses are exact; a wildcard or a prefix is refused.
	Region, AccountID, Instance string
	Addresses                   []string
	// RoleName is the IAM role's name. Default the component's name. It names the
	// role in the key statement, so it is fixed at preview.
	RoleName string
	// TrustPolicy is the role's trust (assume-role) policy document, written by
	// the caller. Exactly one of TrustPolicy and TrustPrincipalArn.
	TrustPolicy pulumi.StringInput
	// TrustPrincipalArn is one exact principal (a role or a user ARN) that may
	// assume the role with sts:AssumeRole. No wildcard.
	TrustPrincipalArn string
	// SecretsKeyArn and ParameterKeyArn: see ExternalReaderArgs.
	SecretsKeyArn   pulumi.StringInput
	ParameterKeyArn string
	// PermissionsBoundaryArn and Tags are the role's.
	PermissionsBoundaryArn string
	Tags                   map[string]string
}

// ExternalReaderRole is the component: the role, its policy and the statement
// the shared key's policy needs for it.
type ExternalReaderRole struct {
	pulumi.ResourceState
	RoleArn, RoleName pulumi.StringOutput
	// PolicyJSON is the attached read policy.
	PolicyJSON pulumi.StringOutput
	// KeyStatement is the statement for the shared key's policy that lets this
	// role, and only it, kms:Decrypt exactly its addresses' parameters through
	// SSM. The estate merges it (KeyPolicyOptions.ReaderStatements, or
	// KeyPolicyDocument's groups); the identity policy alone is not enough, the
	// key policy is a ceiling and grants the account root's delegation only to
	// named principals. It is known at preview: nothing in it is an output.
	KeyStatement map[string]any
}

// ExternalReaderKeyStatement is the key-policy statement for a role that reads
// exactly the given addresses: Decrypt, through SSM, for those parameters.
// The role is named by its exact name, so it need not exist when the key is made.
func ExternalReaderKeyStatement(region, account, instance, roleName string, addresses []string) (map[string]any, error) {
	if !iamRoleNamePattern.MatchString(roleName) {
		return nil, fmt.Errorf("sluispulumi: the role name %q is not an IAM role name", roleName)
	}
	arns, err := (&ExternalReadPolicyArgs{Region: region, AccountID: account, Instance: instance, Addresses: addresses}).validate()
	if err != nil {
		return nil, err
	}
	sid := sidKeyExtReaderPrefix + sidName(strings.Join(regexp.MustCompile(`[^A-Za-z0-9]+`).Split(roleName, -1), "-"))
	return statement{
		"Sid": sid, "Effect": "Allow", "Principal": map[string]any{"AWS": arnPrefix + "iam::" + account + ":root"},
		"Action": []string{kmsDecrypt}, "Resource": "*",
		"Condition": map[string]any{
			condArnEquals: map[string]any{condPrincipalArn: arnPrefix + "iam::" + account + ":role/" + roleName},
			condStringEquals: map[string]any{
				condViaService:   "ssm." + region + ".amazonaws.com",
				condParameterArn: arns,
			},
		},
	}, nil
}

func trustForPrincipal(principal string) (string, error) {
	if !strings.HasPrefix(principal, arnPrefix+"iam::") || strings.ContainsAny(principal, "*?") {
		return "", fmt.Errorf("sluispulumi: ExternalReaderRoleArgs.TrustPrincipalArn needs an exact IAM ARN, got %q", principal)
	}
	// The account's root is an exact ARN and the widest principal there is: every
	// identity of the account that its own IAM policies let assume the role would.
	if rest := strings.TrimPrefix(principal, arnPrefix+"iam::"); !strings.Contains(rest, ":role/") && !strings.Contains(rest, ":user/") {
		return "", fmt.Errorf("sluispulumi: ExternalReaderRoleArgs.TrustPrincipalArn %q is not one role or one user: "+
			"the account's root would let every identity of the account assume the role", principal)
	}
	raw, err := json.Marshal(map[string]any{"Version": polVersion, "Statement": []map[string]any{{
		"Effect": "Allow", "Principal": map[string]any{"AWS": principal}, "Action": "sts:AssumeRole",
	}}})
	return string(raw), err
}

// NewExternalReaderRole makes the reader role of exact `external/` addresses.
func NewExternalReaderRole(ctx *pulumi.Context, name string, args *ExternalReaderRoleArgs, opts ...pulumi.ResourceOption) (*ExternalReaderRole, error) {
	if args == nil {
		return nil, errors.New("sluispulumi: ExternalReaderRoleArgs is nil")
	}
	roleName := args.RoleName
	if roleName == "" {
		roleName = name
	}
	if (args.TrustPolicy == nil) == (args.TrustPrincipalArn == "") {
		return nil, errors.New("sluispulumi: ExternalReaderRoleArgs: set exactly one of TrustPolicy and TrustPrincipalArn " +
			"(who may assume the role is the caller's choice)")
	}
	trust := args.TrustPolicy
	if args.TrustPrincipalArn != "" {
		doc, err := trustForPrincipal(args.TrustPrincipalArn)
		if err != nil {
			return nil, err
		}
		trust = pulumi.String(doc)
	}
	stmt, err := ExternalReaderKeyStatement(args.Region, args.AccountID, args.Instance, roleName, args.Addresses)
	if err != nil {
		return nil, err
	}
	if _, err := (&ExternalReadPolicyArgs{
		Region: args.Region, AccountID: args.AccountID, Instance: args.Instance, Addresses: args.Addresses,
		ParameterKeyArn: args.ParameterKeyArn,
	}).validate(); err != nil {
		return nil, err
	}
	out := &ExternalReaderRole{KeyStatement: stmt}
	if err := ctx.RegisterComponentResource(ExternalReaderRoleType, name, out, opts...); err != nil {
		return nil, err
	}
	rargs := &iam.RoleArgs{Name: pulumi.String(roleName), AssumeRolePolicy: trust, Tags: tagMap(args.Tags)}
	if args.PermissionsBoundaryArn != "" {
		rargs.PermissionsBoundary = pulumi.String(args.PermissionsBoundaryArn)
	}
	role, err := iam.NewRole(ctx, name+"-role", rargs, pulumi.Parent(out))
	if err != nil {
		return nil, fmt.Errorf("sluis external reader role: %w", err)
	}
	rd, err := NewExternalReader(ctx, name, &ExternalReaderArgs{
		RoleName: role.Name, SecretsKeyArn: args.SecretsKeyArn, Region: args.Region, AccountID: args.AccountID,
		Instance: args.Instance, Addresses: args.Addresses, ParameterKeyArn: args.ParameterKeyArn,
	}, pulumi.Parent(out))
	if err != nil {
		return nil, err
	}
	out.RoleArn, out.RoleName, out.PolicyJSON = role.Arn, role.Name, rd.PolicyJSON
	if err := ctx.RegisterResourceOutputs(out, pulumi.Map{"roleArn": out.RoleArn, "roleName": out.RoleName, "policyJson": out.PolicyJSON}); err != nil {
		return nil, err
	}
	return out, nil
}
