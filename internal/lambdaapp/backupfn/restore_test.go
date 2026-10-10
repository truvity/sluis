package backupfn

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/aws/aws-lambda-go/lambdacontext"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/truvity/sluis/internal/backup/restore"
	"github.com/truvity/sluis/internal/backup/restorejob"
	"github.com/truvity/sluis/internal/lambdaapp"
)

type fakeRestorer struct {
	res restorejob.Result
	err error
	got []restorejob.Request
	pre []string
}

func (f *fakeRestorer) Restore(_ context.Context, r restorejob.Request) (restorejob.Result, error) {
	f.got = append(f.got, r)
	return f.res, f.err
}

func (f *fakeRestorer) PreviewRestore(_ context.Context, id string) (*restore.Report, error) {
	f.pre = append(f.pre, id)
	if id == "nope" {
		return nil, errors.New("no such backup")
	}
	return &restore.Report{ID: id}, nil
}

type fakeLambda struct {
	in  []*awslambda.InvokeInput
	err error
}

func (f *fakeLambda) Invoke(_ context.Context, in *awslambda.InvokeInput, _ ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error) {
	f.in = append(f.in, in)
	return &awslambda.InvokeOutput{}, f.err
}

const arn = "arn:aws:lambda:eu-west-1:111122223333:function:sluis-restore:live"

func lambdaCtx() context.Context {
	return lambdacontext.NewContext(context.Background(), &lambdacontext.LambdaContext{InvokedFunctionArn: arn})
}

func paused() restorejob.Result {
	return restorejob.Result{Outcome: restorejob.OutcomePaused, ID: "r1", Run: &restorejob.Run{ID: "r1", BackupID: "b1",
		State: restorejob.StatePaused, Maintenance: restorejob.MaintenanceOn, Reason: "time"}}
}

func TestAPausedRestoreStartsItsOwnNextInvocationThroughTheSameAlias(t *testing.T) {
	f, api := &fakeRestorer{res: paused()}, &fakeLambda{}
	out, err := restoreEvent(lambdaCtx(), f, api, slog.Default(), lambdaapp.RestoreEvent{Backup: "b1", Overwrite: true, By: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Continued || out.Outcome != "paused" || out.Maintenance != "on" || out.ID != "r1" {
		t.Errorf("%+v", out)
	}
	if len(api.in) != 1 || *api.in[0].FunctionName != arn || api.in[0].InvocationType != types.InvocationTypeEvent ||
		string(api.in[0].Payload) != `{"kind":"restore","resume":true}` {
		t.Errorf("invoked %+v", api.in)
	}
	if len(f.got) != 1 || f.got[0].BackupID != "b1" || !f.got[0].Overwrite || f.got[0].By != "ada" {
		t.Errorf("request %+v", f.got)
	}
}

func TestOnlyAPausedRestoreContinues(t *testing.T) {
	for _, res := range []restorejob.Result{
		{Outcome: restorejob.OutcomeCompleted, ID: "r1", Run: &restorejob.Run{ID: "r1"}},
		{Outcome: restorejob.OutcomeFailed, ID: "r1", Run: &restorejob.Run{ID: "r1", Reason: "verify", Error: "differs"}},
		{Outcome: restorejob.OutcomeContended}, {Outcome: restorejob.OutcomeIdle},
		{Outcome: restorejob.OutcomeRefused, Reason: "backup", Error: "no such backup"},
	} {
		api := &fakeLambda{}
		out, err := restoreEvent(lambdaCtx(), &fakeRestorer{res: res}, api, slog.Default(), lambdaapp.RestoreEvent{Backup: "b1"})
		if err != nil || len(api.in) != 0 || out.Continued || out.Outcome != res.Outcome {
			t.Errorf("%s: %+v, %v, invoked %d", res.Outcome, out, err, len(api.in))
		}
	}
}

func TestAFailedContinuationIsSaidAndNotAnError(t *testing.T) {
	out, err := restoreEvent(lambdaCtx(), &fakeRestorer{res: paused()}, &fakeLambda{err: errors.New("access denied")}, slog.Default(), lambdaapp.RestoreEvent{Resume: true})
	if err != nil || out.Continued || out.Error == "" || out.Outcome != "paused" {
		t.Errorf("%+v, %v", out, err)
	}
}

func TestAPreviewWritesNothingAndStartsNothing(t *testing.T) {
	f, api := &fakeRestorer{}, &fakeLambda{}
	out, err := restoreEvent(lambdaCtx(), f, api, slog.Default(), lambdaapp.RestoreEvent{Backup: "b1", Preview: true})
	if err != nil || out.Outcome != "preview" || out.Report == nil || len(f.got) != 0 || len(api.in) != 0 {
		t.Errorf("%+v, %v", out, err)
	}
	if out, err = restoreEvent(lambdaCtx(), f, api, slog.Default(), lambdaapp.RestoreEvent{Backup: "nope", Preview: true}); err != nil || out.Outcome != "refused" {
		t.Errorf("%+v, %v", out, err)
	}
}

func TestTheRestoreEventIsCheckedAndOnlyTheRestoreFunctionTakesIt(t *testing.T) {
	h := lambdaapp.NewHTTP(nil, nil, nil)
	if _, err := h.Handle(context.Background(), json.RawMessage(`{"kind":"restore","backup":"b1"}`)); err == nil {
		t.Fatal("a function with no restore answered the event")
	}
	f := &fakeRestorer{res: restorejob.Result{Outcome: restorejob.OutcomeCompleted, ID: "r1", Run: &restorejob.Run{ID: "r1", BackupID: "b1", State: "completed"}}}
	h.WithRestore(func(ctx context.Context, ev lambdaapp.RestoreEvent) (lambdaapp.RestoreResult, error) {
		return restoreEvent(ctx, f, nil, slog.Default(), ev)
	})
	for _, bad := range []string{`{"kind":"restore"}`, `{"kind":"restore","preview":true}`, `{"kind":"restore","backup":"b1","resume":true}`,
		`{"kind":"restore","resume":true,"preview":true}`} {
		out, err := h.Handle(context.Background(), json.RawMessage(bad))
		if res, ok := out.(lambdaapp.RestoreResult); err != nil || !ok || res.Outcome != "refused" || res.Kind != lambdaapp.KindRestore {
			t.Errorf("%s: %+v, %v", bad, out, err)
		}
	}
	if len(f.got) != 0 {
		t.Errorf("a malformed event reached the restore: %+v", f.got)
	}
	out, err := h.Handle(context.Background(), json.RawMessage(`{"kind":"restore","backup":"b1"}`))
	if res, ok := out.(lambdaapp.RestoreResult); err != nil || !ok || res.Outcome != "completed" || res.Maintenance != "" || res.Kind != lambdaapp.KindRestore {
		t.Errorf("%+v, %v", out, err)
	}
	// A backup event is not the restore function's.
	if _, err := h.Handle(context.Background(), json.RawMessage(`{"kind":"backup"}`)); err == nil {
		t.Error("the restore function ran a backup")
	}
}
