//nolint:lll // a schema is prose, and a description is one string
package schema

// signerSchema is the service document's `signer` section. Which of function
// and url is set is checked in internal/config.Signer.Validate.
func signerSchema() m {
	return obj("Where the signer module is, for a process that does not hold the signing key itself (docs/decisions/0071).", m{
		"remote": obj("Where the signer is when it is not this process: the issuer then signs and reads the key set through the signer module there (docs/decisions/0071), and this process opens no signing key. Set exactly one of `function` and `url`. Unset keeps the signer in this process.", m{
			"function":  str("The module's Lambda function, name or ARN. It is invoked through its `live` alias."),
			"url":       url("The module's Kubernetes Service. A call carries the pod's projected ServiceAccount token."),
			"audience":  str("The audience of that token. The module's name, `signer`, when unset."),
			"tokenFile": str("Where the projected token is mounted. `/var/run/secrets/sluis/signer/token` when unset."),
		}),
	})
}
