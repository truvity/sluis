// Command sluis-lambda is the deprecated all-in-one Lambda function: one arm64
// `bootstrap` binary in one zip that carries the issuer, the Cloudflare module
// and the backup module, and runs the one the function's document is for. It is
// published for one more release as `sluis-lambda_<version>_linux_arm64.zip`
// so that a deployment that names it keeps working; the per-module zips
// (`sluis-issuer_*`, `sluis-cloudflare_*`, `sluis-backup_*`) replace it. See
// docs/reference/sluis/lambda.md.
//
// It is built with `-tags lambda,lambda.norpc`: the first leaves out the
// Kubernetes, NATS and Valkey storage the other binary carries, and
// imports_test.go holds it to that; the second is the Lambda library's own, for
// a runtime that is not Go's RPC one.
package main

import (
	"github.com/truvity/sluis/internal/lambdaapp"
	_ "github.com/truvity/sluis/internal/lambdaapp/backupfn"
	_ "github.com/truvity/sluis/internal/lambdaapp/cloudflarefn"
	_ "github.com/truvity/sluis/internal/lambdaapp/issuerfn"
)

// The dispatch is lambdaapp.Start, shared with the lambda build of cmd/sluis.
func main() { lambdaapp.Start() }
