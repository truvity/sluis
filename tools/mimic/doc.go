// Package mimic is a test identity provider for development (ADR 0054). It stands in for a company's Google
// Workspace or Microsoft Entra ID tenant: an OpenID Connect provider with one issuer per profile and tenant, each
// minting fixed identities that its config declares, on a made-up test domain.
//
// mimic is a relying party of the stable sluis, never of a sluis instance under test. A person signs in there, and
// a CI job hands over a sluis token it got by workload identity; either way mimic keeps a session cookie of its own.
// The session's sluis `groups` say which tenants' identities the subject may use, and every token mimic mints names
// the real subject in `act` (RFC 8693, section 4.1).
//
// It runs on development only: New refuses a config whose environment is not "devel". It is a test tool, never part
// of a product executable. The executable is cmd/sluis-mimic, and mimictest starts one in a test.
package mimic
