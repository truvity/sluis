package backupfn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/aws/aws-lambda-go/lambdacontext"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/truvity/sluis/internal/audit"
	backupapp "github.com/truvity/sluis/internal/backup/app"
	"github.com/truvity/sluis/internal/backup/restore"
	"github.com/truvity/sluis/internal/backup/restorejob"
	"github.com/truvity/sluis/internal/lambdaapp"
)

// lambdaAPI is the part of the Lambda client the function uses to start its own
// next invocation.
type lambdaAPI interface {
	Invoke(ctx context.Context, in *awslambda.InvokeInput, opts ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error)
}

// restorer is the restore role of the backup module; *backupapp.App is one.
type restorer interface {
	Restore(ctx context.Context, req restorejob.Request) (restorejob.Result, error)
	PreviewRestore(ctx context.Context, backupID string) (*restore.Report, error)
}

var _ restorer = (*backupapp.App)(nil)

// continueEvent is what a paused restore sends itself.
var continueEvent = json.RawMessage(`{"kind":"restore","resume":true}`)

// reinvoke starts the next slice: an asynchronous invocation of the very ARN
// (alias included) this one was invoked through, so the same policy applies.
func reinvoke(ctx context.Context, api lambdaAPI) error {
	lc, ok := lambdacontext.FromContext(ctx)
	if !ok || lc.InvokedFunctionArn == "" {
		return errors.New("the invocation does not say which function it is")
	}
	arn := lc.InvokedFunctionArn
	_, err := api.Invoke(ctx, &awslambda.InvokeInput{FunctionName: &arn, InvocationType: types.InvocationTypeEvent, Payload: continueEvent})
	return err
}

// restoreEvent runs one {"kind":"restore"} event on a. api starts the next
// slice when this one paused; nil only reports it.
func restoreEvent(ctx context.Context, a restorer, api lambdaAPI, log *slog.Logger, ev lambdaapp.RestoreEvent) (lambdaapp.RestoreResult, error) {
	if ev.Preview {
		rep, err := a.PreviewRestore(ctx, ev.Backup)
		if err != nil {
			return lambdaapp.RestoreResult{Outcome: restorejob.OutcomeRefused, Backup: ev.Backup, Error: err.Error()}, nil
		}
		return lambdaapp.RestoreResult{Outcome: "preview", Backup: ev.Backup, Report: rep}, nil
	}
	res, err := a.Restore(ctx, restorejob.Request{BackupID: ev.Backup, Overwrite: ev.Overwrite, Actor: audit.Workload("lambda:restore"),
		By: ev.By, Note: ev.Note})
	if err != nil {
		return lambdaapp.RestoreResult{}, err
	}
	out := lambdaapp.RestoreResult{Outcome: res.Outcome, ID: res.ID, Reason: res.Reason, Error: res.Error}
	if r := res.Run; r != nil {
		out.Backup, out.State, out.Maintenance = r.BackupID, r.State, r.Maintenance
		out.Reason, out.Error = r.Reason, r.Error
	}
	if res.Outcome == restorejob.OutcomePaused && api != nil {
		if err := reinvoke(ctx, api); err != nil {
			// The record says paused and maintenance stays set: an administrator's
			// {"kind":"restore","resume":true} carries on.
			log.ErrorContext(ctx, "the restore paused and could not start its next invocation; send {\"kind\":\"restore\",\"resume\":true}",
				slog.String("error", err.Error()))
			out.Error = fmt.Sprintf("paused; the next invocation was not started: %v", err)
		} else {
			out.Continued = true
		}
	}
	return out, nil
}

// openLambdaAPI connects with the function's credentials.
func openLambdaAPI(ctx context.Context) (lambdaAPI, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("lambda client: %w", err)
	}
	return awslambda.NewFromConfig(cfg), nil
}
