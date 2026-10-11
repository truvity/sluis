//nolint:lll // a schema is prose, and a description is one string
package schema

// callsSchema is the `calls` section of the sluis/v3 document and of the two
// controller documents: how this process reaches the modules that are not in
// it. What a pattern cannot say (an http(s) URL, a module named once) is
// internal/config.Calls.Validate.
func callsSchema() m {
	target := obj("How one module is reached. Set at most one of `function` and `url`; neither keeps the module in this process.", m{
		"function":  str("The module's Lambda function, name or ARN. It is invoked through the `live-<class>` alias of this process's caller class."),
		"url":       url("The module's Kubernetes Service. A call carries the pod's projected ServiceAccount token."),
		"audience":  str("The audience of that token. The module's name when unset."),
		"tokenFile": str("Where the projected token is mounted. `/var/run/secrets/sluis/<module>/token` when unset."),
	})
	target["not"] = m{"required": []string{"function", "url"}}
	modules := m{
		"type": "object", "propertyNames": m{"pattern": cloudflareName},
		"description":          "The modules this process calls, by module name (`github`, `slack`, `google`, `cloudflare`, `backup`).",
		"additionalProperties": target,
	}
	return obj("How this process reaches the modules that are not in it (docs/decisions/0071). A module with no entry is called in this process. It replaces `cloudflare.remote`, which is deprecated and still read.", m{
		"modules": modules,
	})
}
