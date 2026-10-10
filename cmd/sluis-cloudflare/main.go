// Command sluis-cloudflare is the Cloudflare module as an AWS Lambda function:
// the STS minter and the credential rotation, answering other modules' calls.
// One arm64 `bootstrap` binary; the issuer is not in it.
//
// It is built with `-tags lambda,lambda.norpc` and, in a release, with
// `-ldflags -X github.com/truvity/sluis/internal/version.Module=cloudflare`.
package main

import (
	"github.com/truvity/sluis/internal/lambdaapp"
	_ "github.com/truvity/sluis/internal/lambdaapp/cloudflarefn"
)

func main() { lambdaapp.StartModule(lambdaapp.ModuleCloudflare) }
