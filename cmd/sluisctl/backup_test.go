package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/backup/restorejob"
	"github.com/truvity/sluis/internal/modcall"
)

const (
	testInstance = "example"
	testBackup   = "20261010T020000Z-3fa9c1"
	testARN      = "arn:aws:sts::111122223333:assumed-role/admin/oleg"
)

// fakeLambda records every invocation and answers module calls from results,
// keyed "module.method". Restore events are answered by onEvent.
type fakeLambda struct {
	mu      sync.Mutex
	calls   []*awslambda.InvokeInput
	results map[string]func() (any, error)
	status  func() restorejob.Status
	denied  bool
	// answer is the payload of a synchronous restore event.
	answer string
}

func (f *fakeLambda) Invoke(_ context.Context, in *awslambda.InvokeInput, _ ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	if f.denied {
		return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no"}
	}
	var env modcall.Request
	if json.Unmarshal(in.Payload, &env) == nil && env.Kind == modcall.Kind {
		var res any
		if env.Module == "restore" && env.Method == "status" {
			res = f.status()
		} else if fn := f.results[env.Module+"."+env.Method]; fn != nil {
			var err error
			if res, err = fn(); err != nil {
				return nil, err
			}
		}
		raw, _ := json.Marshal(res)
		out, _ := json.Marshal(modcall.Response{V: env.V, Result: raw})
		return &awslambda.InvokeOutput{StatusCode: 200, Payload: out}, nil
	}
	if in.InvocationType == lambdatypes.InvocationTypeEvent {
		return &awslambda.InvokeOutput{StatusCode: 202}, nil
	}
	return &awslambda.InvokeOutput{StatusCode: 200, Payload: []byte(f.answer)}, nil
}

func (f *fakeLambda) events() []*awslambda.InvokeInput {
	var out []*awslambda.InvokeInput
	for _, c := range f.calls {
		var env modcall.Request
		if json.Unmarshal(c.Payload, &env) != nil || env.Kind != modcall.Kind {
			out = append(out, c)
		}
	}
	return out
}

type fakeSTS struct{}

func (fakeSTS) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	arn := testARN
	return &sts.GetCallerIdentityOutput{Arn: &arn}, nil
}

// withFake installs the fake clients, an empty config directory with the two
// functions and the instance in the environment, and a clock that does not wait.
func withFake(t *testing.T, f *fakeLambda) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv(envBackupFunction, "sluis-backup")
	t.Setenv(envRestoreFunction, "sluis-restore")
	t.Setenv(envInstance, testInstance)
	savedClients, savedSleep, savedInterval := newBackupClients, pollSleep, pollInterval
	t.Cleanup(func() { newBackupClients, pollSleep, pollInterval = savedClients, savedSleep, savedInterval })
	newBackupClients = func(context.Context, string, string) (backupClients, error) {
		return backupClients{Lambda: f, STS: fakeSTS{}}, nil
	}
	pollSleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	savedWindow := discoverWindow
	discoverWindow = 0
	t.Cleanup(func() { discoverWindow = savedWindow })
}

func runRestore(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return captureStdoutErr(t, func() error { return restoreCommand(args) })
}

func startedRun(state string) restorejob.Run {
	now := time.Now()
	return restorejob.Run{ID: "20261010T030000Z-a1b2c3", BackupID: testBackup, State: state, Started: now, Updated: now}
}

func TestRestoreStartRefusesWithoutTheTypedInstanceAndInvokesNothing(t *testing.T) {
	for name, args := range map[string][]string{
		"missing":   {"start", testBackup},
		"wrong":     {"start", testBackup, "--confirm", "other"},
		"wrong-env": {"start", "--confirm", "Example", testBackup},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeLambda{status: func() restorejob.Status { return restorejob.Status{} }}
			withFake(t, f)
			_, err := runRestore(t, args...)
			var usage usageError
			if !errors.As(err, &usage) {
				t.Fatalf("err = %v, want a usage error", err)
			}
			if len(f.calls) != 0 {
				t.Fatalf("%d invocations before the guard held", len(f.calls))
			}
		})
	}
}

func TestRestoreStartNeedsToKnowTheInstance(t *testing.T) {
	f := &fakeLambda{status: func() restorejob.Status { return restorejob.Status{} }}
	withFake(t, f)
	t.Setenv(envInstance, "")
	_, err := runRestore(t, "start", testBackup, "--confirm", testInstance)
	var usage usageError
	if !errors.As(err, &usage) || len(f.calls) != 0 {
		t.Fatalf("err = %v, calls = %d", err, len(f.calls))
	}
}

