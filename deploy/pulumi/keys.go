package sluispulumi

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/kms"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// The purposes of the keys block (storage/schemas/keys.schema.json) this
// library grants, and the `purpose` of the encryption context each is used
// under: the runtime's default context is {instance, purpose}.
const (
	// SignPurpose is the key that wraps the ring's token-signing key pairs: the
	// runtime generates a pair locally and wraps its private half with
	// kms:Encrypt under {instance, purpose: sign}.
	SignPurpose = "sign"
)

// KeysArgs are the KMS keys the ESTATE supplies, named by alias: the library
// creates no key for them, resolves each alias to the key behind it
// (aws.kms.LookupAlias) to grant IAM on that key's ARN, and passes the aliases
// to the runtime in the service document's `keys:` block
// (`keys: {adapter: kms, sign: alias/…}`, and Secrets as `secrets.kmsKeyId`). An alias is not a
// permission: the grant is on the key, and re-pointing an alias moves the
// function to the new key at the next apply.
//
// Both keys are symmetric (ENCRYPT_DECRYPT). Leave Keys nil to keep the keys the
// library creates itself, which is deprecated (see SigningKeyAlias).
type KeysArgs struct {
	// Sign is the alias of the key that wraps the ring's signing key pairs.
	// Required. The function may kms:Encrypt, kms:Decrypt and
	// kms:GenerateDataKey with it only under the context
	// {instance: <Instance>, purpose: sign} and no other context keys.
	Sign string
	// Secrets is the alias of the key the SSM secrets store encrypts its
	// SecureString parameters with: the service document's `secrets.kmsKeyId`,
	// passed as KeyId on PutParameter. Optional: unset, the secrets use
	// ParameterKeyArn or the AWS-managed key. Exclusive with ParameterKeyArn.
	// SSM encrypts under the context {PARAMETER_ARN}, so the grant is through SSM
	// only, for the installation's parameters, and has no instance/purpose context.
	Secrets string
	// LegacySigningContext keeps the grant on the Sign key under the older
	// context {purpose: sluis-signing, alg, kid} (GenerateDataKeyPair and
	// Decrypt), which ring entries written before the runtime wrapped locally
	// are opened with. Nil is true, so an existing stack keeps opening its ring.
	// Set false once the ring has rotated past the old entries: the statement
	// then goes.
	LegacySigningContext *bool
}

var aliasPattern = regexp.MustCompile(`^alias/[A-Za-z0-9/_-]{1,250}$`)

func (k *KeysArgs) validate() error {
	if k == nil {
		return nil
	}
	check := func(field, alias string, required bool) error {
		switch {
		case alias == "" && required:
			return fmt.Errorf("sluispulumi: LambdaArgs.Keys.%s is required", field)
		case alias == "":
			return nil
		case strings.HasPrefix(alias, "arn:") || !aliasPattern.MatchString(alias) || strings.HasPrefix(alias, "alias/aws/"):
			return fmt.Errorf("sluispulumi: LambdaArgs.Keys.%s %q is not a customer alias (alias/<name>): a key is named by alias, "+
				"never by ARN or key id", field, alias)
		}
		return nil
	}
	return errors.Join(check("Sign", k.Sign, true), check("Secrets", k.Secrets, false))
}

func (k *KeysArgs) legacy() bool { return k.LegacySigningContext == nil || *k.LegacySigningContext }

// contextKeys are the encryption context's keys the grants admit, and only they.
var contextKeys = []string{"instance", "purpose"}

// contextStatement is the use of a symmetric key under the runtime's default
// context {instance, purpose}: Encrypt, Decrypt and GenerateDataKey, on that key
// only, and only with exactly those context keys and values.
func contextStatement(sid, keyArn, instance, purpose string) statement {
	return statement{
		"Sid":      sid,
		"Effect":   "Allow",
		"Action":   []string{kmsEncrypt, kmsDecrypt, kmsGenerateDK},
		"Resource": keyArn,
		"Condition": map[string]any{
			"StringEquals": map[string]any{
				"kms:EncryptionContext:instance": instance,
				"kms:EncryptionContext:purpose":  purpose,
			},
			"ForAllValues:StringEquals": map[string]any{"kms:EncryptionContextKeys": contextKeys},
		},
	}
}

// secretsKeyStatement is the use of the secrets key by the function: through SSM
// only (`kms:ViaService`), for the parameters under the installation's root
// (SSM puts the parameter's ARN in the encryption context).
func secretsKeyStatement(keyArn, region, account, instance string) statement {
	return statement{
		"Sid":      sidKeysSecrets,
		"Effect":   "Allow",
		"Action":   []string{kmsEncrypt, kmsDecrypt, kmsGenerateDK},
		"Resource": keyArn,
		"Condition": map[string]any{
			"StringEquals": map[string]any{"kms:ViaService": "ssm." + region + ".amazonaws.com"},
			"StringLike": map[string]any{
				"kms:EncryptionContext:PARAMETER_ARN": arnPrefix + "ssm:" + region + ":" + account + ":parameter" + SSMRoot(instance) + "/*",
			},
		},
	}
}

const (
	kmsGenerateDK  = "kms:GenerateDataKey"
	sidKeysSign    = "SluisKeysSign"
	sidKeysSecrets = "SluisKeysSecrets"
)

// keyGrants are the resolved keys of KeysArgs.
type keyGrants struct {
	signArn, secretsArn string
	legacy              bool
}

func (g *keyGrants) statements(region, account, instance string) []statement {
	if g == nil {
		return nil
	}
	st := []statement{contextStatement(sidKeysSign, g.signArn, instance, SignPurpose)}
	if g.legacy {
		st = append(st, wrappedSigningStatement(g.signArn))
	}
	if g.secretsArn != "" {
		st = append(st, secretsKeyStatement(g.secretsArn, region, account, instance))
	}
	return st
}

// lookupKeys resolves the aliases to the ARNs of the keys behind them.
func lookupKeys(ctx *pulumi.Context, k *KeysArgs, opts ...pulumi.InvokeOption) (sign, secrets pulumi.StringOutput, err error) {
	one := func(alias string) pulumi.StringOutput {
		return kms.LookupAliasOutput(ctx, kms.LookupAliasOutputArgs{Name: pulumi.String(alias)}, opts...).TargetKeyArn()
	}
	secrets = pulumi.String("").ToStringOutput()
	sign = one(k.Sign)
	if k.Secrets != "" {
		secrets = one(k.Secrets)
	}
	return sign, secrets, nil
}

// keysBlock is the service document's `keys:` block.
func (k *KeysArgs) keysBlock() map[string]any {
	return map[string]any{"adapter": "kms", "sign": k.Sign}
}

// grantsOf is the resolved grants of Keys, nil without Keys.
func grantsOf(k *KeysArgs, signArn, secretsArn string) *keyGrants {
	if k == nil {
		return nil
	}
	return &keyGrants{signArn: signArn, secretsArn: secretsArn, legacy: k.legacy()}
}
