// Command sluis-issuer is the issuer module as an AWS Lambda function: the
// issuer, the signer and the console in one process, in one arm64 `bootstrap`
// binary (docs/decisions/0072). The providers still run in this process until
// each is released as a zip of its own. There is no signer binary: the signer
// is internal/signer, in this process.
//
// It is built with `-tags lambda,lambda.norpc` and, in a release, with
// `-ldflags -X github.com/truvity/sluis/internal/version.Module=issuer`, so the
// zip refuses to run as another module.
package main

import (
	"github.com/truvity/sluis/internal/lambdaapp"
	_ "github.com/truvity/sluis/internal/lambdaapp/issuerfn"
)

func main() { lambdaapp.StartModule(lambdaapp.ModuleIssuer) }