func TestRestoreStartSendsTheEventAsyncAsTheCallerAndPrintsTheRunID(t *testing.T) {
	var started bool
	f := &fakeLambda{}
	f.status = func() restorejob.Status {
		if !started {
			return restorejob.Status{}
		}
		r := startedRun(restorejob.StateRunning)
		return restorejob.Status{Latest: &r, Unfinished: &r}
	}
	withFake(t, f)
	savedSleep := pollSleep
	pollSleep = func(ctx context.Context, _ time.Duration) error { started = true; return savedSleep(ctx, 0) }
	discoverWindow = time.Hour
	savedNow := nowFunc
	t.Cleanup(func() { nowFunc = savedNow })
	n := 0
	nowFunc = func() time.Time { n++; return time.Now().Add(time.Duration(n) * time.Second) }

	out, err := runRestore(t, "start", testBackup, "--confirm", testInstance, "--overwrite", "--note", "after the incident")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "run id: 20261010T030000Z-a1b2c3") {
		t.Errorf("the run id is not printed: %q", out)
	}
	events := f.events()
	if len(events) != 1 {
		t.Fatalf("%d restore events", len(events))
	}
	in := events[0]
	if in.InvocationType != lambdatypes.InvocationTypeEvent || *in.Qualifier != "live-admin" || *in.FunctionName != "sluis-restore" {
		t.Errorf("invocation = %v %v %v", in.InvocationType, *in.Qualifier, *in.FunctionName)
	}
	var ev map[string]any
	if err = json.Unmarshal(in.Payload, &ev); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"kind": "restore", "backup": testBackup, "overwrite": true, "by": testARN, "note": "after the incident"}
	if len(ev) != len(want) {
		t.Errorf("event = %v, want %v", ev, want)
	}
	for k, v := range want {
		if ev[k] != v {
			t.Errorf("event[%s] = %v, want %v", k, ev[k], v)
		}
	}
}

func TestRestoreStartWaitPollsUntilCompletedAndFailedIsAnError(t *testing.T) {
	for _, tc := range []struct {
		final   string
		wantErr bool
	}{{restorejob.StateCompleted, false}, {restorejob.StateFailed, true}} {
		t.Run(tc.final, func(t *testing.T) {
			states := []string{restorejob.StateRunning, restorejob.StatePaused, restorejob.StateRunning, tc.final}
			f := &fakeLambda{}
			polls := 0
			f.status = func() restorejob.Status {
				// The first read is the one before the start; the rest follow it.
				polls++
				if polls == 1 {
					return restorejob.Status{}
				}
				i := polls - 2
				if i >= len(states) {
					i = len(states) - 1
				}
				r := startedRun(states[i])
				r.Reason = "verify"
				return restorejob.Status{Latest: &r}
			}
			withFake(t, f)
			discoverWindow = time.Hour
			ticks := 0
			pollSleep = func(ctx context.Context, _ time.Duration) error { ticks++; return ctx.Err() }

			out, err := runRestore(t, "start", testBackup, "--confirm", testInstance, "--wait")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if !strings.Contains(out, "restore "+"20261010T030000Z-a1b2c3: "+tc.final) {
				t.Errorf("output = %q", out)
			}
			if polls < 5 {
				t.Errorf("polled %d times, expected every state to be read", polls)
			}
		})
	}
}

func TestStatusIsCalledThroughTheAliasOfTheChosenClass(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		alias string
	}{
		{[]string{"status"}, "live-admin"},
		{[]string{"status", "--as", "breakglass"}, "live-breakglass"},
	} {
		f := &fakeLambda{status: func() restorejob.Status { return restorejob.Status{} }}
		withFake(t, f)
		if _, err := runRestore(t, tc.args...); err != nil {
			t.Fatal(err)
		}
		if len(f.calls) != 1 || *f.calls[0].Qualifier != tc.alias {
			t.Fatalf("calls = %v, want one through %s", f.calls, tc.alias)
		}
		var env modcall.Request
		_ = json.Unmarshal(f.calls[0].Payload, &env)
		if env.Module != "restore" || env.Method != "status" {
			t.Errorf("call = %s.%s", env.Module, env.Method)
		}
	}
	f := &fakeLambda{}
	withFake(t, f)
	_, err := runRestore(t, "status", "--as", "console")
	var usage usageError
	if !errors.As(err, &usage) || len(f.calls) != 0 {
		t.Errorf("--as console: %v", err)
	}
}

