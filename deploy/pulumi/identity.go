package sluispulumi

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/eks"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// KubernetesIdentityType is the Pulumi type token of the identity component.
const KubernetesIdentityType = "sluis:aws:KubernetesIdentity"

// KubernetesIdentityArgs is the one EKS Pod Identity role of the one sluis pod: the
// service and its controllers run in one process, in one Deployment, as one
// ServiceAccount.
type KubernetesIdentityArgs struct {
	// ClusterName is the EKS cluster the associations are made in. Required.
	ClusterName string
	// ClusterArn and AccountID pin each role's trust policy to the cluster: the
	// source ARN and the source account EKS stamps on every assume. Required.
	ClusterArn string
	AccountID  string
	// Region is set on each association when it is not empty; empty leaves the
	// provider's region in force.
	Region string

	// Namespace is the namespace of the ServiceAccount. Required.
	Namespace string

	// PermissionsBoundaryArn is the boundary of every role. Default none; the
	// estate's rule is that a role has one (gitops uses `pb@default`).
	PermissionsBoundaryArn string

	// RoleNamePrefix starts the role's name, `<prefix>-sluis`; the role's managed
	// policy has the role's name. Default: the name the component is registered
	// under.
	RoleNamePrefix string

	// ServiceAccount is the name of the ServiceAccount the pod runs as, in
	// Namespace. Required. A ServiceAccount takes ONE association, and the
	// controllers run as it too: their code has all the role's permissions.
	ServiceAccount string
	// Description is the role's managed policy's description, which IAM cannot
	// change once set (a change replaces the policy). Default says what the
	// role is allowed, in one sentence.
	Description string

	// Storage is the blob bucket: every process keeps its reports in it. Required.
	Storage *StorageGrant
	// SigningKeyArns are the token-signing keys (Lambda's SigningKeyArn and
	// SigningKeyRS256Arn). When any is set the role may kms:Sign and
	// kms:GetPublicKey with them. Optional.
	SigningKeyArns []pulumi.StringInput
	// WrappedSigningKeyArn is the symmetric key of the `kms-wrapped` signing
	// adapter: Lambda's WrappedSigningKeyArn, whose policy reserves the signing
	// context to the signing roles (this role must be among
	// WrappedSigningArgs.AdditionalSigningRoleArns, or named in the denial merged
	// into a shared key). When set the role may generate data key pairs and
	// decrypt with it, under the encryption context purpose=sluis-signing.
	// Optional.
	WrappedSigningKeyArn pulumi.StringInput

	// Instance, when set, is the installation's name (`acme`, `prod`) and gives
	// the role the SSM grants the Lambda role has under `/sluis/<instance>`
	// (layout v3), for a pod whose `secrets` or Secrets adapter is `ssm`: read and
	// write under private/credentials/* and export/*, read under private/config/*,
	// all scoped to that root. Needs Region. Unset, the role has no SSM grant.
	Instance string
	// ParameterKeyArn is the customer-managed key SecureString parameters under
	// /sluis are encrypted with; with Instance, the role may use it through SSM
	// only, for the parameters under its own prefixes. Optional.
	ParameterKeyArn string
	// State is the DynamoDB table of the State port. Nil when State is not in
	// DynamoDB (it is in ConfigMaps or memory), and the roles then carry no DynamoDB grant.
	State *StateGrant
}

// KubernetesIdentity is the component. Its fields are the outputs.
type KubernetesIdentity struct {
	pulumi.ResourceState

	RoleArn  pulumi.StringOutput
	RoleName pulumi.StringOutput
}

