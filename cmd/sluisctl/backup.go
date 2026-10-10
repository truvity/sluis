package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/backup/restore"
	"github.com/truvity/sluis/internal/backup/restorejob"
	"github.com/truvity/sluis/internal/backup/rpc"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/modcall/lambdacall"
)

// `sluisctl backup` and `sluisctl restore` are what an administrator runs
// against the two functions (docs/reference/sluis/backup.md). They do not go
// through the issuer or the console: the caller's own AWS credentials invoke the
// function, so IAM, and the resource policy on the function, decide who may. The
// credentials are the SDK's default chain, which is what the profiles of
// `sluisctl aws-config` feed. A restore is never a module call; it is an
// invocation of the restore function, and the console has no way to make one.

const (
	envBackupFunction  = "SLUISCTL_BACKUP_FUNCTION"
	envRestoreFunction = "SLUISCTL_RESTORE_FUNCTION"
	envInstance        = "SLUISCTL_INSTANCE"

	classAdmin      = rpc.CallerAdmin
	classBreakglass = rpc.CallerBreakglass

	// A backup run is one synchronous invocation; Lambda stops it at 15 minutes.
	backupRunTimeout = 16 * time.Minute
	callTimeout      = 2 * time.Minute
)

// BackupConfig is the `backup:` key of config.yaml: where the two functions are.
// None of it is a secret.
type BackupConfig struct {
	// Function is the backup function's name or ARN.
	Function string `yaml:"function,omitempty"`
	// RestoreFunction is the restore function's name or ARN.
	RestoreFunction string `yaml:"restoreFunction,omitempty"`
	// Instance is the installation's name, which `restore start --confirm` must match.
	Instance string `yaml:"instance,omitempty"`
	// Region is the functions' region, when the AWS configuration names none.
	Region string `yaml:"region,omitempty"`
}

// The seams of a test: the AWS clients, the clock and the pause between polls.
var (
	newBackupClients = realBackupClients
	pollSleep        = sleepContext
	pollInterval     = 5 * time.Second
	// discoverWindow is how long `restore start` looks for the run it started.
	discoverWindow = time.Minute
	discoverEvery  = 2 * time.Second
	waitLimit      = 6 * time.Hour
	nowFunc        = time.Now
)

// backupClients is what the commands use of AWS.
type backupClients struct {
	Lambda lambdacall.API
	STS    interface {
		GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, opts ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
	}
}

func realBackupClients(ctx context.Context, profile, region string) (backupClients, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return backupClients{}, fmt.Errorf("load the AWS configuration: %w", err)
	}
	return backupClients{Lambda: awslambda.NewFromConfig(cfg), STS: sts.NewFromConfig(cfg)}, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// target is what the flags, the environment and config.yaml settle: which
// function, as which caller class.
type target struct {
	function, restoreFunction, instance, class string
	asJSON                                     bool
	clients                                    backupClients
}

// targetFlags are the flags every backup and restore command takes.
type targetFlags struct {
	function, restoreFunction, instance, as, profile, region string
	asJSON                                                   bool
}

func (t *targetFlags) register(flags *flag.FlagSet, restoreCommand bool) {
	if restoreCommand {
		flags.StringVar(&t.restoreFunction, "function", "", "the restore function's name or ARN (else $"+envRestoreFunction+", else config.yaml)")
	} else {
		flags.StringVar(&t.function, "function", "", "the backup function's name or ARN (else $"+envBackupFunction+", else config.yaml)")
	}
	flags.StringVar(&t.as, "as", classAdmin, "the caller class, admin or breakglass: the function's live-<class> alias")
	flags.StringVar(&t.profile, "profile", "", "the AWS profile (else the SDK's default chain)")
	flags.StringVar(&t.region, "region", "", "the AWS region (else the configuration's, else backup.region in config.yaml)")
	flags.BoolVar(&t.asJSON, "json", false, "print JSON")
}

