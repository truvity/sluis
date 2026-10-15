//nolint:lll // generated handler signatures and fixtures are one-line tables
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	sluisv1 "github.com/truvity/sluis/gen/sluis/v1"
	"github.com/truvity/sluis/gen/sluis/v1/sluisv1connect"
	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/backup/restorejob"
)

const (
	routeBackupARN  = "arn:aws:lambda:eu-west-1:111122223333:function:example-backup"
	routeRestoreARN = "arn:aws:lambda:eu-west-1:111122223333:function:example-restore"
	routeToken      = "laptop-access-token"
)

// fakeBackupIssuer is an issuer with the console's BackupService and the configuration
// document, counting what reached it.
type fakeBackupIssuer struct {
	sluisv1connect.UnimplementedBackupServiceHandler
	server *httptest.Server

	mu sync.Mutex
	// calls are the console procedures called, in order; bearers the
	// Authorization headers they carried.
	calls   []string
	bearers []string
	// configs counts the document fetches, and config is what it answers;
	// configStatus overrides the status.
	configs      int
	config       string
	configStatus int
	// denied makes every call a permission error.
	denied bool
	// outcome is what RunBackup answers (started when empty); runState is the
	// state the run it started shows in the status (completed when empty);
	// polls counts the status reads after a start.
	outcome, runState string
	polls             int
	started           bool
}

func (f *fakeBackupIssuer) record(name string, h http.Header) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	f.bearers = append(f.bearers, h.Get("Authorization"))
}

func (f *fakeBackupIssuer) refuse() error {
	if f.denied {
		return connect.NewError(connect.CodePermissionDenied, errors.New("starting a backup needs a group that the policy's backup.runGroups names"))
	}
	return nil
}

func (f *fakeBackupIssuer) RunBackup(_ context.Context, req *connect.Request[sluisv1.RunBackupRequest]) (*connect.Response[sluisv1.RunBackupResponse], error) {
	f.record("RunBackup", req.Header())
	if err := f.refuse(); err != nil {
		return nil, err
	}
	outcome := f.outcome
	if outcome == "" {
		outcome = "started"
	}
	f.mu.Lock()
	f.started = true
	f.mu.Unlock()
	// The answer is at once and holds no run, as the issuer's does.
	return connect.NewResponse(&sluisv1.RunBackupResponse{Available: true, Outcome: outcome, Started: timestamppb.Now()}), nil
}

func (f *fakeBackupIssuer) GetBackupStatus(_ context.Context, req *connect.Request[sluisv1.GetBackupStatusRequest]) (*connect.Response[sluisv1.GetBackupStatusResponse], error) {
	f.record("GetBackupStatus", req.Header())
	if err := f.refuse(); err != nil {
		return nil, err
	}
	state := f.runState
	if state == "" {
		state = "completed"
	}
	f.mu.Lock()
	n := 0
	if f.started {
		f.polls++
		n = f.polls
	}
	f.mu.Unlock()
	// The first read of a run that ends shows it still going.
	if n == 1 && state != "running" {
		state = "running"
	}
	run := &sluisv1.BackupRun{Id: testBackup, State: state, Started: timestamppb.Now(), Modules: 6, Records: 42, Chunks: 3, Reason: "bucket", Error: "refused"}
	if state == "running" {
		return connect.NewResponse(&sluisv1.GetBackupStatusResponse{Available: true, Latest: run, Unfinished: run}), nil
	}
	return connect.NewResponse(&sluisv1.GetBackupStatusResponse{Available: true, Latest: run, LastCompleted: run,
		Retention: &sluisv1.BackupRetention{At: timestamppb.New(time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC)), Keep: 7, MaxAge: "720h", Kept: 7, Removed: 2, Cleaned: 1, Refused: 3}}), nil
}

func (f *fakeBackupIssuer) ListBackups(_ context.Context, req *connect.Request[sluisv1.ListBackupsRequest]) (*connect.Response[sluisv1.ListBackupsResponse], error) {
	f.record("ListBackups", req.Header())
	return connect.NewResponse(&sluisv1.ListBackupsResponse{Available: true, Backups: []*sluisv1.BackupInfo{
		{Id: testBackup, Created: timestamppb.New(time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC)), Modules: 6, Records: 42, Layout: "v5"}}}), nil
}

