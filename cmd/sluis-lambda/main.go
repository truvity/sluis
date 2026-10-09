// Command sluis-lambda is sluis as ONE AWS Lambda function: one arm64
// `bootstrap` binary in one zip, deployed as one function that serves API
// Gateway events and runs the controllers' passes. See
// docs/reference/sluis/lambda.md.
//
// It is built with `-tags lambda,lambda.norpc`: the first leaves out the
// Kubernetes, NATS and Valkey storage the other binary carries, and
// imports_test.go holds it to that; the second is the Lambda library's own, for
// a runtime that is not Go's RPC one.
package main

import "github.com/truvity/sluis/internal/lambdaapp"

// The dispatch is lambdaapp.Start, shared with the lambda build of cmd/sluis.
func main() { lambdaapp.Start() }
