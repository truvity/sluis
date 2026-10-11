// Package iameval is a small evaluator of AWS IAM policy documents, for tests.
//
// It answers one question: may this principal do this action on this resource,
// given an identity policy and, for a KMS key, the key policy beside it. It
// exists so that the deny matrix of the Pulumi library (deploy/pulumi) can assert
// what a role is refused, not only what its statements contain.
//
// What it evaluates:
//
//   - Allow and Deny, Action and NotAction, Resource and NotResource, with the
//     `*` and `?` globs (an action compares without case, a resource with it);
//   - Principal in a key policy: `*`, an exact ARN, and an account root, which
//     delegates to the identity policy;
//   - the condition operators StringEquals, StringNotEquals, StringLike,
//     StringNotLike, ArnEquals, ArnLike, ArnNotEquals, ArnNotLike, Null and
//     Bool, each with the ForAnyValue: and ForAllValues: set prefixes and the
//     IfExists suffix. The ARN operators compare the six colon-separated
//     sections one by one, so a `*` in one section never reaches the next.
//
// What it refuses, with an error, rather than guess: any other operator,
// NotPrincipal, a policy variable (`${...}`), a Principal in an identity
// policy, and a multi-valued context key under an operator without a set prefix.
// A document the evaluator cannot read must fail a test, not pass it.
//
// What it does not model: permissions boundaries, SCPs, session policies, resource
// policies other than a key policy, and KMS grants. The decision rule is the
// same-account one: an explicit Deny anywhere wins; otherwise a key policy
// statement that names the principal (or `*`) allows by itself, and one that
// names the account root allows only together with an identity-policy Allow.
package iameval
