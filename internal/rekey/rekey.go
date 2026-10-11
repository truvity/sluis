// Package rekey is `sluis secrets rekey`: it re-writes the SecureString
// parameters below an installation's secrets root under the KMS key the
// document names (`secrets.kmsKeyId`), so that every parameter of the
// installation is encrypted with one estate key.
//
// What a re-key does to a parameter, and what it does not:
//
//   - It reads the parameter with decryption and writes the same value back with
//     PutParameter, Overwrite and KeyId set. The name, the type, the tier and the
//     description stay. SSM makes the write a new version (N+1) whose value is
//     the value of N, encrypted under the new key.
//   - History is not re-keyed. Versions 1..N keep the key they were written
//     under (usually the AWS-managed alias/aws/ssm), for as long as SSM keeps
//     them (the last 100). Whoever may call ssm:GetParameterHistory with
//     decryption can still read them. A secret that must not survive on the old
//     key is rotated, not re-keyed.
//   - A reader that holds a version as its revision (the store's conditional
//     Put) finds it superseded and reads again. The previous revision of a
//     parameter is now the same value, so a client secret rotated within
//     secrets.grace would lose its overlap: a parameter modified within the
//     grace window is deferred, not re-written (see [Options.Grace]).
//   - It is not atomic against a writer. A parameter read at version N and
//     found at another version just before the write is skipped and reported;
//     one that changed between that check and the write is reported as a
//     concurrent write, with both versions named. Run it while nothing writes
//     secrets (the issuer writes them only on a rotation or an export).
//
// Nothing printed, logged or recorded is a value: names, versions, counts.
package rekey

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
)

// API is the part of the SSM client a re-key uses; *ssm.Client satisfies it.
type API interface {
	DescribeParameters(ctx context.Context, in *awsssm.DescribeParametersInput, opts ...func(*awsssm.Options)) (*awsssm.DescribeParametersOutput, error)
	GetParameters(ctx context.Context, in *awsssm.GetParametersInput, opts ...func(*awsssm.Options)) (*awsssm.GetParametersOutput, error)
	PutParameter(ctx context.Context, in *awsssm.PutParameterInput, opts ...func(*awsssm.Options)) (*awsssm.PutParameterOutput, error)
}

const (
	// BatchSize is how many parameters one read asks for, and one pacing step
	// covers: SSM's GetParameters limit.
	BatchSize = 10
	// DefaultPause is the wait between two batches.
	DefaultPause = 500 * time.Millisecond
	// DefaultBackoff is the first wait after a throttled request; it doubles.
	DefaultBackoff = time.Second
	maxBackoff     = 30 * time.Second
	maxRetries     = 8
	// DefaultGrace is secrets.grace when the document sets none.
	DefaultGrace = 24 * time.Hour
)

// Options is how a re-key behaves.
type Options struct {
	// Root is the secrets root, for example /sluis/example. Every SecureString
	// parameter below it is considered.
	Root string
	// KeyID is the key every parameter should be under: an alias
	// (alias/...), a key id or an ARN, as the document names it.
	KeyID string
	// DryRun reads and reports; nothing is written and Before is not called.
	DryRun bool
	// Grace defers a parameter modified within it (zero is [DefaultGrace]); IncludeRecent re-writes it anyway.
	Grace         time.Duration
	IncludeRecent bool
	// Pause is the wait between two batches, Backoff the first wait after a
	// throttled request (zero are the defaults).
	Pause, Backoff time.Duration
	// Sleep waits; nil is a context-aware wait. Now is the clock; nil is time.Now.
	Sleep func(ctx context.Context, d time.Duration) error
	Now   func() time.Time
	// Before is called once, after the plan is known and before the first
	// write, when there is something to write. The command records its intent
	// there; an error stops the run with nothing written.
	Before func(ctx context.Context, r *Report) error
}

