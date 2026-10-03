// Command sluis-lambda is the Lambda extension that sends a
// function's OTLP data with the function role's identity. It is installed
// as /opt/extensions/access-roster-otlp; see docs/integrations/aws-lambda.md.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/truvity/observability/lambdaext"
)

func main() { os.Exit(run()) }

// getenv reads the extension's settings under their SLUIS_* names and falls
// back to the ACCESS_ROSTER_* names they had before the rename to sluis: both
// work, and SLUIS_* wins. Any other variable (AWS_LAMBDA_RUNTIME_API, OTEL_*)
// is read as it is.
func getenv(name string) string {
	if rest, ok := strings.CutPrefix(name, "ACCESS_ROSTER_"); ok {
		if value := os.Getenv("SLUIS_" + rest); value != "" {
			return value
		}
	}
	return os.Getenv(name)
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	err := lambdaext.Run(ctx, lambdaext.Options{
		Getenv: getenv,
		Logf:   log.Printf,
		// The platform matches the registered name to the file name.
		Name: filepath.Base(os.Args[0]),
	})
	if err != nil {
		log.Printf("access-roster-otlp: %v", err)
		return 1
	}
	return 0
}
