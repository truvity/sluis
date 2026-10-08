// Package state is a small versioned key-value store for the secrets and
// small documents a service keeps between runs: signing keys, issued
// credentials, rotation state.
//
// # Store
//
// [Store] is the one interface a backend implements. A key is a relative
// path such as "signing/current"; a Store has a prefix, [Store.Child]
// returns a view of a sub-prefix, and [Store.List] names the keys directly
// under the prefix (one level, not a recursive walk). Every write is
// versioned: [Store.Put] returns a [Rev], [Store.Get] returns the latest
// [Item] with its Rev, and [Store.GetRev] reads an older version. A write is
// conditional on the Rev the caller last saw (ifRev); the empty Rev means
// "the key must not exist". A lost race is [ErrConflict], never a silent
// overwrite. A missing key or version is [ErrNotFound].
//
// Every stored value is a JSON object. That keeps the stored form
// self-describing and lets a field be added to it without a migration; a
// backend refuses anything else with [ErrNotObject].
//
// Backends: [github.com/truvity/sluis/storage/state/memory] (tests and
// single-process use). The conformance suite in
// [github.com/truvity/sluis/storage/state/conformance] is what every backend
// must pass.
//
// # Value
//
// [Value] is a typed handle on one key: a Store, a key and a [Codec]
// that turns T into the JSON object and back. [JSON] encodes T as itself
// (T must marshal to a JSON object, so a struct or a map); [Raw] keeps
// opaque bytes as {"value": "<base64>"}. [Value.Rotating] reads the current
// value and, for a grace period after a rotation, the one it replaced, so
// a verifier that still holds the old key keeps working while the new one
// spreads.
//
// # Products on top
//
// This package knows nothing about what is stored. A product puts its own
// typed layer on top: an Internal view for the values only the service
// reads and writes, and an External view for the values it publishes or
// receives, each a few Value[T] over a Child store with its own types. The
// split lives in the product, so that changing a type there never changes a
// backend.
package state
