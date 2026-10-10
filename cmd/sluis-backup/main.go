// Command sluis-backup is the backup module as an AWS Lambda function. The
// same zip is deployed twice: as the backup function and as the restore
// function (`backup.role: restore` in the function's document). The issuer is
// not in it.
//
// It is built with `-tags lambda,lambda.norpc` and, in a release, with
// `-ldflags -X github.com/truvity/sluis/internal/version.Module=backup`.
package main

import (
	"github.com/truvity/sluis/internal/lambdaapp"
	_ "github.com/truvity/sluis/internal/lambdaapp/backupfn"
)

func main() { lambdaapp.StartModule(lambdaapp.ModuleBackup) }
