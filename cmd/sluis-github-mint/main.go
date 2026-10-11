// Command sluis-github-mint is the GitHub module's installation-token mint as
// an AWS Lambda function: it answers the issuer's `mint_installation` call and
// runs no controller pass. One arm64 `bootstrap` binary; the controller, the
// issuer and the other providers are not in it. It reads the GitHub module's
// document, the same one cmd/sluis-github reads.
//
// It is built with `-tags lambda,lambda.norpc` and, in a release, with
// `-ldflags -X github.com/truvity/sluis/internal/version.Module=sluis-module=github-mint`.
package main

import (
	"github.com/truvity/sluis/internal/lambdaapp"
	_ "github.com/truvity/sluis/internal/lambdaapp/githubmintfn"
)

func main() { lambdaapp.StartModule(lambdaapp.ModuleGitHubMint) }
