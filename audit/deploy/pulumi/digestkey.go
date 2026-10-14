package auditpulumi

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/kms"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// DigestKeyArgs is the key the audit installation's digest job signs its chain
// with, and the alias that names it.
//
// The library creates no key for New: the estate does (see KeysArgs). DigestKey
// is the estate's helper for the one key whose shape does not vary, an
// ECC_NIST_P256 SIGN_VERIFY key in the backup account, so that sealing the
// archive is a privilege held in a different account from writing it. Its
// resources are declared directly under the caller's options, not under a
// component, so an estate that already has them keeps every URN and physical
// name: the hooks below set the logical names, aliases and import ids.
type DigestKeyArgs struct {
	// Description is the key's description, verbatim. Required. KMS takes it at
	// creation only: a change forces a replacement of the key, so an estate that
	// has the key passes the text it has.
	Description string
	// BackupAccountID is the 12-digit account whose root administers the key
	// (`kms:*`). The key policy names it and nobody else. Required.
	BackupAccountID string

	// AliasName is the alias's physical name, `alias/<name>`. Default
	// `alias/<name>` of the name DigestKey is called with.
	AliasName string
	// KeyLogicalName and AliasLogicalName are the resources' logical names, the
	// last part of their URNs. Defaults: the name, and `<name>-alias`.
	KeyLogicalName, AliasLogicalName string
	// KeyAliases and AliasAliases are [pulumi.Alias] values for a resource whose
	// URN is being moved (a rename, a new parent).
	KeyAliases, AliasAliases []pulumi.Alias
	// KeyImport and AliasImport are the key id and the alias name to import,
	// when the resource exists and is not in the stack's state.
	KeyImport, AliasImport string
	// KeyOptions and AliasOptions are further options for one resource, after
	// the shared ones DigestKey is called with. RetainOnDelete is always set on
	// the key: destroying it would leave the chain it signed unverifiable.
	KeyOptions, AliasOptions []pulumi.ResourceOption
}

// DigestKey is the key and alias DigestKey made.
type DigestKey struct {
	Key   *kms.Key
	Alias *kms.Alias
	// Policy is the key policy text the key was created with.
	Policy string
}

var accountIDRE = regexp.MustCompile(`^[0-9]{12}$`)

// NewDigestKey declares the digest signing key and its alias. opts apply to both
// (the provider, the parent). The key is ECC_NIST_P256, SIGN_VERIFY, retained on
// delete, with DigestKeyPolicy as its policy; the alias is `alias/<name>` unless
// AliasName says otherwise.
func NewDigestKey(ctx *pulumi.Context, name string, args *DigestKeyArgs, opts ...pulumi.ResourceOption) (*DigestKey, error) {
	if args == nil {
		return nil, errors.New("auditpulumi: DigestKeyArgs is required")
	}
	if name == "" {
		return nil, errors.New("auditpulumi: DigestKey name is required")
	}
	if args.Description == "" {
		return nil, errors.New("auditpulumi: DigestKeyArgs.Description is required: KMS takes it at creation only, " +
			"so it is given verbatim (an estate that has the key passes the text it has)")
	}
	if !accountIDRE.MatchString(args.BackupAccountID) {
		return nil, fmt.Errorf("auditpulumi: DigestKeyArgs.BackupAccountID %q must be a 12-digit account id", args.BackupAccountID)
	}
	alias := args.AliasName
	if alias == "" {
		alias = "alias/" + name
	}
	if err := checkAlias("DigestKeyArgs.AliasName", alias); err != nil {
		return nil, err
	}
	keyName, aliasName := orDefault(args.KeyLogicalName, name), orDefault(args.AliasLogicalName, name+"-alias")

	kopts := append([]pulumi.ResourceOption{}, opts...)
	kopts = append(kopts, pulumi.RetainOnDelete(true))
	if len(args.KeyAliases) > 0 {
		kopts = append(kopts, pulumi.Aliases(args.KeyAliases))
	}
	if args.KeyImport != "" {
		kopts = append(kopts, pulumi.Import(pulumi.ID(args.KeyImport)))
	}
	kopts = append(kopts, args.KeyOptions...)

	policy := DigestKeyPolicy(args.BackupAccountID)
	key, err := kms.NewKey(ctx, keyName, &kms.KeyArgs{
		Description:           pulumi.String(args.Description),
		CustomerMasterKeySpec: pulumi.String("ECC_NIST_P256"), //nolint:staticcheck // the field the existing state has
		KeyUsage:              pulumi.String("SIGN_VERIFY"),
		Policy:                pulumi.String(policy),
	}, kopts...)
	if err != nil {
		return nil, fmt.Errorf("auditpulumi: digest signing key: %w", err)
	}

	aopts := append([]pulumi.ResourceOption{}, opts...)
	if len(args.AliasAliases) > 0 {
		aopts = append(aopts, pulumi.Aliases(args.AliasAliases))
	}
	if args.AliasImport != "" {
		aopts = append(aopts, pulumi.Import(pulumi.ID(args.AliasImport)))
	}
	aopts = append(aopts, args.AliasOptions...)
	al, err := kms.NewAlias(ctx, aliasName, &kms.AliasArgs{
		Name:        pulumi.String(alias),
		TargetKeyId: key.KeyId,
	}, aopts...)
	if err != nil {
		return nil, fmt.Errorf("auditpulumi: digest signing key alias: %w", err)
	}
	return &DigestKey{Key: key, Alias: al, Policy: policy}, nil
}

func orDefault(v, d string) string {
	if v != "" {
		return v
	}
	return d
}

// DigestKeyPolicy is the digest signing key's policy: the backup account's root
// administers the key (`kms:*`) and no other principal is named. The digest and
// verify roles are not in it: KMS refuses a policy that names a principal that
// does not exist, and the chain stays verifiable by the key's public half, which
// the estate exports.
func DigestKeyPolicy(backupAccountID string) string {
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Sid":       "BackupAccountKeyAdmin",
			"Effect":    "Allow",
			"Principal": map[string]any{"AWS": "arn:" + "aws:iam::" + backupAccountID + ":root"},
			"Action":    "kms:*",
			"Resource":  "*",
		}},
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		panic(err) // maps of strings
	}
	return string(out)
}
