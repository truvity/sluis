package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	sluisv1 "github.com/truvity/sluis/gen/sluis/v1"
	"github.com/truvity/sluis/gen/sluis/v1/sluisv1connect"
	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/backup/restore"
	"github.com/truvity/sluis/internal/backup/restorejob"
	"github.com/truvity/sluis/internal/port"
)

// The two ways `sluisctl backup` and `sluisctl restore` reach the functions
// (docs/reference/sluis/backup.md):
//
//   - through the issuer, with the sign-in `sluisctl login` holds, when there is
//     one: the issuer calls the backup module for the caller, so no AWS
//     credential is needed. backup run|status|list and restore preview|status.
//   - directly, with the caller's own AWS credentials: without a sign-in, with
//     --direct, and always for restore start and restore resume, which must work
//     when the issuer is broken or lost.
//
// Where the functions are comes from the issuer's /.well-known/sluis-configuration,
// which `login` saves in config.yaml's backup: section and a direct command
// fetches afresh.

const (
	envBackupEndpoint  = "SLUISCTL_BACKUP_ENDPOINT"
	envRestoreEndpoint = "SLUISCTL_RESTORE_ENDPOINT"

	// sluisConfigurationPath is the issuer's document about the installation.
	sluisConfigurationPath = "/.well-known/sluis-configuration"
	// consoleMount is where the issuer serves the console's API.
	consoleMount = "/console"

	arnLambdaPrefix = "arn:aws:lambda:"
)

// backupStderr is where the route's notes go: a seam for a test.
var backupStderr io.Writer = os.Stderr

// issuerConfiguration is the issuer's /.well-known/sluis-configuration.
type issuerConfiguration struct {
	Issuer   string `json:"issuer"`
	Instance string `json:"instance"`
	Region   string `json:"region"`
	Backup   struct {
		Endpoint string `json:"endpoint"`
	} `json:"backup"`
	Restore struct {
		Endpoint string `json:"endpoint"`
	} `json:"restore"`
}

// errNoConfiguration is the issuer answering 404: it publishes nothing.
var errNoConfiguration = errors.New("the issuer publishes no sluis-configuration")

