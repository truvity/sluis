//nolint:lll // a schema is prose, and a description is one string
package schema

import "fmt"

// remoteSchema is the `github`, `slack` or `google` section of the sluis/v3
// document and of the controller documents: where that module is when it is not
// this process, shaped as `cloudflare.remote`. What a pattern cannot say
// (exactly one of function and url, an http(s) URL) is
// internal/config.ModuleRoute.Validate.
func remoteSchema(module, title string) m {
	section := obj(fmt.Sprintf("The %s module's routing (docs/decisions/0071). Unset keeps the module in this process.", title), m{
		"remote": obj(fmt.Sprintf("Where the %s module is when it is not this process: the calls to it go there. Set exactly one of `function` and `url`. Unset keeps the module in this process.", title), m{
			"function":  str("The module's Lambda function, name or ARN. It is invoked through the `live-<class>` alias of this process's caller class."),
			"url":       url("The module's Kubernetes Service. A call carries the pod's projected ServiceAccount token."),
			"audience":  str(fmt.Sprintf("The audience of that token. The module's name, `%s`, when unset.", module)),
			"tokenFile": str(fmt.Sprintf("Where the projected token is mounted. `/var/run/secrets/sluis/%s/token` when unset.", module)),
		}),
	})
	if module == "github" || module == "slack" {
		props, _ := section["properties"].(m)
		props["serve"] = obj(fmt.Sprintf("Makes the %s module answer the issuer's calls on Kubernetes (docs/decisions/0071). Only the module's own document sets it. The module listens on `address` and accepts a call whose projected ServiceAccount token was minted for `audience`, checked with a TokenReview, and whose subject is one of `issuers`; any other call is a 401. On Lambda the function answers `rpc` events and IAM decides who may invoke it. Not with `remote`. Unset serves nothing.", title), m{
			"address":  str("The listener, `host:port`; it also answers `GET /healthz`. `:8081` when unset."),
			"audience": str(fmt.Sprintf("The audience a caller's token must be minted for. The module's name, `%s`, when unset.", module)),
			"issuers":  m{"type": "array", "minItems": 1, "uniqueItems": true, "items": m{"type": "string", "pattern": `^system:serviceaccount:[^:]+:[^:]+$`}, "description": "The issuer's ServiceAccount, as `system:serviceaccount:<namespace>:<name>`. A call from it is both the issuer class (`mint_installation`) and the console class (the console methods); a token of any other subject is a 401."},
		}, "issuers")
	}
	return section
}

// addRemotes adds the three module sections to a document's properties.
func addRemotes(props m) {
	props["github"] = remoteSchema("github", "GitHub")
	props["slack"] = remoteSchema("slack", "Slack")
	props["google"] = remoteSchema("google", "Google")
}
