package rpc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/backup/rpc"
	"github.com/truvity/sluis/internal/modcall"
)

var at = time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC)

// fake is a Service with fixed answers; it keeps the last request of Run.
type fake struct{ ran *job.Request }

func (f *fake) Status(context.Context) (job.Status, error) {
	v := job.View{ID: "20261010T020000Z-a1b2c3", State: job.StateCompleted, Trigger: job.TriggerSchedule, Creator: "sluis-backup 1.75.0",
		Started: at, Updated: at.Add(time.Minute), Finished: at.Add(time.Minute), Modules: 5, Records: 120, Chunks: 9}
	return job.Status{Latest: &v, LastCompleted: &v, Retention: &job.Pass{At: at, Keep: 7, MaxAge: "720h0m0s", Kept: 7}}, nil
}

func (f *fake) List(context.Context) ([]job.Info, error) {
	return []job.Info{
		{ID: "20261010T020000Z-a1b2c3", Created: at, Creator: "sluis-backup 1.75.0", Format: 1, Layout: "v5", Modules: 5, Records: 120, Chunks: 9},
		{ID: "20261009T020000Z-d4e5f6", Created: at.Add(-24 * time.Hour), Creator: "sluis-backup 1.75.0", Format: 1, Layout: "v5", Modules: 5, Records: 118, Chunks: 9},
	}, nil
}

func (f *fake) Run(_ context.Context, req job.Request) (job.Result, error) {
	f.ran = &req
	v := job.View{ID: "20261010T020000Z-a1b2c3", State: job.StatePaused, Trigger: job.TriggerRun, Started: at, Updated: at, Units: 40, Done: 3}
	return job.Result{Outcome: job.OutcomePaused, ID: v.ID, Run: &v}, nil
}

func server(f *fake) *modcall.Server {
	s := modcall.NewServer(rpc.Module)
	rpc.Register(s, f)
	return s
}

func call(s *modcall.Server, caller, method, payload string) modcall.Response {
	return s.Dispatch(modcall.WithCaller(context.Background(), caller), modcall.Request{V: 2, Kind: modcall.Kind, Module: rpc.Module, Method: method, Payload: json.RawMessage(payload)})
}

func TestWhoMayCallWhat(t *testing.T) {
	f := &fake{}
	s := server(f)
	for _, c := range []struct {
		caller, method string
		ok             bool
	}{
		{rpc.CallerConsole, rpc.MethodStatus, true}, {rpc.CallerConsole, rpc.MethodList, true}, {rpc.CallerConsole, rpc.MethodRun, false},
		{rpc.CallerAdmin, rpc.MethodStatus, true}, {rpc.CallerAdmin, rpc.MethodRun, true},
		{rpc.CallerBreakglass, rpc.MethodRun, true},
		{"issuer", rpc.MethodStatus, false}, {"issuer", rpc.MethodRun, false}, {"", rpc.MethodList, false},
	} {
		resp := call(s, c.caller, c.method, `{}`)
		if got := resp.Error == nil; got != c.ok {
			t.Errorf("%q calls %s: allowed %v, want %v (%+v)", c.caller, c.method, got, c.ok, resp.Error)
		}
		if resp.Error != nil && resp.Error.Code != modcall.CodeForbidden {
			t.Errorf("%q calls %s: code %q", c.caller, c.method, resp.Error.Code)
		}
	}
}

func TestRunCarriesTheCallerClassAsTheActorAndResume(t *testing.T) {
	f := &fake{}
	resp := call(server(f), rpc.CallerBreakglass, rpc.MethodRun, `{"resume":true}`)
	if resp.Error != nil || f.ran == nil {
		t.Fatalf("%+v", resp)
	}
	if f.ran.Trigger != job.TriggerRun || !f.ran.ResumeOnly || f.ran.Actor != audit.Workload("lambda:breakglass") {
		t.Errorf("request %+v", *f.ran)
	}
}

func TestListHonoursItsLimit(t *testing.T) {
	var out rpc.ListResponse
	resp := call(server(&fake{}), rpc.CallerConsole, rpc.MethodList, `{"limit":1}`)
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil || len(out.Backups) != 1 || out.Backups[0].ID != "20261010T020000Z-a1b2c3" {
		t.Errorf("%+v, %v", out, err)
	}
}