// fetchConfiguration reads the issuer's document with the caller's sign-in.
func fetchConfiguration(ctx context.Context, cfg Config) (issuerConfiguration, error) {
	token, err := refresh(ctx, cfg)
	if err != nil {
		return issuerConfiguration{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Issuer+sluisConfigurationPath, nil)
	if err != nil {
		return issuerConfiguration{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return issuerConfiguration{}, fmt.Errorf("%w: %w", errUnreachable, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return issuerConfiguration{}, errNoConfiguration
	case http.StatusUnauthorized:
		return issuerConfiguration{}, errNotSignedIn
	default:
		return issuerConfiguration{}, fmt.Errorf("the issuer answered %s", response.Status)
	}
	var doc issuerConfiguration
	if err = json.Unmarshal(body, &doc); err != nil {
		return issuerConfiguration{}, fmt.Errorf("parse the issuer's configuration: %w", err)
	}
	return doc, nil
}

// saveBackupConfiguration saves the issuer's document in config.yaml's backup:
// section, under the keys the document owns: endpoint, restoreEndpoint, and,
// when it has them, instance and region. The older function and
// restoreFunction keys, which a person wrote, are left alone.
func saveBackupConfiguration(doc issuerConfiguration) error {
	saved, err := readConfigFile()
	if err != nil {
		return err
	}
	saved.Backup.Endpoint = doc.Backup.Endpoint
	saved.Backup.RestoreEndpoint = doc.Restore.Endpoint
	if doc.Instance != "" {
		saved.Backup.Instance = doc.Instance
	}
	if doc.Region != "" {
		saved.Backup.Region = doc.Region
	}
	return saveConfig(saved)
}

// loginAfterBackup is login's best-effort hook: it asks the freshly signed-in
// issuer where the functions are and saves the answer. A failure never fails a
// sign-in; an issuer with nothing to publish is no failure at all.
func loginAfterBackup(ctx context.Context, cfg Config) {
	doc, err := fetchConfiguration(ctx, cfg)
	switch {
	case errors.Is(err, errNoConfiguration):
		return
	case err != nil:
		_, _ = fmt.Fprintf(backupStderr, "sluisctl: could not read where the backup functions are (%v); `sluisctl backup --endpoint` names one\n", err)
		return
	}
	if err = saveBackupConfiguration(doc); err != nil {
		_, _ = fmt.Fprintf(backupStderr, "sluisctl: could not save where the backup functions are: %v\n", err)
	}
}

// backupLogin finds the issuer a backup command could go through: the one
// --issuer, $SLUISCTL_ISSUER, the issuer: key or the single live session names,
// provided this laptop holds a login there. ok is false for no login, and then
// the command is direct. A choice that cannot be made (two live sessions, none
// chosen) is an error, as it is for every other command.
func backupLogin(issuerFlag string) (cfg Config, ok bool, err error) {
	file, err := readConfigFile()
	if err != nil {
		return Config{}, false, err
	}
	pinned := firstNonEmpty(issuerFlag, os.Getenv(envIssuer), file.Issuer)
	if pinned == "" && len(liveSessionIssuers()) == 0 {
		return Config{}, false, nil
	}
	cfg, err = loadConfig(issuerFlag, "")
	if err != nil {
		return Config{}, false, err
	}
	if _, err = loadSession(cfg.Issuer); err != nil {
		return cfg, false, nil //nolint:nilerr // no login at this issuer is the direct route, not a failure
	}
	return cfg, true, nil
}

// lambdaFunction turns an endpoint into the function the Lambda API is given.
//
// The scheme of an endpoint names the transport. Only a Lambda function
// (`arn:aws:lambda:...`) is implemented; a bare name, which the older
// `function` key and $SLUISCTL_*_FUNCTION held, is read as a function in the
// caller's own account and region. `k8s://` and `https://` are reserved, and
// refused as what they are rather than guessed at.
func lambdaFunction(endpoint string) (string, error) {
	e := strings.TrimSpace(endpoint)
	switch {
	case strings.HasPrefix(e, arnLambdaPrefix):
		return e, nil
	case strings.HasPrefix(e, "arn:"):
		return "", badUsage("the endpoint %q is an ARN of something other than a Lambda function", e)
	case strings.Contains(e, "://"):
		scheme, _, _ := strings.Cut(e, "://")
		return "", badUsage("sluisctl cannot reach a %s:// endpoint yet (only a Lambda function's ARN); "+
			"go through the issuer by signing in with `sluisctl login`, or name a Lambda function with --endpoint", scheme)
	case strings.Contains(e, ":"):
		return "", badUsage("the endpoint %q is not a Lambda function's ARN or a function name", e)
	}
	return e, nil
}

// regionOf is the region of a Lambda ARN, or "".
func regionOf(function string) string {
	if !strings.HasPrefix(function, arnLambdaPrefix) {
		return ""
	}
	parts := strings.Split(function, ":")
	if len(parts) > 3 {
		return parts[3]
	}
	return ""
}

// resolveEndpoint settles where a direct command invokes: --endpoint (or its
// deprecated alias --function), then the environment, then the issuer's live
// document, then the copy saved at login. A live fetch that fails is said on
// stderr and the saved copy is used.
func resolveEndpoint(
	ctx context.Context, flagValue, envNew, envOld, saved string, login *Config, restoreCommand bool,
) (endpoint string, doc *issuerConfiguration) {
	if v := firstNonEmpty(flagValue, settingEnv(envNew), settingEnv(envOld)); v != "" {
		return v, nil
	}
	if login != nil {
		fetched, err := fetchConfiguration(ctx, *login)
		if err == nil {
			live := fetched.Backup.Endpoint
			if restoreCommand {
				live = fetched.Restore.Endpoint
			}
			if live != "" {
				return live, &fetched
			}
			// The issuer answered and names no function: nothing newer than the copy.
			return saved, &fetched
		}
		_, _ = fmt.Fprintf(backupStderr, "sluisctl: could not fetch the issuer's configuration (%v); using the copy saved at login\n", err)
	}
	return saved, nil
}

// ---------------------------------------------------------------------------
// The issuer route.

// backupBearer presents the caller's sign-in on every call.
type backupBearer struct {
	token string
	base  http.RoundTripper
}

func (b backupBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	base := b.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

// issuerBackup is the backup and restore functions as the issuer's console API
// shows them.
type issuerBackup struct {
	client sluisv1connect.BackupServiceClient
}

func newIssuerBackup(ctx context.Context, cfg Config) (*issuerBackup, error) {
	token, err := refresh(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &issuerBackup{client: sluisv1connect.NewBackupServiceClient(
		&http.Client{Transport: backupBearer{token: token.AccessToken}}, cfg.Issuer+consoleMount)}, nil
}

// issuerError says what a refusal by the issuer means for the caller.
func issuerError(err error) error {
	if err == nil {
		return nil
	}
	var coded *connect.Error
	if !errors.As(err, &coded) {
		return fmt.Errorf("%w: %w", errUnreachable, err)
	}
	// An Unavailable the client made up (a refused connection, a closed one) is
	// the issuer being down; one the issuer sent is a module that did not answer.
	if coded.Code() == connect.CodeUnavailable && !connect.IsWireError(err) {
		return fmt.Errorf("%w: %w", errUnreachable, err)
	}
	switch coded.Code() {
	case connect.CodeUnauthenticated:
		return errNotSignedIn
	case connect.CodePermissionDenied:
		return fmt.Errorf("%w: %s", errNotGranted, coded.Message())
	case connect.CodeUnimplemented:
		return fmt.Errorf("the issuer does not serve this call (it is older than this sluisctl); add --direct to use your own AWS credentials")
	}
	return fmt.Errorf("the issuer: %s", coded.Message())
}

var errNoBackupModule = errors.New("the issuer has no backup module connected; add --direct to use your own AWS credentials")

func (i *issuerBackup) run(ctx context.Context, resume bool) (job.Result, error) {
	res, err := i.client.RunBackup(ctx, connect.NewRequest(&sluisv1.RunBackupRequest{Resume: resume}))
	if err != nil {
		return job.Result{}, issuerError(err)
	}
	if !res.Msg.GetAvailable() {
		return job.Result{}, errNoBackupModule
	}
	return job.Result{Outcome: res.Msg.GetOutcome(), ID: res.Msg.GetId(), Run: viewOf(res.Msg.GetRun())}, nil
}

// retentionCounts is the last prune as the issuer reports it: how many, not which.
type retentionCounts struct {
	removed, cleaned, refused int
}

func (i *issuerBackup) status(ctx context.Context) (job.Status, *retentionCounts, error) {
	res, err := i.client.GetBackupStatus(ctx, connect.NewRequest(&sluisv1.GetBackupStatusRequest{}))
	if err != nil {
		return job.Status{}, nil, issuerError(err)
	}
	m := res.Msg
	if !m.GetAvailable() {
		return job.Status{}, nil, errNoBackupModule
	}
	st := job.Status{Latest: viewOf(m.GetLatest()), LastCompleted: viewOf(m.GetLastCompleted()), Unfinished: viewOf(m.GetUnfinished())}
	var counts *retentionCounts
	if r := m.GetRetention(); r != nil {
		st.Retention = &job.Pass{At: r.GetAt().AsTime(), Keep: int(r.GetKeep()), MaxAge: r.GetMaxAge(), Kept: int(r.GetKept())}
		counts = &retentionCounts{removed: int(r.GetRemoved()), cleaned: int(r.GetCleaned()), refused: int(r.GetRefused())}
	}
	return st, counts, nil
}

func (i *issuerBackup) list(ctx context.Context, limit int) ([]job.Info, error) {
	res, err := i.client.ListBackups(ctx, connect.NewRequest(&sluisv1.ListBackupsRequest{Limit: int32(limit)}))
	if err != nil {
		return nil, issuerError(err)
	}
	if !res.Msg.GetAvailable() {
		return nil, errNoBackupModule
	}
	out := make([]job.Info, 0, len(res.Msg.GetBackups()))
	for _, b := range res.Msg.GetBackups() {
		out = append(out, job.Info{ID: b.GetId(), Created: stampOf(b.GetCreated()), Creator: b.GetCreator(), Format: int(b.GetFormat()),
			Layout: b.GetLayout(), Modules: int(b.GetModules()), Records: b.GetRecords(), Chunks: int(b.GetChunks()), Error: b.GetError()})
	}
	return out, nil
}

func (i *issuerBackup) restoreStatus(ctx context.Context) (restorejob.Status, error) {
	res, err := i.client.GetRestoreStatus(ctx, connect.NewRequest(&sluisv1.GetRestoreStatusRequest{}))
	if err != nil {
		return restorejob.Status{}, issuerError(err)
	}
	m := res.Msg
	if !m.GetAvailable() {
		return restorejob.Status{}, errors.New("the issuer has no restore function connected; add --direct to use your own AWS credentials")
	}
	st := restorejob.Status{Latest: restoreRunOf(m.GetLatest()), Unfinished: restoreRunOf(m.GetUnfinished()), LastCompleted: restoreRunOf(m.GetLastCompleted())}
	for _, f := range m.GetMaintenance() {
		st.Maintenance = append(st.Maintenance, restorejob.ModuleFlag{Module: port.Module(f.GetModule()), State: f.GetState(),
			Since: stampOf(f.GetSince()), By: f.GetBy(), Unreadable: f.GetUnreadable()})
	}
	return st, nil
}

func (i *issuerBackup) preview(ctx context.Context, id string) (restoreAnswer, error) {
	res, err := i.client.GetRestorePreview(ctx, connect.NewRequest(&sluisv1.GetRestorePreviewRequest{BackupId: id}))
	if err != nil {
		return restoreAnswer{}, issuerError(err)
	}
	m := res.Msg
	if !m.GetAvailable() {
		return restoreAnswer{}, errors.New("the issuer has no restore function connected; add --direct to use your own AWS credentials")
	}
	ans := restoreAnswer{Outcome: m.GetOutcome(), Backup: m.GetBackupId(), Error: m.GetError()}
	if m.GetOutcome() == "preview" || len(m.GetSections()) > 0 {
		rep := &restore.Report{Installation: m.GetInstallation(), ID: m.GetBackupId(), Layout: backup.Layout(m.GetLayout()), Created: m.GetCreated()}
		for _, s := range m.GetSections() {
			rep.Sections = append(rep.Sections, restore.SectionReport{Module: port.Module(s.GetModule()), Section: backup.Section(s.GetSection()),
				Create: int(s.GetCreate()), Overwrite: int(s.GetOverwrite()), Same: int(s.GetSame()), Expired: int(s.GetExpired()), Regenerated: int(s.GetRegenerated())})
		}
		ans.Report = rep
	}
	return ans, nil
}

func stampOf(t *timestamppb.Timestamp) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.AsTime()
}

func viewOf(r *sluisv1.BackupRun) *job.View {
	if r == nil {
		return nil
	}
	return &job.View{ID: r.GetId(), State: r.GetState(), Trigger: r.GetTrigger(), Creator: r.GetCreator(),
		Started: stampOf(r.GetStarted()), Updated: stampOf(r.GetUpdated()), Finished: stampOf(r.GetFinished()),
		Modules: int(r.GetModules()), Records: r.GetRecords(), Chunks: int(r.GetChunks()), Resumed: r.GetResumed(),
		Reason: r.GetReason(), Error: r.GetError(), Units: int(r.GetUnits()), Done: int(r.GetDone())}
}

func restoreRunOf(r *sluisv1.RestoreRun) *restorejob.Run {
	if r == nil {
		return nil
	}
	return &restorejob.Run{ID: r.GetId(), BackupID: r.GetBackupId(), State: r.GetState(), Overwrite: r.GetOverwrite(), By: r.GetBy(),
		Note: r.GetNote(), Started: stampOf(r.GetStarted()), Updated: stampOf(r.GetUpdated()), Finished: stampOf(r.GetFinished()),
		Maintenance: r.GetMaintenance(), Slices: int(r.GetSlices()), Create: int(r.GetCreate()), Overwritten: int(r.GetOverwritten()),
		Same: int(r.GetSame()), Reason: r.GetReason(), Error: r.GetError()}
}
