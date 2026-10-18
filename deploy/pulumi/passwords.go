package sluispulumi

// The recovery account and the conformance account sign in by SRP
// (docs/decisions/0076): the service holds a verifier for each and never the
// password, so the password parameters are the operator's alone. The issuer's
// role holds the oidc namespace they sit in, so its allows would reach them; an
// explicit deny, in the role's policy and in the parameter key's, keeps them
// out, as the restore identity is kept from the backup role.

const (
	sidDenyPasswords    = "SluisDenyPasswords"
	sidDenyPasswordsKey = "SluisDenyPasswordsKey"
	sidKeyDenyPasswords = "SluisKeyDenyPasswords"
)

// oidcPasswordArns are the ARNs of the passwords on layout v5.
func oidcPasswordArns(region, account string, m ModuleSet) []string {
	base := arnPrefix + "ssm:" + region + ":" + account + ":parameter" + m.InternalPrefix(ModuleOIDC) + "/"
	return []string{base + "recovery-password", base + "conformance-password"}
}

// oidcPasswordsDeny is what keeps the passwords from the role that hosts the
// oidc module. The parameters themselves are denied to it, and so is the
// decryption of their values, which closes GetParametersByPath (authorised on
// the path and not on the parameter).
func oidcPasswordsDeny(env ModuleEnv) []statement {
	arns := oidcPasswordArns(env.Region, env.Account, env.Modules)
	return []statement{
		{"Sid": sidDenyPasswords, "Effect": "Deny", "Action": "ssm:*", "Resource": arns},
		{
			"Sid": sidDenyPasswordsKey, "Effect": "Deny", "Action": []string{kmsDecrypt}, "Resource": "*",
			"Condition": map[string]any{"StringEquals": map[string]any{condParameterArn: arns}},
		},
	}
}
