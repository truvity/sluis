// Command sluis-slack is the Slack module's controller as an AWS Lambda
// function: the workspaces' user-group passes, on a schedule or when the
// console asks. One arm64 `bootstrap` binary; the issuer and the other
// providers are not in it.
//
// It is built with `-tags lambda,lambda.norpc` and, in a release, with
// `-ldflags -X github.com/truvity/sluis/internal/version.Module=sluis-module=slack`.
package main

import (
	"github.com/truvity/sluis/internal/lambdaapp"
	_ "github.com/truvity/sluis/internal/lambdaapp/slackfn"
)

func main() { lambdaapp.StartModule(lambdaapp.ModuleSlack) }
