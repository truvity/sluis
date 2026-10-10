package sluispulumi

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
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

// KeyRole is one function's role as the shared key's policy names it: the role
// (its name and the modules it hosts) and the ARN the policy admits. The policy
// compares aws:PrincipalArn, so the role need not exist when the key is made.
type KeyRole struct {
	Role Role
	// Arn is the role's ARN, `arn:aws:iam::<account>:role/[path/]<name>`.
	Arn string
	// MinterRefs are the minter parameters (`internal/cloudflare/<account>/minter`,
	// as in ModuleEnv.MinterRefs) the role reads for `credentials.preset` without
	// hosting the Cloudflare module: Decrypt on exactly those, through SSM.
	MinterRefs []string
}

// KeyPolicyArgs is what SluisKeyPolicyStatements renders the statements of the
// shared key from.
type KeyPolicyArgs struct {
	Region, Account string
	// Modules names the installation (its Instance is the context's `instance`
	// and the SSM root).
	Modules ModuleSet
	// Layout says which parameter addresses the roles use; empty is v4. v4 gives
	// the role that hosts oidc the whole of internal/ and external/ (as its IAM
	// grant does); v5 gives each role its hosted modules only.
	Layout Layout
	// Roles are the functions' roles, one per function: the issuer, Cloudflare,
	// backup, restore. Backup and restore also get the cross-grants of
	// CrossGrants (read-all, write-all).
	Roles []KeyRole
	// ExternalReaders are the ARNs of the roles that read the exported documents
	// (the External Secrets readers). They decrypt `external/*` and nothing else.
	ExternalReaders []string
	// Admins are the operator's roles, as ARN patterns (`*` allowed, so an SSO
	// role with a hash suffix is a pattern). They seed parameters and read the two
	// the stack manages (the state secret and the recovery password).
	Admins []string
	// Breakglass are the emergency roles, as ARN patterns: the function's
	// operations on every parameter, through SSM.
	Breakglass []string
	// DenyOtherActions adds the deny that holds each sign role to the key's three
	// calls and DescribeKey. Off by default: it is a statement about the sluis
	// roles only, but a role an estate shares with another usage would lose it.
	DenyOtherActions bool
}

// The Sids of the statements SluisKeyPolicyStatements returns. All start with
// `SluisKey`; the role names are camel-cased into the Sid.
const (
	sidKeyRolePrefix       = "SluisKeyRole"
	sidKeyReadPrefix       = "SluisKeyReadAll"
	sidKeyWritePrefix      = "SluisKeyWriteAll"
	sidKeyMinterPrefix     = "SluisKeyMinter"
	sidKeyExternalReaders  = "SluisKeyExternalReaders"
	sidKeyAdminSeeds       = "SluisKeyAdminSeeds"
	sidKeyAdminReads       = "SluisKeyAdminReads"
	sidKeyBreakglass       = "SluisKeyBreakglass"
	sidKeySignAllow        = "SluisKeySign"
	sidKeySignReserved     = "SluisKeySignContextReserved"
	sidKeySignPurposeOnly  = "SluisKeySignDirectPurposeOnly"
	sidKeySignContextOnly  = "SluisKeySignDirectContextKeysOnly"
	sidKeyRolesNothingElse = "SluisKeyRolesNothingElse"
	kmsDescribe            = "kms:DescribeKey"
	condPrincipalArn       = "aws:PrincipalArn"
	condViaService         = "kms:ViaService"
	condParameterArn       = "kms:EncryptionContext:PARAMETER_ARN"
	condContextKeys        = "kms:EncryptionContextKeys"
	condContextPurpose     = "kms:EncryptionContext:purpose"
	condContextInstance    = "kms:EncryptionContext:instance"
	condArnEquals          = "ArnEquals"
	condArnLike            = "ArnLike"
	condStringEquals       = "StringEquals"
	condStringLike         = "StringLike"
)

