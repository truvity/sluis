// Package clientcreds keeps the secrets of confidential OIDC clients the
// issuer generates itself (a policy client written `secret: {generate: true}`).
//
// A generated secret is a credential, so it lives in the Secrets port under
// `credentials/oidc-client/<id>/secret` as one JSON [Record]. It is made
// once, with a create-only write ([port.Secrets.PutIfVersion] with an empty
// version), so that however many replicas or invocations start together
// exactly one value exists and nobody ever overwrites it.
//
// [Reconcile] makes sure every generated client has a record. [Resolver] is
// what the token endpoint asks for a client's secret: the record when there is
// one, otherwise the input `clients/<id>/secret` the installation delivers,
// for every client, so that a first deploy never refuses a valid client
// before the first reconcile ran.
//
// No value is ever logged, put in an error or used as a metric attribute.
package clientcreds
