// Command sluis-lambda is sluis as ONE AWS Lambda function: one arm64
// `bootstrap` binary in one zip, deployed as one function that serves API
// Gateway events and runs the controllers' passes. See
// docs/reference/sluis/lambda.md.
//
// It is built with `-tags lambda,lambda.norpc`: the first leaves out the
// Kubernetes, NATS and Valkey storage the other binary carries, and
// imports_test.go holds it to that; the second is the Lambda library's own, for
// a runtime that is not Go's RPC one.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/truvity/sluis/internal/lambdaapp"
)

func main() {
	fn, err := lambdaapp.Open(context.Background(), os.Getenv)
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