// resolve fills what the flags left from the environment and config.yaml, and
// opens the AWS clients.
func (t *targetFlags) resolve(ctx context.Context, restoreCommand bool) (target, error) {
	if t.as != classAdmin && t.as != classBreakglass {
		return target{}, badUsage("--as is admin or breakglass, not %q", t.as)
	}
	file, err := readConfigFile()
	if err != nil {
		return target{}, err
	}
	out := target{class: t.as, asJSON: t.asJSON}
	if restoreCommand {
		out.restoreFunction = firstNonEmpty(t.restoreFunction, settingEnv(envRestoreFunction), file.Backup.RestoreFunction)
		if out.restoreFunction == "" {
			return target{}, badUsage("no restore function: pass --function, set $%s, or set backup.restoreFunction in %s", envRestoreFunction, configFileHint())
		}
		out.instance = firstNonEmpty(t.instance, settingEnv(envInstance), file.Backup.Instance)
	} else {
		out.function = firstNonEmpty(t.function, settingEnv(envBackupFunction), file.Backup.Function)
		if out.function == "" {
			return target{}, badUsage("no backup function: pass --function, set $%s, or set backup.function in %s", envBackupFunction, configFileHint())
		}
	}
	out.clients, err = newBackupClients(ctx, t.profile, firstNonEmpty(t.region, file.Backup.Region))
	if err != nil {
		return target{}, err
	}
	return out, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// caller is the module-call transport for the chosen function and class.
func (t target) caller(module string) *lambdacall.Caller {
	fn := t.function
	if module == rpc.RestoreModule {
		fn = t.restoreFunction
	}
	return &lambdacall.Caller{API: t.clients.Lambda, Functions: map[string]string{module: fn}, Class: t.class}
}

// explain turns a refusal by IAM or the resource policy into the exit code
// that says "not granted"; anything else is returned as it was.
func explain(err error) error {
	if err == nil {
		return nil
	}
	var api smithy.APIError
	if errors.As(err, &api) && (api.ErrorCode() == "AccessDeniedException" || api.ErrorCode() == "AccessDenied") {
		return fmt.Errorf("%w: AWS refused the call (%s); the caller's role must be allowed lambda:InvokeFunction on the function's live-<class> alias",
			errNotGranted, api.ErrorCode())
	}
	var coded *modcall.Error
	if errors.As(err, &coded) && coded.Code == modcall.CodeForbidden {
		return fmt.Errorf("%w: the function refused this caller class: %s", errNotGranted, coded.Message)
	}
	return err
}

func printJSON(v any) error {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func say(format string, args ...any) { _, _ = fmt.Fprintf(stdout, format+"\n", args...) }

func newFlags(name string, usage string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(os.Stderr, usage) }
	return flags
}

// parseInterleaved parses flags in any position among the positional words.
func parseInterleaved(flags *flag.FlagSet, args []string) ([]string, error) {
	var rest []string
	for {
		if err := flags.Parse(args); err != nil {
			return nil, usageError{err}
		}
		if flags.NArg() == 0 {
			return rest, nil
		}
		rest = append(rest, flags.Arg(0))
		args = flags.Args()[1:]
	}
}

const backupUsage = `Usage: sluisctl backup run [--resume] | status | list [--limit N]

Calls the backup function with your own AWS credentials (the SDK's default
chain). Flags for every subcommand:
  --function  the backup function's name or ARN (else $SLUISCTL_BACKUP_FUNCTION,
              else backup.function in config.yaml)
  --as        admin (default) or breakglass: the function's live-<class> alias
  --profile, --region   the AWS profile and region
  --json      print JSON
`

func backupCommand(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		_, _ = fmt.Fprint(os.Stderr, backupUsage)
		return badUsage("backup needs run, status or list")
	}
	switch args[0] {
	case "run":
		return backupRun(args[1:])
	case "status":
		return backupStatus(args[1:])
	case "list":
		return backupList(args[1:])
	default:
		return badUsage("backup %q is not run, status or list", args[0])
	}
}

func backupRun(args []string) error {
	var tf targetFlags
	flags := newFlags("backup run", backupUsage)
	tf.register(flags, false)
	resume := flags.Bool("resume", false, "only continue a paused run; start none")
	rest, err := parseInterleaved(flags, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return badUsage("backup run takes no argument, not %q", rest[0])
	}
	ctx, cancel := context.WithTimeout(context.Background(), backupRunTimeout)
	defer cancel()
	t, err := tf.resolve(ctx, false)
	if err != nil {
		return err
	}
	res, err := modcall.Do[rpc.RunRequest, job.Result](ctx, t.caller(rpc.Module), rpc.Module, rpc.MethodRun, rpc.RunRequest{Resume: *resume})
	if err != nil {
		return explain(err)
	}
	if t.asJSON {
		return printJSON(res)
	}
	say("backup: %s", res.Outcome)
	if res.Run != nil {
		printBackupRun("run", *res.Run)
	} else if res.ID != "" {
		say("run:    %s", res.ID)
	}
	return nil
}

