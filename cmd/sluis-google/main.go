// Command sluis-google is the Google module as an AWS Lambda function: the
// directory refresh of the connected workspaces, on a schedule or for one
// workspace. One arm64 `bootstrap` binary; the issuer and the other providers
// are not in it.
//
// It is built with `-tags lambda,lambda.norpc` and, in a release, with
// `-ldflags -X github.com/truvity/sluis/internal/version.Module=sluis-module=google`.
package main

import (
	"github.com/truvity/sluis/internal/lambdaapp"
	_ "github.com/truvity/sluis/internal/lambdaapp/googlefn"
)

func main() { lambdaapp.StartModule(lambdaapp.ModuleGoogle) }