// Report is what a re-key found and did: names, versions and counts.
type Report struct {
	Root   string `json:"root"`
	KeyID  string `json:"keyId"`
	DryRun bool   `json:"dryRun,omitempty"`
	// Considered are the SecureString parameters below the root; Current are
	// those already under the key.
	Considered int `json:"considered"`
	Current    int `json:"current"`
	// Rekey are the parameters to re-write (a dry run) or the ones selected.
	Rekey []string `json:"rekey"`
	// Deferred were modified within the grace window and are left for a later run.
	Deferred []string `json:"deferred,omitempty"`
	// Rekeyed counts the parameters written, in Batches batches.
	Rekeyed int `json:"rekeyed,omitempty"`
	Batches int `json:"batches,omitempty"`
	// Skipped are parameters that changed under the run: left as they are, to
	// be run again.
	Skipped []string `json:"skipped,omitempty"`
	// Concurrent are parameters a writer changed between the check and the
	// write: the run's write is the current version and the writer's is its
	// predecessor in the history. The operator reads it from there.
	Concurrent []string `json:"concurrent,omitempty"`
	Notes      []string `json:"notes,omitempty"`
	OK         bool     `json:"ok"`
	Error      string   `json:"error,omitempty"`
}

// ErrNotStarted marks a run stopped by its Before hook: nothing was written.
var ErrNotStarted = errors.New("rekey: nothing was written")

type param struct {
	name     string
	version  int64
	tier     types.ParameterTier
	desc     string
	modified time.Time
}

// SameKey reports whether the key a parameter reports (DescribeParameters
// returns it as it was given to PutParameter: an alias, a key id or an ARN) is
// the configured one. An alias ARN is its alias.
func SameKey(reported, want string) bool {
	trim := func(k string) string {
		if i := strings.Index(k, ":alias/"); strings.HasPrefix(k, "arn:") && i >= 0 {
			return k[i+1:]
		}
		return k
	}
	return reported != "" && trim(reported) == trim(want)
}

// Run lists the SecureString parameters below the root, selects the ones not
// under the key, and re-writes them, ten at a time, paced. The report is
// returned with every error that is not a failure to start.
func Run(ctx context.Context, api API, opt Options) (*Report, error) {
	root := "/" + strings.Trim(opt.Root, "/")
	if root == "/" || opt.KeyID == "" {
		return nil, errors.New("rekey: a secrets root and a key are required")
	}
	if strings.HasPrefix(opt.KeyID, "alias/aws/") {
		return nil, fmt.Errorf("rekey: %q is the AWS-managed key: name the estate's key", opt.KeyID)
	}
	sleep, now := opt.Sleep, opt.Now
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-time.After(d):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if now == nil {
		now = time.Now
	}
	pause, backoff, grace := opt.Pause, opt.Backoff, opt.Grace
	if pause == 0 {
		pause = DefaultPause
	}
	if backoff == 0 {
		backoff = DefaultBackoff
	}
	if grace == 0 {
		grace = DefaultGrace
	}

	out := &Report{Root: root, KeyID: opt.KeyID, DryRun: opt.DryRun, Rekey: []string{}}
	fail := func(err error) (*Report, error) {
		out.Error = err.Error()
		return out, err
	}
	var todo []param
	var next *string
	for {
		var page *awsssm.DescribeParametersOutput
		err := withBackoff(ctx, backoff, sleep, func() (err error) {
			page, err = api.DescribeParameters(ctx, &awsssm.DescribeParametersInput{
				ParameterFilters: []types.ParameterStringFilter{{Key: aws.String("Path"), Option: aws.String("Recursive"), Values: []string{root}}},
				NextToken:        next,
				MaxResults:       aws.Int32(50),
			})
			return err
		})
		if err != nil {
			return fail(fmt.Errorf("rekey: list %s: %w", root, err))
		}
		for i := range page.Parameters {
			m := &page.Parameters[i]
			if m.Type != types.ParameterTypeSecureString {
				continue
			}
			out.Considered++
			if SameKey(aws.ToString(m.KeyId), opt.KeyID) {
				out.Current++
				continue
			}
			p := param{name: aws.ToString(m.Name), version: m.Version, tier: m.Tier, desc: aws.ToString(m.Description)}
			if m.LastModifiedDate != nil {
				p.modified = *m.LastModifiedDate
			}
			if !opt.IncludeRecent && !p.modified.IsZero() && now().Sub(p.modified) < grace {
				out.Deferred = append(out.Deferred, p.name)
				continue
			}
			todo = append(todo, p)
		}
		if next = page.NextToken; next == nil {
			break
		}
	}
	slices.SortFunc(todo, func(a, b param) int { return strings.Compare(a.name, b.name) })
	slices.Sort(out.Deferred)
	for _, p := range todo {
		out.Rekey = append(out.Rekey, p.name)
	}
	if len(out.Deferred) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"%d parameters were modified within the last %s (secrets.grace) and are left: re-writing one would make its previous version the same value, "+
				"ending a rotated client secret's overlap. Run again after the window, or with --include-recent", len(out.Deferred), grace))
	}
	out.Notes = append(out.Notes, "history is not re-keyed: earlier versions keep the key they were written under")
	if opt.DryRun || len(todo) == 0 {
		out.OK = true
		return out, nil
	}
	if opt.Before != nil {
		if err := opt.Before(ctx, out); err != nil {
			return fail(fmt.Errorf("%w: %w", ErrNotStarted, err))
		}
	}
	for chunk := range slices.Chunk(todo, BatchSize) {
		if err := rekeyBatch(ctx, api, opt.KeyID, chunk, out, backoff, sleep); err != nil {
			return fail(fmt.Errorf("%w (re-keyed so far: %d; run again, it continues with what is left)", err, out.Rekeyed))
		}
		out.Batches++
		if err := sleep(ctx, pause); err != nil {
			return fail(err)
		}
	}
	if len(out.Skipped) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d parameters changed after they were listed and were left: run again", len(out.Skipped)))
	}
	out.OK = len(out.Concurrent) == 0
	if !out.OK {
		return fail(fmt.Errorf("rekey: %d parameters were written by someone else while the run was writing them: %s; "+
			"the run's write is the current version and theirs is the one before it in the history", len(out.Concurrent), strings.Join(out.Concurrent, ", ")))
	}
	return out, nil
}

