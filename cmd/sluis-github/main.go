// Command sluis-github is the GitHub module's controller as an AWS Lambda
// function: the organisations' team passes and the links pass, on a schedule or
// when the console asks. One arm64 `bootstrap` binary; the issuer, the mint and
// the other providers are not in it.
//
// It is built with `-tags lambda,lambda.norpc` and, in a release, with
// `-ldflags -X github.com/truvity/sluis/internal/version.Module=sluis-module=github`.
package main

import (
	"github.com/truvity/sluis/internal/lambdaapp"
	_ "github.com/truvity/sluis/internal/lambdaapp/githubfn"
)

func main() { lambdaapp.StartModule(lambdaapp.ModuleGitHub) }