// SluisKeyPolicyStatements are the statements sluis needs in the key policy of
// the estate's ONE shared symmetric key (the key behind `Keys.Sign` and
// `Keys.Secrets`). Each is an Allow for the account root narrowed by
// aws:PrincipalArn, so a role that does not exist yet is a condition value and
// the key can be made first; each still needs the role's own IAM policy, which
// the library writes (the key policy is a ceiling, not a grant).
//
//   - SluisKeyRole<Role>: the role's own parameters (its hosted modules'
//     `internal/<module>/*` and `external/<module>/*`), Encrypt, Decrypt,
//     GenerateDataKey and DescribeKey, through SSM only;
//   - SluisKeyReadAll<Role> (backup): Decrypt of every `internal/*` and
//     `external/*` parameter, through SSM; SluisKeyWriteAll<Role> (restore): the
//     same parameters, Encrypt, Decrypt and GenerateDataKey;
//   - SluisKeyMinter<Role>: Decrypt of the minter parameters a role without the
//     Cloudflare module reads;
//   - SluisKeyExternalReaders: Decrypt of `external/*` only, through SSM;
//   - SluisKeyAdminSeeds: Encrypt and GenerateDataKey through SSM (seed
//     parameters); SluisKeyAdminReads: Decrypt of the state secret and the
//     recovery password, which the stack reads back on every refresh;
//   - SluisKeyBreakglass: the function's operations through SSM;
//   - SluisKeySign: the roles that host oidc, directly (no service), Encrypt,
//     Decrypt and GenerateDataKey under exactly {instance, purpose: sign}
//     (decision D8), and three denies that hold the sign purpose to them:
//     SluisKeySignContextReserved (nobody else uses purpose=sign),
//     SluisKeySignDirectPurposeOnly and SluisKeySignDirectContextKeysOnly (their
//     direct calls carry that context and no other key). With DenyOtherActions,
//     SluisKeyRolesNothingElse too.
//
// Composition. The statements are about sluis's principals and the sign
// purpose; none is a catch-all (no Deny on a principal other than a sluis role,
// no NotPrincipal, no Deny without a sluis condition), so an estate's other
// usages of the same key (a seal, Pulumi secrets, sops, a state bucket) keep
// working. The estate concatenates its own statements with these and passes the
// lists to KeyPolicyDocument, which refuses a repeated Sid and a policy over
// KMS's size limit. The estate keeps what is its own: key administration, its
// other usages, and a "nothing else" deny over its own principals, which must
// name the sluis roles among the principals it lets through.
func SluisKeyPolicyStatements(a KeyPolicyArgs) ([]map[string]any, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	root := map[string]any{"AWS": arnPrefix + "iam::" + a.Account + ":root"}
	viaSSM := map[string]any{condViaService: "ssm." + a.Region + ".amazonaws.com"}
	arns := func(prefixes ...string) []string { return parameterArnsUnder(a.Region, a.Account, prefixes...) }
	internalAll, externalAll := InternalParameterPrefix(a.Modules.Instance), ExternalParameterPrefix(a.Modules.Instance)
	use := []string{kmsEncrypt, kmsDecrypt, kmsGenerateDK, kmsDescribe}

	allow := func(sid string, actions []string, cond map[string]any) statement {
		return statement{"Sid": sid, "Effect": "Allow", "Principal": root, "Action": actions, "Resource": "*", "Condition": cond}
	}
	deny := func(sid string, cond map[string]any) statement {
		return statement{"Sid": sid, "Effect": "Deny", "Principal": map[string]any{"AWS": "*"}, "Resource": "*", "Condition": cond}
	}

	var st []statement
	var signRoles []string
	for _, r := range sortedKeyRoles(a.Roles) {
		name := sidName(r.Role.Name)
		var own []string
		if a.Layout.has4() && r.Role.hosts(ModuleOIDC) {
			own = append(own, arns(internalAll, externalAll)...)
		}
		if a.Layout.has5() || !r.Role.hosts(ModuleOIDC) {
			for _, m := range sortedModules(r.Role.Hosts) {
				own = append(own, arns(a.Modules.InternalPrefix(m), a.Modules.ExternalPrefix(m))...)
			}
		}
		st = append(st, allow(sidKeyRolePrefix+name, use, map[string]any{
			condArnEquals:    map[string]any{condPrincipalArn: r.Arn},
			condStringEquals: viaSSM,
			condStringLike:   map[string]any{condParameterArn: dedupe(own)},
		}))
		for _, g := range CrossGrants {
			if g.Role != r.Role.Name || (g.Verb != CrossReadAll && g.Verb != CrossWriteAll) {
				continue
			}
			sid, actions := sidKeyReadPrefix+name, []string{kmsDecrypt, kmsDescribe}
			if g.Verb == CrossWriteAll {
				sid, actions = sidKeyWritePrefix+name, use
			}
			st = append(st, allow(sid, actions, map[string]any{
				condArnEquals:    map[string]any{condPrincipalArn: r.Arn},
				condStringEquals: viaSSM,
				condStringLike:   map[string]any{condParameterArn: arns(internalAll, externalAll)},
			}))
		}
		if len(r.MinterRefs) > 0 && !r.Role.hosts(ModuleCloudflare) {
			var refs []string
			for _, ref := range sortedStrings(r.MinterRefs) {
				refs = append(refs, arnPrefix+"ssm:"+a.Region+":"+a.Account+":parameter"+a.Modules.rootOf()+"/"+ref)
			}
			st = append(st, allow(sidKeyMinterPrefix+name, []string{kmsDecrypt, kmsDescribe}, map[string]any{
				condArnEquals:    map[string]any{condPrincipalArn: r.Arn},
				condStringEquals: viaSSM,
				condStringLike:   map[string]any{condParameterArn: refs},
			}))
		}
		if r.Role.hosts(ModuleOIDC) {
			signRoles = append(signRoles, r.Arn)
		}
	}

	if len(a.ExternalReaders) > 0 {
		st = append(st, allow(sidKeyExternalReaders, []string{kmsDecrypt}, map[string]any{
			condArnEquals:    map[string]any{condPrincipalArn: sortedStrings(a.ExternalReaders)},
			condStringEquals: viaSSM,
			condStringLike:   map[string]any{condParameterArn: arns(externalAll)},
		}))
	}
	if len(a.Admins) > 0 {
		admins := map[string]any{condPrincipalArn: sortedStrings(a.Admins)}
		own := []string{StateSecretParameterNameV5(a.Modules.Instance), RecoveryPasswordParameterNameV5(a.Modules.Instance)}
		if a.Layout.has4() {
			own = append(own, StateSecretParameterName(a.Modules.Instance), RecoveryPasswordParameterName(a.Modules.Instance))
		}
		var own2 []string
		for _, n := range own {
			own2 = append(own2, arnPrefix+"ssm:"+a.Region+":"+a.Account+":parameter"+n)
		}
		st = append(st,
			allow(sidKeyAdminSeeds, []string{kmsEncrypt, kmsGenerateDK, kmsDescribe}, map[string]any{
				condArnLike: admins, condStringEquals: viaSSM,
			}),
			allow(sidKeyAdminReads, []string{kmsDecrypt}, map[string]any{
				condArnLike: admins, condStringEquals: viaSSM,
				condStringLike: map[string]any{condParameterArn: own2},
			}),
		)
	}
	if len(a.Breakglass) > 0 {
		st = append(st, allow(sidKeyBreakglass, use, map[string]any{
			condArnLike:      map[string]any{condPrincipalArn: sortedStrings(a.Breakglass)},
			condStringEquals: viaSSM,
		}))
	}

	if len(signRoles) > 0 {
		signRoles = sortedStrings(signRoles)
		actions := []string{kmsEncrypt, kmsDecrypt, kmsGenerateDK}
		keys := []string{"instance", "purpose"}
		direct := map[string]any{condViaService: "true"}
		st = append(st,
			allow(sidKeySignAllow, actions, map[string]any{
				condArnEquals: map[string]any{condPrincipalArn: signRoles},
				condStringEquals: map[string]any{
					condContextInstance: a.Modules.Instance,
					condContextPurpose:  SignPurpose,
				},
				"ForAllValues:StringEquals": map[string]any{condContextKeys: keys},
			}),
			deny(sidKeySignReserved, map[string]any{
				condStringEquals: map[string]any{condContextPurpose: SignPurpose},
				"ArnNotEquals":   map[string]any{condPrincipalArn: signRoles},
			}),
			deny(sidKeySignPurposeOnly, map[string]any{
				condArnEquals:     map[string]any{condPrincipalArn: signRoles},
				"Null":            direct,
				"StringNotEquals": map[string]any{condContextPurpose: SignPurpose},
			}),
			deny(sidKeySignContextOnly, map[string]any{
				condArnEquals:                 map[string]any{condPrincipalArn: signRoles},
				"Null":                        direct,
				"ForAnyValue:StringNotEquals": map[string]any{condContextKeys: keys},
			}),
		)
		st[len(st)-3]["Action"] = []string{kmsDecrypt, kmsEncrypt, "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:CreateGrant"}
		st[len(st)-2]["Action"] = actions
		st[len(st)-1]["Action"] = actions
		if a.DenyOtherActions {
			d := deny(sidKeyRolesNothingElse, map[string]any{condArnEquals: map[string]any{condPrincipalArn: signRoles}})
			d["NotAction"] = append(append([]string(nil), actions...), kmsDescribe)
			st = append(st, d)
		}
	}
	if err := uniqueSids(st); err != nil {
		return nil, err
	}
	return st, nil
}