// rekeyBatch reads up to ten parameters with decryption and writes each back
// under the key, if it is still at the version it was listed at.
func rekeyBatch(ctx context.Context, api API, keyID string, batch []param, out *Report, backoff time.Duration,
	sleep func(context.Context, time.Duration) error) error {
	names := make([]string, len(batch))
	for i, p := range batch {
		names[i] = p.name
	}
	var got *awsssm.GetParametersOutput
	if err := withBackoff(ctx, backoff, sleep, func() (err error) {
		got, err = api.GetParameters(ctx, &awsssm.GetParametersInput{Names: names, WithDecryption: aws.Bool(true)})
		return err
	}); err != nil {
		return fmt.Errorf("rekey: read %s and %d more: %w", names[0], len(names)-1, err)
	}
	read := map[string]types.Parameter{}
	for _, p := range got.Parameters {
		read[aws.ToString(p.Name)] = p
	}
	for _, p := range batch {
		cur, ok := read[p.name]
		if !ok || cur.Version != p.version {
			// Deleted or rewritten since it was listed: leave it to a next run.
			out.Skipped = append(out.Skipped, p.name)
			continue
		}
		in := &awsssm.PutParameterInput{
			Name: aws.String(p.name), Value: cur.Value, Type: types.ParameterTypeSecureString,
			Overwrite: aws.Bool(true), KeyId: aws.String(keyID),
		}
		if p.tier == types.ParameterTierAdvanced {
			in.Tier = types.ParameterTierAdvanced
		}
		if p.desc != "" {
			in.Description = aws.String(p.desc)
		}
		var put *awsssm.PutParameterOutput
		if err := withBackoff(ctx, backoff, sleep, func() (err error) {
			put, err = api.PutParameter(ctx, in)
			return err
		}); err != nil {
			return fmt.Errorf("rekey: write %s: %w", p.name, err)
		}
		out.Rekeyed++
		if put.Version != p.version+1 {
			// Somebody wrote between our read and our write.
			out.Concurrent = append(out.Concurrent, fmt.Sprintf("%s (read v%d, wrote v%d)", p.name, p.version, put.Version))
		}
	}
	return nil
}

// withBackoff runs f, waiting and trying again while SSM throttles it.
func withBackoff(ctx context.Context, first time.Duration, sleep func(context.Context, time.Duration) error, f func() error) error {
	wait := first
	for attempt := 0; ; attempt++ {
		err := f()
		if err == nil || !throttled(err) || attempt >= maxRetries {
			return err
		}
		if serr := sleep(ctx, wait); serr != nil {
			return serr
		}
		wait = min(wait*2, maxBackoff)
	}
}

func throttled(err error) bool {
	var api smithy.APIError
	if !errors.As(err, &api) {
		return false
	}
	switch api.ErrorCode() {
	case "ThrottlingException", "Throttling", "TooManyUpdates", "RequestLimitExceeded", "ThrottledException":
		return true
	}
	return false
}
