// Package audit is sluis's thin wrapper over audit's emitter (github.com/truvity/audit/emitter): the library inside each
// emitting process that builds one OCSF 1.9.0 record as RFC 8785 canonical JSON from one call and hands it to atoom's
// transport port on the emit socket. The canonical form, the record, the schemas and their policy, the ids, the count of
// lost facts and the transport are audit's, and so are their rules (see the emitters page of truvity/audit).
//
// sluis keeps three things here: its action table and the schemas generated from it (zz_table.go, made by tools/gen/audit
// from docs/architecture/audit-actions.yaml), its configuration in its own words (Config: an actor of a boundary, which New
// maps to audit's component and scope, with the product sluis), and the Mac of key@ that makes pseudonyms under the label
// sluis-pseudonym-v1.
//
// The Rust crate sluis-audit is the same wrapper over audit's crate audit-emitter, and both produce the same bytes from the
// same input (kits/conformance/audit).
package audit