func TestBackupCallsAreModuleCallsOnTheBackupFunction(t *testing.T) {
	now := time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC)
	view := job.View{ID: testBackup, State: job.StateCompleted, Started: now, Modules: 6, Records: 42, Chunks: 3}
	f := &fakeLambda{results: map[string]func() (any, error){
		"backup.status": func() (any, error) { return job.Status{Latest: &view, LastCompleted: &view}, nil },
		"backup.list": func() (any, error) {
			return map[string]any{"backups": []job.Info{{ID: testBackup, Created: now, Modules: 6, Records: 42, Layout: "v5"}}}, nil
		},
		"backup.run": func() (any, error) { return job.Result{Outcome: "completed", ID: testBackup, Run: &view}, nil },
	}}
	withFake(t, f)
	for _, tc := range []struct {
		args    []string
		method  string
		payload string
		want    string
	}{
		{[]string{"status"}, "status", `{}`, testBackup},
		{[]string{"list", "--limit", "3"}, "list", `{"limit":3}`, "42 records"},
		{[]string{"run", "--resume", "--as", "breakglass"}, "run", `{"resume":true}`, "backup: completed"},
	} {
		f.calls = nil
		out, err := captureStdoutErr(t, func() error { return backupCommand(tc.args) })
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("%v: output %q lacks %q", tc.args, out, tc.want)
		}
		var env modcall.Request
		_ = json.Unmarshal(f.calls[0].Payload, &env)
		wantAlias := "live-admin"
		if len(tc.args) > 3 {
			wantAlias = "live-breakglass"
		}
		if env.Module != "backup" || env.Method != tc.method || string(env.Payload) != tc.payload ||
			*f.calls[0].Qualifier != wantAlias || *f.calls[0].FunctionName != "sluis-backup" {
			t.Errorf("%v: call = %s.%s %s via %s %s", tc.args, env.Module, env.Method, env.Payload, *f.calls[0].Qualifier, *f.calls[0].FunctionName)
		}
	}
}

func TestBackupJSONIsTheAnswerAndRefusalIsNotGranted(t *testing.T) {
	f := &fakeLambda{results: map[string]func() (any, error){
		"backup.status": func() (any, error) { return job.Status{}, nil },
	}}
	withFake(t, f)
	out, err := captureStdoutErr(t, func() error { return backupCommand([]string{"status", "--json"}) })
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	f.denied = true
	if err = backupCommand([]string{"status"}); !errors.Is(err, errNotGranted) {
		t.Errorf("err = %v, want not granted", err)
	}
}

func TestPreviewIsSynchronousAndPrintsCountsNeverValues(t *testing.T) {
	f := &fakeLambda{answer: `{"kind":"restore","outcome":"preview","backup":"` + testBackup + `","report":{"installation":"example","id":"` + testBackup +
		`","layout":"v5","created":"x","sections":[{"module":"oidc","section":"state","create":2,"overwrite":1,"same":5,"expired":0,"regenerated":3,` +
		`"items":[{"type":"state","name":"rec.x","action":"overwrite"}]}]}}`}
	withFake(t, f)
	out, err := runRestore(t, "preview", testBackup)
	if err != nil {
		t.Fatal(err)
	}
	events := f.events()
	if len(events) != 1 || events[0].InvocationType != lambdatypes.InvocationTypeRequestResponse ||
		string(events[0].Payload) != `{"kind":"restore","backup":"`+testBackup+`","preview":true}` {
		t.Fatalf("events = %v", events)
	}
	for _, want := range []string{"nothing was written", "create 2", "overwrite 1", "needs --overwrite"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q: %q", want, out)
		}
	}
}

func TestResumeWithNothingUnfinishedInvokesNothing(t *testing.T) {
	f := &fakeLambda{status: func() restorejob.Status { return restorejob.Status{} }}
	withFake(t, f)
	out, err := runRestore(t, "resume")
	if err != nil || !strings.Contains(out, "nothing to resume") || len(f.events()) != 0 {
		t.Fatalf("out = %q, err = %v, events = %d", out, err, len(f.events()))
	}
	r := startedRun(restorejob.StatePaused)
	f.status = func() restorejob.Status { return restorejob.Status{Unfinished: &r} }
	if _, err = runRestore(t, "resume", "--as", "breakglass"); err != nil {
		t.Fatal(err)
	}
	events := f.events()
	if len(events) != 1 || string(events[0].Payload) != `{"kind":"restore","resume":true}` ||
		events[0].InvocationType != lambdatypes.InvocationTypeEvent || *events[0].Qualifier != "live-breakglass" {
		t.Fatalf("events = %v", events)
	}
}

func TestTheFunctionComesFromTheFlagThenTheEnvironmentThenConfig(t *testing.T) {
	f := &fakeLambda{results: map[string]func() (any, error){"backup.status": func() (any, error) { return job.Status{}, nil }}}
	withFake(t, f)
	t.Setenv(envBackupFunction, "")
	if err := backupCommand([]string{"status"}); err == nil {
		t.Fatal("no function configured and the command ran")
	}
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, "backup:\n  function: from-config\n", 0o600)
	if err = backupCommand([]string{"status"}); err != nil {
		t.Fatal(err)
	}
	if got := *f.calls[len(f.calls)-1].FunctionName; got != "from-config" {
		t.Errorf("function = %q", got)
	}
	t.Setenv(envBackupFunction, "from-env")
	if err = backupCommand([]string{"status", "--function", "from-flag"}); err != nil {
		t.Fatal(err)
	}
	if got := *f.calls[len(f.calls)-1].FunctionName; got != "from-flag" {
		t.Errorf("function = %q", got)
	}
}