// NewKubernetesIdentity creates the one role of the one pod, its policy and its
// Pod Identity association:
//
//   - a customer-managed policy and the role it is attached to, both named
//     `<prefix>-sluis`;
//   - a trust policy that lets EKS Pod Identity assume it for this cluster and
//     for this namespace's one ServiceAccount, and for nothing else;
//   - one association of that ServiceAccount with the role.
//
// The policy grants the storage (S3 get, put and delete of objects, list of the
// bucket), kms:Sign and kms:GetPublicKey on SigningKeyArns (or the wrapped key's
// two calls under their encryption context), when State is given the DynamoDB
// adapter's item calls and DescribeTable on the one table, and, when Instance is
// given, the SSM grants of the Lambda role under /sluis/<instance>. The
// controllers run in the same pod, so their code has all of it. Nothing else.
func NewKubernetesIdentity(ctx *pulumi.Context, name string, args *KubernetesIdentityArgs, opts ...pulumi.ResourceOption) (*KubernetesIdentity, error) {
	if args == nil {
		return nil, errors.New("sluispulumi: KubernetesIdentityArgs is nil")
	}
	a := *args
	var missing []string
	for k, v := range map[string]string{
		"ClusterName": a.ClusterName, "ClusterArn": a.ClusterArn, "AccountID": a.AccountID,
		"Namespace": a.Namespace, "ServiceAccount": a.ServiceAccount,
	} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("sluispulumi: KubernetesIdentityArgs: required and empty: %v", sortedStrings(missing))
	}
	if a.Storage == nil || (a.Storage.BucketArn == nil && a.Storage.External == nil) {
		return nil, errors.New("sluispulumi: KubernetesIdentityArgs.Storage is required (Storage.Grant())")
	}
	if a.Storage.External != nil && a.Instance == "" {
		return nil, errors.New("sluispulumi: KubernetesIdentityArgs.Instance is required with external blobs: " +
			"the role reads their credentials' address under the installation's SSM root")
	}
	if a.State != nil && a.State.TableArn == nil {
		return nil, errors.New("sluispulumi: KubernetesIdentityArgs.State has no TableArn (State.Grant())")
	}
	if a.Instance != "" {
		if !validInstance(a.Instance) {
			return nil, fmt.Errorf("sluispulumi: KubernetesIdentityArgs.Instance %q is lower-case letters, digits and dashes, "+
				"at most 32, and not private or export", a.Instance)
		}
		if a.Region == "" {
			return nil, errors.New("sluispulumi: KubernetesIdentityArgs.Region is required with Instance: the SSM grants name it")
		}
	}
	if a.RoleNamePrefix == "" {
		a.RoleNamePrefix = name
	}
	out := &KubernetesIdentity{}
	if err := ctx.RegisterComponentResource(KubernetesIdentityType, name, out, opts...); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(map[string]any{
		"Version": polVersion,
		"Statement": []map[string]any{{
			"Effect":    "Allow",
			"Principal": map[string]any{"Service": "pods.eks.amazonaws.com"},
			"Action":    []string{"sts:AssumeRole", "sts:TagSession"},
			"Condition": map[string]any{
				"StringEquals": map[string]any{
					"aws:SourceAccount":                         a.AccountID,
					"aws:RequestTag/kubernetes-namespace":       a.Namespace,
					"aws:RequestTag/kubernetes-service-account": a.ServiceAccount,
				},
				"ArnEquals": map[string]any{"aws:SourceArn": a.ClusterArn},
			},
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("render trust policy: %w", err)
	}
	roleName := a.RoleNamePrefix + "-sluis"
	desc := a.Description
	if desc == "" {
		desc = "sluis, the issuer, the console and the controllers in one pod: its reports in the sluis bucket and its table; nothing else"
	}
	role, err := newPodIdentity(ctx, out, &a, roleName, desc, string(raw))
	if err != nil {
		return nil, fmt.Errorf("sluis identity: %w", err)
	}
	out.RoleArn, out.RoleName = role.Arn, role.Name
	if err := ctx.RegisterResourceOutputs(out, pulumi.Map{"roleArn": role.Arn, "roleName": role.Name}); err != nil {
		return nil, err
	}
	return out, nil
}

// newPodIdentity is the policy, role, attachment and association of the pod. The
// children are named `<role>-policy`, `<role>-role`, `<role>-attachment` and
// `<role>-pia`.
func newPodIdentity(ctx *pulumi.Context, parent *KubernetesIdentity, a *KubernetesIdentityArgs,
	roleName, desc, trust string) (*iam.Role, error) {
	child := pulumi.Parent(parent)

	stateTable, stateKey := pulumi.StringInput(pulumi.String("")), pulumi.StringInput(pulumi.String(""))
	if a.State != nil {
		stateTable = a.State.TableArn
		if a.State.KeyArn != nil {
			stateKey = a.State.KeyArn
		}
	}
	withState := a.State != nil
	withWrapped := a.WrappedSigningKeyArn != nil
	wrapped := pulumi.StringInput(pulumi.String(""))
	if withWrapped {
		wrapped = a.WrappedSigningKeyArn
	}
	bucketArn := a.Storage.BucketArn
	if bucketArn == nil {
		bucketArn = pulumi.String("")
	}
	inputs := []any{bucketArn, stateTable, stateKey, wrapped}
	for _, k := range a.SigningKeyArns {
		inputs = append(inputs, k)
	}
	doc := pulumi.All(inputs...).ApplyT(func(v []any) (string, error) {
		var st []statement
		if a.Storage.External == nil {
			st = storageStatements(v[0].(string))
		} else {
			st = credentialsStatements(a.Storage.External, a.Region, a.AccountID, a.Instance, a.ParameterKeyArn)
		}
		if withState {
			st = append(st, stateStatements(v[1].(string), v[2].(string))...)
		}
		if len(v) > 4 {
			keys := make([]string, 0, len(v)-4)
			for _, k := range v[4:] {
				keys = append(keys, k.(string))
			}
			st = append(st, signingStatement(keys))
		}
		if withWrapped {
			st = append(st, wrappedSigningStatement(v[3].(string)))
		}
		if a.Instance != "" {
			st = append(st, ssmStatements(a.Region, a.AccountID, a.Instance, a.ParameterKeyArn)...)
		}
		return document(st)
	}).(pulumi.StringOutput)

	policy, err := iam.NewPolicy(ctx, roleName+"-policy", &iam.PolicyArgs{
		Name:        pulumi.String(roleName),
		Description: pulumi.String(desc),
		Policy:      doc,
	}, child)
	if err != nil {
		return nil, fmt.Errorf("create policy: %w", err)
	}

	rargs := &iam.RoleArgs{
		Name:             pulumi.String(roleName),
		AssumeRolePolicy: pulumi.String(trust),
	}
	if a.PermissionsBoundaryArn != "" {
		rargs.PermissionsBoundary = pulumi.String(a.PermissionsBoundaryArn)
	}
	role, err := iam.NewRole(ctx, roleName+"-role", rargs, child)
	if err != nil {
		return nil, fmt.Errorf("create role: %w", err)
	}

	if _, err := iam.NewRolePolicyAttachment(ctx, roleName+"-attachment", &iam.RolePolicyAttachmentArgs{
		Role:      role.Name,
		PolicyArn: policy.Arn,
	}, child); err != nil {
		return nil, fmt.Errorf("attach policy: %w", err)
	}

	pia := &eks.PodIdentityAssociationArgs{
		ClusterName:    pulumi.String(a.ClusterName),
		Namespace:      pulumi.String(a.Namespace),
		ServiceAccount: pulumi.String(a.ServiceAccount),
		RoleArn:        role.Arn,
	}
	if a.Region != "" {
		pia.Region = pulumi.String(a.Region)
	}
	if _, err := eks.NewPodIdentityAssociation(ctx, roleName+"-pia", pia, child); err != nil {
		return nil, fmt.Errorf("associate %s/%s: %w", a.Namespace, a.ServiceAccount, err)
	}
	return role, nil
}