func (a KeyPolicyArgs) validate() error {
	if err := a.Modules.validate(); err != nil {
		return err
	}
	if !a.Layout.valid() {
		return fmt.Errorf("sluispulumi: KeyPolicyArgs.Layout %q is not v4, v5 or v4+v5", a.Layout)
	}
	if a.Region == "" || a.Account == "" {
		return errors.New("sluispulumi: KeyPolicyArgs.Region and Account are required")
	}
	if len(a.Roles) == 0 {
		return errors.New("sluispulumi: KeyPolicyArgs.Roles is empty: the key would admit no function")
	}
	seen := map[string]bool{}
	for _, r := range a.Roles {
		switch {
		case !roleNamePattern.MatchString(r.Role.Name):
			return fmt.Errorf("sluispulumi: KeyPolicyArgs role name %q is not lower-case letters, digits and '-'", r.Role.Name)
		case seen[r.Role.Name]:
			return fmt.Errorf("sluispulumi: KeyPolicyArgs names role %q twice", r.Role.Name)
		case len(r.Role.Hosts) == 0:
			return fmt.Errorf("sluispulumi: KeyPolicyArgs role %q hosts no module", r.Role.Name)
		case !strings.HasPrefix(r.Arn, "arn:") || strings.ContainsAny(r.Arn, "*?"):
			return fmt.Errorf("sluispulumi: KeyPolicyArgs role %q needs an exact role ARN, got %q", r.Role.Name, r.Arn)
		}
		seen[r.Role.Name] = true
		for _, m := range r.Role.Hosts {
			if !validModule(m) {
				return fmt.Errorf("sluispulumi: KeyPolicyArgs role %q hosts unknown module %q", r.Role.Name, m)
			}
		}
	}
	for _, list := range [][]string{a.ExternalReaders, a.Admins, a.Breakglass} {
		for _, v := range list {
			if !strings.HasPrefix(v, "arn:") {
				return fmt.Errorf("sluispulumi: KeyPolicyArgs names %q, which is not an ARN", v)
			}
		}
	}
	for _, v := range a.ExternalReaders {
		if strings.ContainsAny(v, "*?") {
			return fmt.Errorf("sluispulumi: KeyPolicyArgs.ExternalReaders needs exact role ARNs, got %q", v)
		}
	}
	return nil
}

var roleNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)

// sidName camel-cases a role name for a Sid (`my-role` is `MyRole`).
func sidName(name string) string {
	var b strings.Builder
	for _, p := range strings.Split(name, "-") {
		if p != "" {
			b.WriteString(strings.ToUpper(p[:1]) + p[1:])
		}
	}
	return b.String()
}

func sortedKeyRoles(in []KeyRole) []KeyRole {
	out := append([]KeyRole(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Role.Name < out[j].Role.Name })
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func uniqueSids(st []statement) error {
	seen := map[string]bool{}
	for _, s := range st {
		sid, _ := s["Sid"].(string)
		if seen[sid] {
			return fmt.Errorf("sluispulumi: the key policy repeats Sid %q", sid)
		}
		seen[sid] = true
	}
	return nil
}