func (f *fakeBackupIssuer) GetRestoreStatus(_ context.Context, req *connect.Request[sluisv1.GetRestoreStatusRequest]) (*connect.Response[sluisv1.GetRestoreStatusResponse], error) {
	f.record("GetRestoreStatus", req.Header())
	return connect.NewResponse(&sluisv1.GetRestoreStatusResponse{Available: true,
		Latest: &sluisv1.RestoreRun{Id: "20261010T030000Z-a1b2c3", BackupId: testBackup, State: "completed", By: "someone"}}), nil
}

func (f *fakeBackupIssuer) GetRestorePreview(_ context.Context, req *connect.Request[sluisv1.GetRestorePreviewRequest]) (*connect.Response[sluisv1.GetRestorePreviewResponse], error) {
	f.record("GetRestorePreview", req.Header())
	return connect.NewResponse(&sluisv1.GetRestorePreviewResponse{Available: true, Outcome: "preview", Installation: "example", BackupId: req.Msg.GetBackupId(), Layout: "v5",
		Sections: []*sluisv1.RestorePreviewSection{{Module: "oidc", Section: "state", Create: 2, Overwrite: 1, Same: 5}}}), nil
}

func (f *fakeBackupIssuer) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// newFakeBackupIssuer serves the issuer, signs the laptop in at it with a live
// access token (so nothing is refreshed), and pins it with $SLUISCTL_ISSUER.
func newFakeBackupIssuer(t *testing.T) *fakeBackupIssuer {
	t.Helper()
	f := &fakeBackupIssuer{}
	mux := http.NewServeMux()
	path, handler := sluisv1connect.NewBackupServiceHandler(f)
	mux.Handle("/console"+path, http.StripPrefix("/console", handler))
	mux.HandleFunc(sluisConfigurationPath, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.configs++
		body, status := f.config, f.configStatus
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+routeToken {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if status != 0 {
			http.Error(w, "broken", status)
			return
		}
		_, _ = w.Write([]byte(body))
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	f.config = `{"issuer":"` + f.server.URL + `","instance":"example","region":"eu-west-1","backup":{"endpoint":"` + routeBackupARN + `"},"restore":{"endpoint":"` + routeRestoreARN + `"}}`
	if err := saveSession(f.server.URL, Session{RefreshToken: "refresh", AccessToken: routeToken, AccessExpires: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envIssuer, f.server.URL)
	return f
}

// routeEnv is withFake with the functions' environment cleared, so that what
// names a function is the test's choice.
func routeEnv(t *testing.T, l *fakeLambda) {
	t.Helper()
	withFake(t, l)
	t.Setenv(envBackupFunction, "")
	t.Setenv(envRestoreFunction, "")
	t.Setenv(envInstance, "")
	t.Setenv(envIssuer, "")
}

func captureRouteStderr(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	saved := backupStderr
	backupStderr = &buf
	t.Cleanup(func() { backupStderr = saved })
	return &buf
}

func runBackupCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return captureStdoutErr(t, func() error { return backupCommand(args) })
}

func TestSignedInTheBackupCommandsGoThroughTheIssuerWithTheLoginsToken(t *testing.T) {
	l := &fakeLambda{}
	routeEnv(t, l)
	iss := newFakeBackupIssuer(t)

	for _, tc := range []struct {
		run  func() (string, error)
		call string
		want string
	}{
		{func() (string, error) { return runBackupCmd(t, "run") }, "RunBackup", "backup: completed"},
		{func() (string, error) { return runBackupCmd(t, "status") }, "GetBackupStatus", "removed 2, orphans 1, refused 3"},
		{func() (string, error) { return runBackupCmd(t, "list", "--limit", "5") }, "ListBackups", "42 records"},
		{func() (string, error) { return runRestore(t, "status") }, "GetRestoreStatus", "20261010T030000Z-a1b2c3"},
		{func() (string, error) { return runRestore(t, "preview", testBackup) }, "GetRestorePreview", "create 2"},
	} {
		out, err := tc.run()
		if err != nil {
			t.Fatalf("%s: %v", tc.call, err)
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("%s: output %q lacks %q", tc.call, out, tc.want)
		}
	}
	calls := iss.called()
	if got := strings.Join(calls, " "); got != "RunBackup GetBackupStatus GetBackupStatus GetBackupStatus ListBackups GetRestoreStatus GetRestorePreview" {
		t.Errorf("issuer calls = %s", got)
	}
	for i, b := range iss.bearers {
		if b != "Bearer "+routeToken {
			t.Errorf("%s carried %q, not the login's token", calls[i], b)
		}
	}
	// No AWS credential was needed or used.
	if len(l.calls) != 0 {
		t.Errorf("%d AWS invocations on the issuer route", len(l.calls))
	}
}

func TestTheIssuerRoutePrintsTheSameJSONShapeAsTheDirectOne(t *testing.T) {
	routeEnv(t, &fakeLambda{})
	newFakeBackupIssuer(t)
	out, err := runBackupCmd(t, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var st job.Status
	if err = json.Unmarshal([]byte(out), &st); err != nil || st.Latest == nil || st.Latest.Records != 42 || st.Retention == nil || st.Retention.Keep != 7 {
		t.Errorf("%s, %v", out, err)
	}
}

func TestWithoutALoginTheCommandsAreDirect(t *testing.T) {
	l := &fakeLambda{results: map[string]func() (any, error){"backup.status": func() (any, error) { return job.Status{}, nil }}}
	routeEnv(t, l)
	t.Setenv(envBackupFunction, routeBackupARN)
	if _, err := runBackupCmd(t, "status"); err != nil {
		t.Fatal(err)
	}
	if len(l.calls) != 1 || *l.calls[0].FunctionName != routeBackupARN {
		t.Errorf("calls = %v", l.calls)
	}
}

func TestDirectAndTheFlagsOnlyTheDirectRouteHasKeepADirectCall(t *testing.T) {
	for name, args := range map[string][]string{
		"--direct":            {"status", "--direct"},
		"--as breakglass":     {"status", "--as", "breakglass"},
		"--endpoint":          {"status", "--endpoint", routeBackupARN},
		"--profile":           {"status", "--profile", "ops"},
		"--region":            {"status", "--region", "eu-west-1"},
		"the deprecated name": {"status", "--function", routeBackupARN},
	} {
		t.Run(name, func(t *testing.T) {
			l := &fakeLambda{results: map[string]func() (any, error){"backup.status": func() (any, error) { return job.Status{}, nil }}}
			routeEnv(t, l)
			iss := newFakeBackupIssuer(t)
			if _, err := runBackupCmd(t, args...); err != nil {
				t.Fatal(err)
			}
			if got := iss.called(); len(got) != 0 {
				t.Errorf("the issuer was called: %v", got)
			}
			if len(l.calls) != 1 {
				t.Errorf("%d AWS invocations", len(l.calls))
			}
		})
	}
}

// A restore must start when the issuer is broken, so these two never go
// through it, signed in or not.
func TestRestoreStartAndResumeNeverGoThroughTheIssuer(t *testing.T) {
	r := startedRun(restorejob.StatePaused)
	l := &fakeLambda{status: func() restorejob.Status { return restorejob.Status{Unfinished: &r} }}
	routeEnv(t, l)
	iss := newFakeBackupIssuer(t)
	path, _ := configPath()
	writeTestFile(t, path, "backup:\n  instance: example\n  restoreEndpoint: "+routeRestoreARN+"\n", 0o600)

	if _, err := runRestore(t, "start", testBackup, "--confirm", "example"); err != nil {
		t.Fatal(err)
	}
	if _, err := runRestore(t, "resume"); err != nil {
		t.Fatal(err)
	}
	if got := iss.called(); len(got) != 0 {
		t.Fatalf("the console was called for a restore: %v", got)
	}
	events := l.events()
	if len(events) != 2 {
		t.Fatalf("%d restore invocations, want 2", len(events))
	}
	for _, e := range events {
		if *e.FunctionName != routeRestoreARN || *e.Qualifier != "live-admin" {
			t.Errorf("invoked %s:%s", *e.FunctionName, *e.Qualifier)
		}
	}
	// And a broken issuer changes nothing: the document is the saved copy.
	iss.server.Close()
	buf := captureRouteStderr(t)
	if _, err := runRestore(t, "resume"); err != nil {
		t.Fatalf("with the issuer down: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a resume tried the issuer: %q", buf.String())
	}
}

func TestStartIsHeldToTheSavedInstanceName(t *testing.T) {
	l := &fakeLambda{status: func() restorejob.Status { return restorejob.Status{} }}
	routeEnv(t, l)
	iss := newFakeBackupIssuer(t)
	// The laptop was told "example" when it signed in; the issuer now says
	// otherwise, which must not move the guard.
	iss.config = strings.Replace(iss.config, `"instance":"example"`, `"instance":"other"`, 1)
	path, _ := configPath()
	writeTestFile(t, path, "backup:\n  instance: example\n  restoreEndpoint: "+routeRestoreARN+"\n", 0o600)

	_, err := runRestore(t, "start", testBackup, "--confirm", "other")
	var usage usageError
	if !errors.As(err, &usage) || len(l.calls) != 0 {
		t.Fatalf("a name that is only the live document's was accepted: %v, %d calls", err, len(l.calls))
	}
	if _, err = runRestore(t, "start", testBackup, "--confirm", "example"); err != nil {
		t.Fatal(err)
	}
}

// A restore starts from what this laptop was told, never from the issuer's
// live word: a document that moves the target or the guard would let a broken or
// hostile issuer aim a restore.
func TestAHostileLiveDocumentCannotAimARestoreStartOrResume(t *testing.T) {
	const hostile = "arn:aws:lambda:eu-west-1:444455556666:function:elsewhere"
	r := startedRun(restorejob.StatePaused)
	l := &fakeLambda{status: func() restorejob.Status { return restorejob.Status{Unfinished: &r} }}
	routeEnv(t, l)
	iss := newFakeBackupIssuer(t)
	iss.config = `{"instance":"hostile","region":"eu-west-1","backup":{"endpoint":"` + hostile + `"},"restore":{"endpoint":"` + hostile + `"}}`
	path, _ := configPath()
	writeTestFile(t, path, "backup:\n  instance: example\n  restoreEndpoint: "+routeRestoreARN+"\n", 0o600)

	// The guard is the saved name, not the document's.
	var usage usageError
	if _, err := runRestore(t, "start", testBackup, "--confirm", "hostile"); !errors.As(err, &usage) || len(l.calls) != 0 {
		t.Fatalf("the document's instance guarded a start: %v, %d calls", err, len(l.calls))
	}
	if _, err := runRestore(t, "start", testBackup, "--confirm", "example"); err != nil {
		t.Fatal(err)
	}
	if _, err := runRestore(t, "resume"); err != nil {
		t.Fatal(err)
	}
	for _, c := range l.calls {
		if got := *c.FunctionName; got != routeRestoreARN {
			t.Errorf("invoked %q: the document moved the target", got)
		}
	}
	if iss.configs != 0 {
		t.Errorf("the document was fetched %d times for a start or resume", iss.configs)
	}
	// The saved copy is untouched.
	if saved, _ := readConfigFile(); saved.Backup.RestoreEndpoint != routeRestoreARN || saved.Backup.Instance != "example" {
		t.Errorf("saved = %+v", saved.Backup)
	}
}

// With nothing saved and no --instance, a start has no name to hold --confirm to.
func TestStartRefusesWithoutASavedOrExplicitInstance(t *testing.T) {
	l := &fakeLambda{status: func() restorejob.Status { return restorejob.Status{} }}
	routeEnv(t, l)
	newFakeBackupIssuer(t)
	path, _ := configPath()
	writeTestFile(t, path, "backup:\n  restoreEndpoint: "+routeRestoreARN+"\n", 0o600)
	var usage usageError
	if _, err := runRestore(t, "start", testBackup, "--confirm", "example"); !errors.As(err, &usage) || len(l.calls) != 0 {
		t.Fatalf("a start with no known instance: %v, %d calls", err, len(l.calls))
	}
	if _, err := runRestore(t, "start", testBackup, "--confirm", "example", "--instance", "example"); err != nil {
		t.Fatal(err)
	}
}

// directFunction runs `backup status --direct` and returns the function it invoked.
func directFunction(t *testing.T, l *fakeLambda, args ...string) string {
	t.Helper()
	l.calls = nil
	if _, err := runBackupCmd(t, append([]string{"status", "--direct"}, args...)...); err != nil {
		t.Fatal(err)
	}
	if len(l.calls) != 1 {
		t.Fatalf("%d invocations", len(l.calls))
	}
	return *l.calls[0].FunctionName
}

// --endpoint, then the environment (the new name and the old), then the
// issuer's live document, then the copy saved at login.
func TestTheEndpointComesFromTheFlagThenTheEnvironmentThenTheIssuerThenTheSavedCopy(t *testing.T) {
	l := &fakeLambda{results: map[string]func() (any, error){"backup.status": func() (any, error) { return job.Status{}, nil }}}
	routeEnv(t, l)
	iss := newFakeBackupIssuer(t)
	path, _ := configPath()
	const savedARN = "arn:aws:lambda:eu-west-1:111122223333:function:saved-backup"
	writeTestFile(t, path, "backup:\n  endpoint: "+savedARN+"\n", 0o600)
	stderrBuf := captureRouteStderr(t)

	// 4. the live document wins over the saved copy.
	if got := directFunction(t, l); got != routeBackupARN {
		t.Errorf("live: %q", got)
	}
	// 3. the environment wins over the live document; the new name over the old.
	t.Setenv(envBackupFunction, "old-env-function")
	if got := directFunction(t, l); got != "old-env-function" {
		t.Errorf("old env: %q", got)
	}
	t.Setenv(envBackupEndpoint, "arn:aws:lambda:eu-west-1:111122223333:function:new-env")
	if got := directFunction(t, l); got != "arn:aws:lambda:eu-west-1:111122223333:function:new-env" {
		t.Errorf("new env: %q", got)
	}
	// 1. the flag wins over the environment.
	if got := directFunction(t, l, "--endpoint", "arn:aws:lambda:eu-west-1:111122223333:function:flag"); got != "arn:aws:lambda:eu-west-1:111122223333:function:flag" {
		t.Errorf("flag: %q", got)
	}
	t.Setenv(envBackupFunction, "")
	t.Setenv(envBackupEndpoint, "")

	// 5. the issuer down: the saved copy, and it says so.
	iss.mu.Lock()
	iss.configStatus = http.StatusInternalServerError
	iss.mu.Unlock()
	stderrBuf.Reset()
	if got := directFunction(t, l); got != savedARN {
		t.Errorf("saved fallback: %q", got)
	}
	if !strings.Contains(stderrBuf.String(), "could not fetch the issuer's configuration") || !strings.Contains(stderrBuf.String(), "copy saved at login") {
		t.Errorf("the fallback was not said on stderr: %q", stderrBuf.String())
	}
}

func TestAnIssuerThatIsGoneStillLeavesTheSavedCopy(t *testing.T) {
	l := &fakeLambda{results: map[string]func() (any, error){"backup.status": func() (any, error) { return job.Status{}, nil }}}
	routeEnv(t, l)
	iss := newFakeBackupIssuer(t)
	path, _ := configPath()
	writeTestFile(t, path, "backup:\n  endpoint: "+routeBackupARN+"\n", 0o600)
	iss.server.Close()
	buf := captureRouteStderr(t)
	if got := directFunction(t, l); got != routeBackupARN {
		t.Errorf("function = %q", got)
	}
	if !strings.Contains(buf.String(), "copy saved at login") {
		t.Errorf("stderr = %q", buf.String())
	}
}

// The keys that `function` and `restoreFunction` were are read as endpoints, so
// a config.yaml from before keeps working; the new key wins when both are set.
func TestTheOlderConfigKeysAreStillReadAsEndpoints(t *testing.T) {
	l := &fakeLambda{results: map[string]func() (any, error){"backup.status": func() (any, error) { return job.Status{}, nil }}}
	routeEnv(t, l)
	path, _ := configPath()

	writeTestFile(t, path, "backup:\n  function: sluis-backup\n  restoreFunction: sluis-restore\n  instance: example\n", 0o600)
	if got := directFunction(t, l); got != "sluis-backup" {
		t.Errorf("function key: %q", got)
	}
	writeTestFile(t, path, "backup:\n  function: sluis-backup\n  endpoint: "+routeBackupARN+"\n", 0o600)
	if got := directFunction(t, l); got != routeBackupARN {
		t.Errorf("endpoint beats function: %q", got)
	}

	writeTestFile(t, path, "backup:\n  restoreFunction: sluis-restore\n  instance: example\n", 0o600)
	l.status = func() restorejob.Status { return restorejob.Status{} }
	if _, err := runRestore(t, "start", testBackup, "--confirm", "example"); err != nil {
		t.Fatal(err)
	}
	if events := l.events(); len(events) != 1 || *events[0].FunctionName != "sluis-restore" {
		t.Errorf("restoreFunction key: %v", events)
	}
}

func TestAnEndpointWhoseSchemeSluisctlCannotUseIsAUsageError(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(t *testing.T)
		args []string
	}{
		"flag k8s":      {func(*testing.T) {}, []string{"status", "--endpoint", "k8s://backup.ns"}},
		"flag https":    {func(*testing.T) {}, []string{"status", "--endpoint", "https://backup.example"}},
		"env":           {func(t *testing.T) { t.Setenv(envBackupEndpoint, "k8s://backup.ns") }, []string{"status"}},
		"another ARN":   {func(*testing.T) {}, []string{"status", "--endpoint", "arn:aws:s3:::bucket"}},
		"neither shape": {func(*testing.T) {}, []string{"status", "--endpoint", "a:b"}},
		"saved": {func(t *testing.T) {
			path, _ := configPath()
			writeTestFile(t, path, "backup:\n  endpoint: https://backup.example\n", 0o600)
		}, []string{"status"}},
	} {
		t.Run(name, func(t *testing.T) {
			l := &fakeLambda{}
			routeEnv(t, l)
			tc.set(t)
			_, err := runBackupCmd(t, tc.args...)
			var usage usageError
			if !errors.As(err, &usage) {
				t.Fatalf("err = %v, want a usage error", err)
			}
			if len(l.calls) != 0 {
				t.Errorf("%d AWS invocations for an endpoint that cannot be reached", len(l.calls))
			}
		})
	}
	if _, err := runBackupCmd(t, "status", "--endpoint", "k8s://backup.ns"); err == nil || !strings.Contains(err.Error(), "k8s://") {
		t.Errorf("the refusal does not name the scheme: %v", err)
	}
}

func TestTheDeprecatedFunctionFlagWarns(t *testing.T) {
	l := &fakeLambda{results: map[string]func() (any, error){"backup.status": func() (any, error) { return job.Status{}, nil }}}
	routeEnv(t, l)
	buf := captureRouteStderr(t)
	if _, err := runBackupCmd(t, "status", "--function", "sluis-backup"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "--function is deprecated") {
		t.Errorf("stderr = %q", buf.String())
	}
}

func TestTheIssuersRefusalsAreTheExitCodesOfAnyOtherCommand(t *testing.T) {
	routeEnv(t, &fakeLambda{})
	iss := newFakeBackupIssuer(t)
	iss.denied = true
	_, err := runBackupCmd(t, "run")
	if !errors.Is(err, errNotGranted) || !strings.Contains(err.Error(), "backup.runGroups") {
		t.Errorf("a denied run: %v", err)
	}
	if codeFor(err) != exitNotGranted {
		t.Errorf("code = %d", codeFor(err))
	}

	iss.server.Close()
	if _, err = runBackupCmd(t, "status"); !errors.Is(err, errUnreachable) {
		t.Errorf("an issuer that is down: %v", err)
	}
}

func TestLoginSavesWhereTheFunctionsAreAndKeepsWhatAPersonWrote(t *testing.T) {
	routeEnv(t, &fakeLambda{})
	iss := newFakeBackupIssuer(t)
	path, _ := configPath()
	writeTestFile(t, path, "clientId: sluisctl\nbackup:\n  function: by-hand\n  restoreFunction: by-hand-restore\n", 0o600)

	loginAfterBackup(t.Context(), Config{Issuer: iss.server.URL, ClientID: "sluisctl"})
	saved, err := readConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	b := saved.Backup
	if b.Endpoint != routeBackupARN || b.RestoreEndpoint != routeRestoreARN || b.Instance != "example" || b.Region != "eu-west-1" {
		t.Errorf("saved = %+v", b)
	}
	if b.Function != "by-hand" || b.RestoreFunction != "by-hand-restore" || saved.ClientID != "sluisctl" {
		t.Errorf("what a person wrote was lost: %+v", saved)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{"endpoint: " + routeBackupARN, "restoreEndpoint: " + routeRestoreARN} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("config.yaml lacks %q:\n%s", want, raw)
		}
	}
}

func TestLoginSaysNothingWhereTheIssuerPublishesNothing(t *testing.T) {
	routeEnv(t, &fakeLambda{})
	iss := newFakeBackupIssuer(t)
	iss.configStatus = http.StatusNotFound
	buf := captureRouteStderr(t)
	loginAfterBackup(t.Context(), Config{Issuer: iss.server.URL, ClientID: "sluisctl"})
	if buf.Len() != 0 {
		t.Errorf("stderr = %q", buf.String())
	}
	if saved, _ := readConfigFile(); saved.Backup != (BackupConfig{}) {
		t.Errorf("saved = %+v", saved.Backup)
	}
}

// The issuer starts the run and answers at once; sluisctl reads it back until it
// ends, and a failed run is an error as it is on the direct route.
func TestRunThroughTheIssuerStartsThenPollsUntilTheRunEnds(t *testing.T) {
	routeEnv(t, &fakeLambda{})
	iss := newFakeBackupIssuer(t)
	out, err := runBackupCmd(t, "run", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res job.Result
	if err = json.Unmarshal([]byte(out), &res); err != nil || res.Outcome != "completed" || res.Run == nil || res.Run.Records != 42 {
		t.Errorf("%s, %v", out, err)
	}
	if got := strings.Join(iss.called(), " "); got != "RunBackup GetBackupStatus GetBackupStatus" {
		t.Errorf("calls = %s", got)
	}

	iss2 := newFakeBackupIssuer(t)
	iss2.runState = "failed"
	if _, err = runBackupCmd(t, "run"); err == nil || !strings.Contains(err.Error(), "the backup failed") {
		t.Errorf("a failed run: %v", err)
	}
}

func TestNoWaitPrintsStartedAndDoesNotPoll(t *testing.T) {
	routeEnv(t, &fakeLambda{})
	iss := newFakeBackupIssuer(t)
	out, err := runBackupCmd(t, "run", "--no-wait")
	if err != nil || !strings.Contains(out, "backup: started") {
		t.Fatalf("%q, %v", out, err)
	}
	if got := iss.called(); len(got) != 1 || got[0] != "RunBackup" {
		t.Errorf("calls = %v", got)
	}
}

func TestAContendedRunIsReportedNotWaitedFor(t *testing.T) {
	routeEnv(t, &fakeLambda{})
	iss := newFakeBackupIssuer(t)
	iss.outcome = "contended"
	out, err := runBackupCmd(t, "run")
	if err != nil || !strings.Contains(out, "backup: contended") || len(iss.called()) != 1 {
		t.Errorf("%q, %v, %v", out, err, iss.called())
	}
}

func TestTheWaitIsBoundedByTheContext(t *testing.T) {
	routeEnv(t, &fakeLambda{})
	iss := newFakeBackupIssuer(t)
	iss.runState = "running"
	saved := pollSleep
	t.Cleanup(func() { pollSleep = saved })
	n := 0
	pollSleep = func(context.Context, time.Duration) error {
		if n++; n > 3 {
			return context.DeadlineExceeded
		}
		return nil
	}
	_, err := runBackupCmd(t, "run")
	if err == nil || !strings.Contains(err.Error(), "carries on in the function") {
		t.Errorf("err = %v", err)
	}
}