// A failure the service has is an internal error with no detail: the caller is
// another module's trust domain.
func TestAFailureCrossesTheBoundaryWithoutItsCause(t *testing.T) {
	s := modcall.NewServer(rpc.Module)
	rpc.Register(s, &failing{})
	resp := call(s, rpc.CallerAdmin, rpc.MethodRun, `{}`)
	if resp.Error == nil || resp.Error.Code != modcall.CodeInternal || resp.Error.Message != "" {
		t.Errorf("%+v", resp.Error)
	}
}

type failing struct{ fake }

func (*failing) Run(context.Context, job.Request) (job.Result, error) {
	return job.Result{}, errors.New("the bucket s3://secret-name refused")
}

// releasedDir holds, per release, one file per method (the pattern of
// internal/cloudflare/rpc): the envelope a caller of that release wrote and the
// answer it got, for the current protocol version and the one before. A later
// release replays every file; a file is never edited after its release.
const releasedDir = "../../modcall/testdata/released"

type golden struct {
	Module     string          `json:"module"`
	Method     string          `json:"method"`
	Caller     string          `json:"caller"`
	Request    json.RawMessage `json:"request"`
	RequestV1  json.RawMessage `json:"request_v1"`
	Response   json.RawMessage `json:"response"`
	ResponseV1 json.RawMessage `json:"response_v1"`
}

func dispatch(t *testing.T, raw json.RawMessage, caller string) json.RawMessage {
	t.Helper()
	var req modcall.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(server(&fake{}).Dispatch(modcall.WithCaller(context.Background(), caller), req))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameJSON(t *testing.T, name string, got, want json.RawMessage) {
	t.Helper()
	var a, b bytes.Buffer
	if err := json.Indent(&a, got, "", " "); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if err := json.Indent(&b, want, "", " "); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if a.String() != b.String() {
		t.Errorf("%s: the answer changed\n got: %s\nwant: %s", name, got, want)
	}
}

func TestEveryReleasedEnvelopeStillDecodesAndIsAnsweredAsBefore(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(releasedDir, "v*", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var g golden
		if err = json.Unmarshal(raw, &g); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if g.Module != rpc.Module {
			continue
		}
		seen[g.Method]++
		sameJSON(t, f+" (v2)", dispatch(t, g.Request, g.Caller), g.Response)
		sameJSON(t, f+" (v1)", dispatch(t, g.RequestV1, g.Caller), g.ResponseV1)
	}
	for _, m := range []string{rpc.MethodStatus, rpc.MethodList, rpc.MethodRun} {
		if seen[m] == 0 {
			t.Errorf("no released golden for %s.%s under %s", rpc.Module, m, releasedDir)
		}
	}
}

// TestWriteMissingGoldens writes the files of the version in SLUIS_GOLDEN_DIR
// that do not exist yet. It never overwrites one: a released file is history.
func TestWriteMissingGoldens(t *testing.T) {
	dir := os.Getenv("SLUIS_GOLDEN_DIR")
	if dir == "" {
		t.Skip("set SLUIS_GOLDEN_DIR to write the goldens of a new release")
	}
	for _, c := range []struct{ method, class, payload string }{
		{rpc.MethodStatus, rpc.CallerConsole, `{}`},
		{rpc.MethodList, rpc.CallerConsole, `{"limit":2}`},
		{rpc.MethodRun, rpc.CallerAdmin, `{"resume":true}`},
	} {
		path := filepath.Join(dir, rpc.Module+"."+c.method+".json")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		env := func(v int) json.RawMessage {
			r := modcall.Request{V: v, Kind: modcall.Kind, Module: rpc.Module, Method: c.method, Payload: json.RawMessage(c.payload)}
			if v >= 2 {
				r.Traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
				r.Deadline = 4102444800000
			}
			b, _ := json.Marshal(r)
			return b
		}
		g := golden{Module: rpc.Module, Method: c.method, Caller: c.class, Request: env(2), RequestV1: env(0)}
		g.Response, g.ResponseV1 = dispatch(t, g.Request, c.class), dispatch(t, g.RequestV1, c.class)
		out, _ := json.MarshalIndent(g, "", "  ")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
