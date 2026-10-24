// Command sluis-outline is the Outline module as an AWS Lambda function: the
// workspaces' passes, on a schedule or when asked, and the console's Outline
// calls (docs/decisions/0077). One arm64 `bootstrap` binary; the issuer and the
// other providers are not in it.
//
// It is built with `-tags lambda,lambda.norpc` and, in a release, with
// `-ldflags -X github.com/truvity/sluis/internal/version.Module=sluis-module=outline`.
package main

import (
	"github.com/truvity/sluis/internal/lambdaapp"
	_ "github.com/truvity/sluis/internal/lambdaapp/outlinefn"
)

func main() { lambdaapp.StartModule(lambdaapp.ModuleOutline) }
