// Package secretstore is sluis's view of its secrets on a [state.Store] under
// layout v5 (docs/decisions/0072-storage-layout-v5-module-first.md, which
// refines docs/decisions/0041-the-secret-contract.md).
//
// An installation's secrets are in two namespaces of one store, each module
// first:
//
//   - internal/<module>/<name> is read and written by sluis only and is never
//     granted to anyone; the addresses below it are sluis's own business and
//     not a contract;
//   - external/<module>/<name> is the public contract: one typed, versioned
//     JSON document per address (see [OIDCv1], [GitHubv1] and [Slackv1],
//     pinned by the JSON Schemas under schemas/external/ and by golden tests).
//
// A value needed by something outside sluis is external, whoever wrote it; a
// value only sluis uses is internal. Each is stored once. Every domain method
// returns a typed [state.Value]: Get, Put and Rotating over the backend's own
// versions.
//
// [OpenV5] builds the stores ([StoresV5]) from a serve document's `secrets`
// section, through one view type per module. Layout v4 (internal/config,
// internal/credentials/<kind>/<id>/<ref>, external/<kind>/<id>) was removed in
// v1.77.1 ([CheckLayout]).
package secretstore
