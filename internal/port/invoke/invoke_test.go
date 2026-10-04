package invoke_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/invoke"
)

type fake struct {
	calls  []*awslambda.InvokeInput
	status int32
	err    error
}

func (f *fake) Invoke(_ context.Context, in *awslambda.InvokeInput, _ ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error) {
	f.calls = append(f.calls, in)
	if f.err != nil {
		return nil, f.err
	}
	return &awslambda.InvokeOutput{StatusCode: f.status}, nil
}

func TestNotifyInvokesTheFunctionAsynchronouslyWithARunEvent(t *testing.T) {
	api := &fake{status: 202}
	trigger, err := invoke.New(api, invoke.Config{GitHub: "sluis-github", Slack: "sluis-slack"})
	if err != nil {
		t.Fatal(err)
	}
	trigger.SetKind(func(string) string { return invoke.KindGitHub })
	if err = trigger.Notify(context.Background(), "acme"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(api.calls) != 1 {
		t.Fatalf("%d invocations, want 1", len(api.calls))
	}
	call := api.calls[0]
	if aws.ToString(call.FunctionName) != "sluis-github" {
		t.Errorf("function %q", aws.ToString(call.FunctionName))
	}
	if call.InvocationType != types.InvocationTypeEvent {
		t.Errorf("invocation type %q: a synchronous invoke would hold the console's request until the pass ends", call.InvocationType)
	}
	var event invoke.Event
	if err = json.Unmarshal(call.Payload, &event); err != nil {
		t.Fatal(err)
	}
	if event != (invoke.Event{Kind: "run", Target: "acme"}) {
		t.Errorf("payload %s", call.Payload)
	}
}

func TestNotifyReportsAFailureAsUnavailable(t *testing.T) {
	for name, api := range map[string]*fake{
		"the call fails":          {err: errors.New("AccessDeniedException")},
		"the event is not queued": {status: 500},
	} {
		trigger, err := invoke.New(api, invoke.Config{GitHub: "f"})
		if err != nil {
			t.Fatal(err)
		}
		if err = trigger.Notify(context.Background(), "acme"); !errors.Is(err, port.ErrUnavailable) {
			t.Errorf("%s: %v, want port.ErrUnavailable", name, err)
		}
	}
}

func TestSubscribeDeliversNothing(t *testing.T) {
	trigger, _ := invoke.New(&fake{status: 202}, invoke.Config{GitHub: "f"})
	stop := trigger.Subscribe(func(string) { t.Error("a Lambda process is never notified") })
	stop()
}

func TestNewRefusesNoFunction(t *testing.T) {
	if _, err := invoke.New(&fake{}, invoke.Config{}); err == nil {
		t.Error("an empty function was accepted")
	}
	if _, err := invoke.Open(context.Background(), invoke.Config{}); err == nil {
		t.Error("an empty config was accepted")
	}
}

func TestNotifyInvokesOnlyTheTargetsKind(t *testing.T) {
	api := &fake{status: 202}
	trigger, _ := invoke.New(api, invoke.Config{GitHub: "gh", Slack: "sl"})
	trigger.SetKind(func(target string) string {
		if target == "T123" {
			return invoke.KindSlack
		}
		return invoke.KindGitHub
	})
	if err := trigger.Notify(context.Background(), "T123"); err != nil {
		t.Fatal(err)
	}
	if len(api.calls) != 1 || aws.ToString(api.calls[0].FunctionName) != "sl" {
		t.Fatalf("calls %v, want only sl", api.calls)
	}
}

func TestNotifyOfAnUnknownTargetInvokesEveryFunction(t *testing.T) {
	api := &fake{status: 202}
	trigger, _ := invoke.New(api, invoke.Config{GitHub: "gh", Slack: "sl"})
	trigger.SetKind(func(string) string { return "" })
	if err := trigger.Notify(context.Background(), "who"); err != nil {
		t.Fatal(err)
	}
	if len(api.calls) != 2 {
		t.Fatalf("%d invocations, want one per function", len(api.calls))
	}
}

func TestTheAdapterIsRegisteredForTheTriggerConcernOnLambda(t *testing.T) {
	d, ok := port.Default.Lookup(port.ConcernTrigger, "invoke")
	if !ok || d.Factory == nil || !d.Works(port.RuntimeLambda) || d.Works(port.RuntimeKubernetes) {
		t.Fatalf("descriptor %+v, registered %v", d, ok)
	}
}
