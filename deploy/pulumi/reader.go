package sluispulumi

import (
	"errors"
	"fmt"
	"sort"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// ExternalReaderType is the Pulumi type token of the ExternalReader component.
const ExternalReaderType = "sluis:aws:ExternalReader"

// ExternalReadPolicyArgs is what a consumer's role needs to read exact
// `external/<kind>/<id>` secrets of an installation: values the estate seeds for
// a workload (a provider's client secret, a deploy key), as opposed to what
// sluis keeps for itself.
type ExternalReadPolicyArgs struct {
	// Region, AccountID and Instance name the parameters: the secrets root is
	// `/sluis/<instance>`. Required.
	Region, AccountID, Instance string
	// Addresses are the exact secrets, `external/<kind>/<id>`. At least one. A
	// wildcard, a prefix or a repeated address is refused: a role that may read
	// "everything under external/" is not what this is.
	Addresses []string
	// SecretsKeyArn is the key the secrets are encrypted with (the one behind
	// LambdaArgs.Keys.Secrets). Set, the policy also lets the role kms:Decrypt
	// with it, only under the context {instance, purpose: conceal}. Unset, the
	// secrets use SSM's own key and no key grant is needed (or ParameterKeyArn).
	SecretsKeyArn string
	// ParameterKeyArn is a customer-managed key the SecureString parameters are
	// encrypted with, decrypted through SSM only for exactly these parameters.
	// Exclusive with SecretsKeyArn.
	ParameterKeyArn string
}

func (a *ExternalReadPolicyArgs) validate() ([]string, error) {
	if a == nil {
		return nil, errors.New("sluispulumi: ExternalReadPolicyArgs is nil")
	}
	var errs []error
	for k, v := range map[string]string{"Region": a.Region, "AccountID": a.AccountID} {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", k))
		}
	}
	if !validInstance(a.Instance) {
		errs = append(errs, fmt.Errorf("Instance %q is lower-case letters, digits and dashes, at most 32, and not private or export", a.Instance))
	}
	if len(a.Addresses) == 0 {
		errs = append(errs, errors.New("Addresses is empty: a reader of nothing is a mistake"))
	}
	if a.SecretsKeyArn != "" && a.ParameterKeyArn != "" {
		errs = append(errs, errors.New("SecretsKeyArn and ParameterKeyArn are two ways to encrypt the same parameters: set one"))
	}
	seen := map[string]bool{}
	var arns []string
	for _, addr := range a.Addresses {
		switch {
		case !validAddress("external", addr):
			errs = append(errs, fmt.Errorf("address %q is not external/<kind>/<id> (lower-case letters, digits, - . _; no wildcard, no prefix)", addr))
		case seen[addr]:
			errs = append(errs, fmt.Errorf("address %q is listed twice", addr))
		default:
			seen[addr] = true
			arns = append(arns, secretParameterArn(a.Region, a.AccountID, a.Instance, addr))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("sluispulumi: ExternalReadPolicyArgs: %w", err)
	}
	sort.Strings(arns)
	return arns, nil
}

// ExternalReadPolicy is the IAM policy document a consumer's role attaches to
// read exactly the `external/` addresses given: ssm:GetParameter on those
// parameters' ARNs, and, with SecretsKeyArn, kms:Decrypt on the secrets key
// under the context {instance, purpose: conceal} and no other context key. No
// wildcard: no GetParametersByPath, no prefix, no other action.
func ExternalReadPolicy(a ExternalReadPolicyArgs) (string, error) {
	arns, err := a.validate()
	if err != nil {
		return "", err
	}
	st := []statement{{"Sid": "SluisExternalParameters", "Effect": "Allow", "Action": ssmGetParameter, "Resource": arns}}
	switch {
	case a.SecretsKeyArn != "":
		st = append(st, statement{
			"Sid": "SluisExternalSecretsKey", "Effect": "Allow", "Action": kmsDecrypt, "Resource": a.SecretsKeyArn,
			"Condition": map[string]any{
				"StringEquals": map[string]any{
					"kms:EncryptionContext:instance": a.Instance, "kms:EncryptionContext:purpose": SecretsPurpose,
				},
				"ForAllValues:StringEquals": map[string]any{"kms:EncryptionContextKeys": contextKeys},
			},
		})
	case a.ParameterKeyArn != "":
		st = append(st, parameterKeyStatements(a.ParameterKeyArn, []string{kmsDecrypt}, arns)...)
	}
	return document(st)
}

// ExternalReaderArgs attaches ExternalReadPolicy to a role.
type ExternalReaderArgs struct {
	// RoleName is the consumer's role. Required.
	RoleName pulumi.StringInput
	// PolicyName names the inline policy. Default "sluis-external-<name>".
	PolicyName string
	// SecretsKeyArn is the key behind LambdaArgs.Keys.Secrets, as an input so
	// that it can be the resolved alias; see ExternalReadPolicyArgs.
	SecretsKeyArn pulumi.StringInput
	// Region, AccountID, Instance, Addresses and ParameterKeyArn: see
	// ExternalReadPolicyArgs.
	Region, AccountID, Instance string
	Addresses                   []string
	ParameterKeyArn             string
}

// ExternalReader is the component. PolicyJSON is the attached document.
type ExternalReader struct {
	pulumi.ResourceState
	PolicyJSON pulumi.StringOutput
}

// NewExternalReader attaches the reader policy of exact `external/` addresses to
// a role, as an inline role policy.
func NewExternalReader(ctx *pulumi.Context, name string, args *ExternalReaderArgs, opts ...pulumi.ResourceOption) (*ExternalReader, error) {
	if args == nil || args.RoleName == nil {
		return nil, errors.New("sluispulumi: ExternalReaderArgs.RoleName is required")
	}
	base := ExternalReadPolicyArgs{
		Region: args.Region, AccountID: args.AccountID, Instance: args.Instance,
		Addresses: args.Addresses, ParameterKeyArn: args.ParameterKeyArn,
	}
	key := args.SecretsKeyArn
	if key == nil {
		key = pulumi.String("")
	}
	// Validated now, before anything is registered, with a stand-in key when
	// there is one (the real ARN may be unknown until apply).
	check := base
	if args.SecretsKeyArn != nil {
		check.SecretsKeyArn = "arn"
	}
	if _, err := check.validate(); err != nil {
		return nil, err
	}
	out := &ExternalReader{}
	if err := ctx.RegisterComponentResource(ExternalReaderType, name, out, opts...); err != nil {
		return nil, err
	}
	out.PolicyJSON = key.ToStringOutput().ApplyT(func(k string) (string, error) {
		p := base
		p.SecretsKeyArn = k
		return ExternalReadPolicy(p)
	}).(pulumi.StringOutput)
	pn := args.PolicyName
	if pn == "" {
		pn = "sluis-external-" + name
	}
	if _, err := iam.NewRolePolicy(ctx, name+"-policy", &iam.RolePolicyArgs{
		Name: pulumi.String(pn), Role: args.RoleName, Policy: out.PolicyJSON,
	}, pulumi.Parent(out)); err != nil {
		return nil, fmt.Errorf("sluis external reader policy: %w", err)
	}
	if err := ctx.RegisterResourceOutputs(out, pulumi.Map{"policyJson": out.PolicyJSON}); err != nil {
		return nil, err
	}
	return out, nil
}
