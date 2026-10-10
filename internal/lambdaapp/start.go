package lambdaapp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/truvity/sluis/internal/version"
)

// Start is the Lambda entry of the lambda build of cmd/sluis: it opens the function from the
// environment, whichever linked module the document is for, and serves API Gateway
// events and scheduler ticks until the runtime stops it.
func Start() { serve(Open) }

// StartModule is the entry of a zip built for one module (cmd/sluis-<module>).
// It refuses to run when the build pins another module (the release builds each
// zip with `-ldflags -X .../internal/version.Module=sluis-module=<module>`), and when the
// document is another module's.
func StartModule(module string) {
	if err := CheckPin(version.Pinned(), module); err != nil {
		fmt.Fprintln(os.Stderr, "sluis-"+module+":", err)
		slog.New(slog.NewJSONHandler(os.Stdout, nil)).ErrorContext(context.Background(), "sluis could not start", slog.Any("error", err))
		os.Exit(1)
	}
	serve(func(ctx context.Context, getenv func(string) string) (*Function, error) {
		return OpenModule(ctx, getenv, module)
	})
}

// CheckPin refuses a binary whose pinned module (the build's, empty for an
// unpinned development build) is not the module its main is for.
func CheckPin(pinned, module string) error {
	if pinned != "" && pinned != module {
		return fmt.Errorf("this build is pinned to the %q module and cannot run as %q", pinned, module)
	}
	return nil
}

func serve(open func(context.Context, func(string) string) (*Function, error)) {
	fn, err := open(context.Background(), os.Getenv)
	if err != nil {
		// A failure at cold start is the platform's "Init error": the function
		// does not take an invocation, and the message is in the log.
		slog.New(slog.NewJSONHandler(os.Stdout, nil)).ErrorContext(context.Background(), "sluis could not start", slog.Any("error", err))
		os.Exit(1)
	}
	defer fn.Close()
	lambda.Start(func(ctx context.Context, payload json.RawMessage) (any, error) {
		defer fn.Flush(ctx)
		return fn.Handler.Handle(ctx, payload)
	})
}