func backupStatus(args []string) error {
	var tf targetFlags
	flags := newFlags("backup status", backupUsage)
	tf.register(flags, false)
	rest, err := parseInterleaved(flags, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return badUsage("backup status takes no argument, not %q", rest[0])
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	t, err := tf.resolve(ctx, false)
	if err != nil {
		return err
	}
	st, err := modcall.Do[rpc.StatusRequest, job.Status](ctx, t.caller(rpc.Module), rpc.Module, rpc.MethodStatus, rpc.StatusRequest{})
	if err != nil {
		return explain(err)
	}
	if t.asJSON {
		return printJSON(st)
	}
	if st.Latest == nil {
		say("no backup has run yet")
	}
	for _, row := range []struct {
		label string
		view  *job.View
	}{{"latest", st.Latest}, {"unfinished", st.Unfinished}, {"last completed", st.LastCompleted}} {
		if row.view != nil {
			printBackupRun(row.label, *row.view)
		}
	}
	if p := st.Retention; p != nil {
		say("last prune: %s, kept %d, removed %d, orphans %d, refused %d (keep %d, max age %s)",
			p.At.Format(time.RFC3339), p.Kept, len(p.Removed), len(p.Orphans), len(p.Failed), p.Keep, p.MaxAge)
	}
	return nil
}

func printBackupRun(label string, v job.View) {
	line := fmt.Sprintf("%-15s %s  %s", label+":", v.ID, v.State)
	if v.Units > 0 {
		line += fmt.Sprintf(" (%d of %d units)", v.Done, v.Units)
	}
	if v.State == job.StateCompleted {
		line += fmt.Sprintf(", %d modules, %d records, %d chunks", v.Modules, v.Records, v.Chunks)
	}
	say("%s", line)
	if v.Reason != "" || v.Error != "" {
		say("%-15s %s %s", "", v.Reason, v.Error)
	}
}

func backupList(args []string) error {
	var tf targetFlags
	flags := newFlags("backup list", backupUsage)
	tf.register(flags, false)
	limit := flags.Int("limit", 0, "the newest N backups (default all)")
	rest, err := parseInterleaved(flags, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 || *limit < 0 {
		return badUsage("backup list takes no argument and a --limit of 0 or more")
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	t, err := tf.resolve(ctx, false)
	if err != nil {
		return err
	}
	list, err := modcall.Do[rpc.ListRequest, rpc.ListResponse](ctx, t.caller(rpc.Module), rpc.Module, rpc.MethodList, rpc.ListRequest{Limit: *limit})
	if err != nil {
		return explain(err)
	}
	if t.asJSON {
		return printJSON(list)
	}
	if len(list.Backups) == 0 {
		say("the archive holds no backup")
		return nil
	}
	for _, b := range list.Backups {
		line := fmt.Sprintf("%s  %s  %d modules  %d records  layout %s", b.ID, b.Created.Format(time.RFC3339), b.Modules, b.Records, b.Layout)
		if b.Error != "" {
			line += "  (manifest unreadable: " + b.Error + ")"
		}
		say("%s", line)
	}
	return nil
}

const restoreUsage = `Usage: sluisctl restore preview <backup-id>
       sluisctl restore start <backup-id> --confirm <instance> [--overwrite] [--note TEXT] [--wait]
       sluisctl restore resume [--wait]
       sluisctl restore status

Invokes the restore function with your own AWS credentials; the function's
resource policy names who may. Flags for every subcommand:
  --function  the restore function's name or ARN (else $SLUISCTL_RESTORE_FUNCTION,
              else backup.restoreFunction in config.yaml)
  --as        admin (default) or breakglass: the function's live-<class> alias
  --profile, --region   the AWS profile and region
  --json      print JSON

start also takes --instance (else $SLUISCTL_INSTANCE, else backup.instance in
config.yaml): the installation's name, which --confirm must equal. A restore
sets every module under maintenance until it completes.
`

func restoreCommand(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		_, _ = fmt.Fprint(os.Stderr, restoreUsage)
		return badUsage("restore needs preview, start, resume or status")
	}
	switch args[0] {
	case "preview":
		return restorePreview(args[1:])
	case "start":
		return restoreStart(args[1:])
	case "resume":
		return restoreResume(args[1:])
	case "status":
		return restoreStatus(args[1:])
	default:
		return badUsage("restore %q is not preview, start, resume or status", args[0])
	}
}

// restoreEvent is the {"kind":"restore"} event of the restore function.
type restoreEvent struct {
	Kind      string `json:"kind"`
	Backup    string `json:"backup,omitempty"`
	Overwrite bool   `json:"overwrite,omitempty"`
	Preview   bool   `json:"preview,omitempty"`
	Resume    bool   `json:"resume,omitempty"`
	By        string `json:"by,omitempty"`
	Note      string `json:"note,omitempty"`
}

// restoreAnswer is what a synchronous restore invocation returns.
type restoreAnswer struct {
	Outcome string          `json:"outcome"`
	Backup  string          `json:"backup,omitempty"`
	Reason  string          `json:"reason,omitempty"`
	Error   string          `json:"error,omitempty"`
	Report  *restore.Report `json:"report,omitempty"`
}

func (t target) invokeRestore(ctx context.Context, ev restoreEvent, async bool) (*awslambda.InvokeOutput, error) {
	ev.Kind = "restore"
	body, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	kind := lambdatypes.InvocationTypeRequestResponse
	if async {
		kind = lambdatypes.InvocationTypeEvent
	}
	out, err := t.clients.Lambda.Invoke(ctx, &awslambda.InvokeInput{
		FunctionName: aws.String(t.restoreFunction), Qualifier: aws.String(lambdacall.AliasOf(t.class)),
		InvocationType: kind, Payload: body,
	})
	if err != nil {
		return nil, explain(fmt.Errorf("invoke %s: %w", t.restoreFunction, err))
	}
	if out.FunctionError != nil {
		return nil, fmt.Errorf("the restore function failed: %s", aws.ToString(out.FunctionError))
	}
	return out, nil
}

func (t target) restoreStatus(ctx context.Context) (restorejob.Status, error) {
	st, err := modcall.Do[rpc.StatusRequest, restorejob.Status](ctx, t.caller(rpc.RestoreModule), rpc.RestoreModule, rpc.MethodRestoreStatus, rpc.StatusRequest{})
	return st, explain(err)
}

func restorePreview(args []string) error {
	var tf targetFlags
	flags := newFlags("restore preview", restoreUsage)
	tf.register(flags, true)
	rest, err := parseInterleaved(flags, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return badUsage("restore preview needs one backup id (see `sluisctl backup list`)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), backupRunTimeout)
	defer cancel()
	t, err := tf.resolve(ctx, true)
	if err != nil {
		return err
	}
	out, err := t.invokeRestore(ctx, restoreEvent{Backup: rest[0], Preview: true}, false)
	if err != nil {
		return err
	}
	var ans restoreAnswer
	if err = json.Unmarshal(out.Payload, &ans); err != nil {
		return fmt.Errorf("the restore function's answer is not valid: %w", err)
	}
	if t.asJSON {
		return printJSON(ans)
	}
	if ans.Report == nil {
		say("preview: %s %s %s", ans.Outcome, ans.Reason, ans.Error)
		return nil
	}
	say("preview of backup %s (%s, layout %s): nothing was written", ans.Report.ID, ans.Report.Installation, ans.Report.Layout)
	for _, s := range ans.Report.Sections {
		say("  %-12s %-10s create %d  overwrite %d  same %d  expired %d  regenerated %d",
			s.Module, s.Section, s.Create, s.Overwrite, s.Same, s.Expired, s.Regenerated)
	}
	tot := ans.Report.Totals()
	say("total: create %d, overwrite %d, same %d, expired %d, regenerated %d", tot.Create, tot.Overwrite, tot.Same, tot.Expired, tot.Regenerated)
	if tot.Overwrite > 0 {
		say("restore start needs --overwrite to replace the %d records that differ", tot.Overwrite)
	}
	return nil
}

func restoreStart(args []string) error {
	var tf targetFlags
	flags := newFlags("restore start", restoreUsage)
	tf.register(flags, true)
	flags.StringVar(&tf.instance, "instance", "", "the installation's name (else $"+envInstance+", else backup.instance in config.yaml)")
	confirm := flags.String("confirm", "", "the installation's name, typed")
	overwrite := flags.Bool("overwrite", false, "replace records the destination holds with a different value")
	note := flags.String("note", "", "why, for the status record")
	wait := flags.Bool("wait", false, "poll until the restore completes or fails")
	rest, err := parseInterleaved(flags, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return badUsage("restore start needs one backup id (see `sluisctl backup list`)")
	}
	id := rest[0]
	if strings.TrimSpace(*confirm) == "" {
		return badUsage("restore start overwrites the installation's state and puts every module under maintenance: " +
			"type the installation's name with --confirm <instance>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	t, err := tf.resolve(ctx, true)
	if err != nil {
		return err
	}
	if t.instance == "" {
		return badUsage("the installation's name is not known: pass --instance, set $%s, or set backup.instance in %s", envInstance, configFileHint())
	}
	if *confirm != t.instance {
		return badUsage("--confirm %q is not the installation's name; nothing was started", *confirm)
	}
	who, err := callerIdentity(ctx, t.clients)
	if err != nil {
		return err
	}

	// The status before the start tells the run this start made from one that
	// was already there. A restore that cannot report must still be startable,
	// so a failed read is not an error.
	var before *restorejob.Run
	if st, statusErr := t.restoreStatus(ctx); statusErr == nil {
		before = st.Latest
	}
	began := nowFunc()
	out, err := t.invokeRestore(ctx, restoreEvent{Backup: id, Overwrite: *overwrite, By: who, Note: *note}, true)
	if err != nil {
		return err
	}
	if out.StatusCode != 202 {
		return fmt.Errorf("the restore function was not started: status %d", out.StatusCode)
	}
	run := t.discoverRun(ctx, id, before, began)
	result := startResult{Requested: true, Backup: id, By: who, Overwrite: *overwrite}
	if run != nil {
		result.Run, result.State = run.ID, run.State
	}
	if !t.asJSON {
		say("restore of backup %s requested as %s", id, who)
		if run != nil {
			say("run id: %s (%s)", run.ID, run.State)
		} else {
			say("no run is visible yet; the function may have refused it. See `sluisctl restore status`")
		}
	}
	if *wait {
		if run == nil {
			if t.asJSON {
				_ = printJSON(result)
			}
			return errors.New("no run appeared to wait for: see `sluisctl restore status`")
		}
		final, waitErr := t.waitRestore(ctx, run.ID)
		if final != nil {
			result.State, result.Reason, result.Error = final.State, final.Reason, final.Error
		}
		if t.asJSON {
			_ = printJSON(result)
		}
		return waitErr
	}
	if t.asJSON {
		return printJSON(result)
	}
	return nil
}

// startResult is `restore start --json`.
type startResult struct {
	Requested bool   `json:"requested"`
	Backup    string `json:"backup"`
	Run       string `json:"run,omitempty"`
	State     string `json:"state,omitempty"`
	By        string `json:"by"`
	Overwrite bool   `json:"overwrite,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Error     string `json:"error,omitempty"`
}

// callerIdentity is the caller's STS ARN, which `by` declares. The function
// cannot authenticate it; IAM already decided who may invoke.
func callerIdentity(ctx context.Context, c backupClients) (string, error) {
	out, err := c.STS.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("who am I (sts:GetCallerIdentity): %w", err)
	}
	return aws.ToString(out.Arn), nil
}

// discoverRun finds the run an asynchronous start made: the run of the backup
// that is new, or that was written since the start. The invocation returns
// before the function has written its record.
func (t target) discoverRun(ctx context.Context, backup string, before *restorejob.Run, began time.Time) *restorejob.Run {
	deadline := nowFunc().Add(discoverWindow)
	for {
		if st, err := t.restoreStatus(ctx); err == nil {
			for _, r := range []*restorejob.Run{st.Unfinished, st.Latest} {
				if r != nil && r.BackupID == backup && isNewRun(r, before, began) {
					return r
				}
			}
		}
		if !nowFunc().Before(deadline) || pollSleep(ctx, discoverEvery) != nil {
			return nil
		}
	}
}

// isNewRun is whether r was made or touched by a start at began. The clocks of
// the laptop and the function differ, hence the minute.
func isNewRun(r, before *restorejob.Run, began time.Time) bool {
	touched := r.Updated.After(began.Add(-time.Minute))
	if before == nil {
		return touched
	}
	return r.ID != before.ID || touched
}

// waitRestore polls restore.status until the run completed or failed. A paused
// run carries on by itself, so it is only reported.
func (t target) waitRestore(ctx context.Context, id string) (*restorejob.Run, error) {
	last := ""
	for {
		st, err := t.restoreStatus(ctx)
		if err == nil {
			if r := findRun(st, id); r != nil {
				if state := r.State + "/" + r.Maintenance; state != last && !t.asJSON {
					say("restore %s: %s", id, r.State)
					last = state
				}
				switch r.State {
				case restorejob.StateCompleted:
					return r, nil
				case restorejob.StateFailed:
					return r, fmt.Errorf("the restore failed (%s): %s; every module stays under maintenance, run `sluisctl restore start` again to retry", r.Reason, r.Error)
				}
			}
		}
		if err := pollSleep(ctx, pollInterval); err != nil {
			return nil, fmt.Errorf("stopped waiting for restore %s: %w; it carries on in the function", id, err)
		}
	}
}

func findRun(st restorejob.Status, id string) *restorejob.Run {
	for _, r := range []*restorejob.Run{st.Latest, st.Unfinished, st.LastCompleted} {
		if r != nil && r.ID == id {
			return r
		}
	}
	return nil
}

func restoreResume(args []string) error {
	var tf targetFlags
	flags := newFlags("restore resume", restoreUsage)
	tf.register(flags, true)
	wait := flags.Bool("wait", false, "poll until the restore completes or fails")
	rest, err := parseInterleaved(flags, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return badUsage("restore resume takes no argument, not %q", rest[0])
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	t, err := tf.resolve(ctx, true)
	if err != nil {
		return err
	}
	st, err := t.restoreStatus(ctx)
	if err != nil {
		return err
	}
	if st.Unfinished == nil {
		if t.asJSON {
			return printJSON(startResult{Requested: false})
		}
		say("no restore is unfinished; nothing to resume")
		return nil
	}
	run := st.Unfinished
	out, err := t.invokeRestore(ctx, restoreEvent{Resume: true}, true)
	if err != nil {
		return err
	}
	if out.StatusCode != 202 {
		return fmt.Errorf("the restore function was not started: status %d", out.StatusCode)
	}
	result := startResult{Requested: true, Backup: run.BackupID, Run: run.ID, State: run.State}
	if !t.asJSON {
		say("resume of restore %s requested", run.ID)
	}
	if *wait {
		final, waitErr := t.waitRestore(ctx, run.ID)
		if final != nil {
			result.State, result.Reason, result.Error = final.State, final.Reason, final.Error
		}
		if t.asJSON {
			_ = printJSON(result)
		}
		return waitErr
	}
	if t.asJSON {
		return printJSON(result)
	}
	return nil
}

func restoreStatus(args []string) error {
	var tf targetFlags
	flags := newFlags("restore status", restoreUsage)
	tf.register(flags, true)
	rest, err := parseInterleaved(flags, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return badUsage("restore status takes no argument, not %q", rest[0])
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	t, err := tf.resolve(ctx, true)
	if err != nil {
		return err
	}
	st, err := t.restoreStatus(ctx)
	if err != nil {
		return err
	}
	if t.asJSON {
		return printJSON(st)
	}
	if st.Latest == nil {
		say("no restore has run")
	}
	for _, row := range []struct {
		label string
		run   *restorejob.Run
	}{{"latest", st.Latest}, {"unfinished", st.Unfinished}, {"last completed", st.LastCompleted}} {
		if r := row.run; r != nil {
			say("%-15s %s  %s  backup %s  by %s", row.label+":", r.ID, r.State, r.BackupID, r.By)
			if r.Reason != "" || r.Error != "" {
				say("%-15s %s %s", "", r.Reason, r.Error)
			}
		}
	}
	for _, m := range st.Maintenance {
		say("maintenance: %s %s since %s", m.Module, m.State, m.Since.Format(time.RFC3339))
	}
	return nil
}
