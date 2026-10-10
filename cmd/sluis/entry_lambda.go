//go:build lambda

package main

import (
	"github.com/truvity/sluis/internal/lambdaapp"
	_ "github.com/truvity/sluis/internal/lambdaapp/backupfn"
	_ "github.com/truvity/sluis/internal/lambdaapp/cloudflarefn"
	_ "github.com/truvity/sluis/internal/lambdaapp/issuerfn"
)

// platformEntry is the Lambda build's second entry (docs/decisions/0071): the
// runtime starts `bootstrap` with no arguments and AWS_LAMBDA_RUNTIME_API set,
// and the process then serves API Gateway events and scheduler ticks through
// lambdaapp, as cmd/sluis-lambda does. Anywhere else (a workstation, a test)
// the command line is the entry. The event dispatch is in this build only.
func platformEntry(args []string, getenv func(string) string) bool {
	if len(args) != 0 || getenv("AWS_LAMBDA_RUNTIME_API") == "" {
		return false
	}
	lambdaapp.Start()
	return true
}
