package rekey

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/smithy-go"
)

// keyStatus is where a parameter's key stands against the configured one.
type keyStatus int

const (
	keyCurrent   keyStatus = iota // the configured key
	keyDifferent                  // another key: re-write
	keyUnknown                    // KMS would not say, and the spelling does not settle it
)

type keyEntry struct {
	use    KeyUse
	status keyStatus
}

// keyResolver turns the spellings of a key that SSM reports (it returns the
// KeyId as it was given to PutParameter: an alias, an alias ARN, a key id or a
// key ARN) into key ARNs with kms:DescribeKey, once per distinct spelling, and
// compares ARNs. Without a KMS client it compares spellings.
type keyResolver struct {
	kms     KMS
	want    string
	backoff time.Duration
	sleep   func(context.Context, time.Duration) error

	wantARN string
	wantErr error
	seen    map[string]*keyEntry
	asked   map[string]described
}

type described struct {
	arn string
	err error
}

func newKeyResolver(k KMS, want string, backoff time.Duration, sleep func(context.Context, time.Duration) error) *keyResolver {
	return &keyResolver{kms: k, want: want, backoff: backoff, sleep: sleep, seen: map[string]*keyEntry{}, asked: map[string]described{}}
}

// describe asks KMS for the ARN of the key the spelling names, once per spelling.
func (r *keyResolver) describe(ctx context.Context, id string) (string, error) {
	if d, ok := r.asked[id]; ok {
		return d.arn, d.err
	}
	arn, err := r.ask(ctx, id)
	if ctx.Err() == nil {
		r.asked[id] = described{arn, err}
	}
	return arn, err
}

func (r *keyResolver) ask(ctx context.Context, id string) (string, error) {
	var arn string
	err := withBackoff(ctx, r.backoff, r.sleep, func() error {
		out, err := r.kms.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(id)})
		if err != nil {
			return err
		}
		if out.KeyMetadata == nil || aws.ToString(out.KeyMetadata.Arn) == "" {
			return errors.New("kms:DescribeKey returned no key ARN")
		}
		arn = aws.ToString(out.KeyMetadata.Arn)
		return nil
	})
	return arn, err
}

// resolveWant resolves the configured key. A key KMS does not know cannot be
// re-keyed to, so that stops the run; a refusal only costs the comparison by
// ARN (the spelling is compared instead, and what it cannot settle is unknown).
func (r *keyResolver) resolveWant(ctx context.Context) error {
	if r.kms == nil {
		return nil
	}
	r.wantARN, r.wantErr = r.describe(ctx, r.want)
	var nf *kmstypes.NotFoundException
	if errors.As(r.wantErr, &nf) {
		return fmt.Errorf("rekey: secrets.kmsKeyId %q is not a key KMS knows: %s", r.want, briefErr(r.wantErr))
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// isKeyARN is true for the ARN of a key (not of an alias).
func isKeyARN(id string) bool {
	return strings.HasPrefix(id, "arn:") && strings.Contains(id, ":key/")
}

// awsManaged is true for an AWS-managed alias (alias/aws/ssm) or its alias ARN:
// the prefix is reserved by AWS, so such a key is never the estate's key.
func awsManaged(id string) bool {
	if i := strings.Index(id, ":alias/"); strings.HasPrefix(id, "arn:") && i >= 0 {
		id = id[i+1:]
	}
	return strings.HasPrefix(id, "alias/aws/")
}

// classify says whether the key a parameter reports is the configured one.
func (r *keyResolver) classify(ctx context.Context, reported string) keyStatus {
	if r.kms == nil {
		if SameKey(reported, r.want) {
			return keyCurrent
		}
		return keyDifferent
	}
	e := r.seen[reported]
	if e == nil {
		e = &keyEntry{use: KeyUse{Reported: reported}}
		if reported != "" {
			var err error
			if e.use.ARN, err = r.describe(ctx, reported); err != nil {
				e.use.Error = briefErr(err)
			}
		}
		e.status = r.compare(reported, e.use.ARN)
		e.use.Current = e.status == keyCurrent
		r.seen[reported] = e
	}
	e.use.Count++
	return e.status
}

// compare settles one spelling given what KMS said for it (arn, empty if it
// would not) and for the configured key.
func (r *keyResolver) compare(reported, arn string) keyStatus {
	switch {
	case reported == "":
		return keyDifferent
	case arn != "" && r.wantARN != "":
		if arn == r.wantARN {
			return keyCurrent
		}
		return keyDifferent
	case SameKey(reported, r.want), r.wantARN != "" && reported == r.wantARN:
		return keyCurrent
	case arn != "" && isKeyARN(r.want):
		// The configured key is spelled as the one thing KMS reports.
		if arn == r.want {
			return keyCurrent
		}
		return keyDifferent
	case awsManaged(reported):
		return keyDifferent
	}
	return keyUnknown
}

// report puts the keys seen, and the configured key's ARN, into the report.
func (r *keyResolver) report(out *Report) {
	out.KeyARN = r.wantARN
	for _, e := range r.seen {
		out.Keys = append(out.Keys, e.use)
	}
	slices.SortFunc(out.Keys, func(a, b KeyUse) int { return strings.Compare(a.Reported, b.Reported) })
}

// notes are what the operator needs to know about how the keys were compared.
func (r *keyResolver) notes(out *Report, includeUnknown bool) []string {
	if r.kms == nil {
		return nil
	}
	var notes []string
	if r.wantErr != nil {
		notes = append(notes, fmt.Sprintf("kms:DescribeKey on %s failed (%s): keys that could not be resolved are compared by their spelling", r.want, briefErr(r.wantErr)))
	}
	var denied []string
	for _, k := range out.Keys {
		if k.Error != "" {
			denied = append(denied, fmt.Sprintf("%s (%s)", k.Reported, k.Error))
		}
	}
	if len(denied) > 0 {
		notes = append(notes, "kms:DescribeKey failed for "+strings.Join(denied, "; "))
	}
	if n := len(out.Unknown); n > 0 {
		if includeUnknown {
			notes = append(notes, fmt.Sprintf("%d parameters are under a key that could not be resolved and are re-written anyway (--include-unknown)", n))
		} else {
			notes = append(notes, fmt.Sprintf("%d parameters are under a key that could not be resolved (unknown key) and are not re-written: "+
				"grant kms:DescribeKey (RekeyOperatorStatements does), or pass --include-unknown to re-write them", n))
		}
	}
	return notes
}

// briefErr is an AWS error as its code and message, without the request trail.
func briefErr(err error) string {
	var api smithy.APIError
	if errors.As(err, &api) {
		return api.ErrorCode() + ": " + api.ErrorMessage()
	}
	return err.Error()
}
