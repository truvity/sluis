package lambdaapp

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
)

// Start is the Lambda entry of the service: it opens the function from the
// environment and serves API Gateway events and scheduler ticks until the
// runtime stops it. Both mains that ship as `bootstrap` call it
// (cmd/sluis-lambda and the lambda build of cmd/sluis), so there is one
// dispatch.
func Start() {
	fn, err := Open(context.Background(), os.Getenv)
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
