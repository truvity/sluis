// Package secretstore is sluis's view of its secrets on a [state.Store] under
// layout v4 (docs/decisions/0041-the-secret-contract.md).
//
// An installation's secrets are in two namespaces of one store:
//
//   - [Internal], rooted at <root>/internal, is read and written by sluis
//     only and is never granted to anyone; the addresses below it are sluis's
//     own business and not a contract;
//   - [External], rooted at <root>/external, is the public contract: one typed,
//     versioned JSON document per address, <kind>/<id> (see [OIDCv1],
//     [GitHubv1] and [Slackv1], pinned by the JSON Schemas under
//     schemas/external/ and by golden tests).
//
// A value needed by something outside sluis is external, whoever wrote it; a
// value only sluis uses is internal. Each is stored once. Every domain method
// returns a typed [state.Value]: Get, Put and Rotating over the backend's own
// versions.
//
// [Open] builds the two namespaces from a serve document's `secrets` section.
// [Open] builds layout v4 only ([CheckLayout]). Layout v5 ([StoresV5],
// [LayoutV5], opened by [OpenV5]) names the module first, internal/<module>/<name>
// and external/<module>/<name>, through one view type per module; the two do not
// mix.
package secretstore
