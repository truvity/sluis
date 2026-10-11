package rekey_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"

	"github.com/truvity/sluis/internal/rekey"
)

const (
	root = "/sluis/example"
	key  = "alias/example-secrets"
)

var t0 = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

type entry struct {
	value    string
	version  int64
	keyID    string
	typ      types.ParameterType
	tier     types.ParameterTier
	desc     string
	modified time.Time
	history  []string // values of earlier versions, with their key
}

// ssm is a Parameter Store: versions, a key per version, and the pacing.
type ssm struct {
	mu       sync.Mutex
	params   map[string]*entry
	puts     []*awsssm.PutParameterInput
	reads    [][]string
	throttle int // the next n calls answer ThrottlingException
	// beforePut runs inside PutParameter, before it applies: a concurrent writer.
	beforePut func(name string)
	pageSize  int
}

func newSSM() *ssm { return &ssm{params: map[string]*entry{}, pageSize: 50} }

func (s *ssm) add(name, value, keyID string, modified time.Time) {
	s.params[name] = &entry{value: value, version: 3, keyID: keyID, typ: types.ParameterTypeSecureString, modified: modified,
		history: []string{"v1 " + keyID, "v2 " + keyID}}
}

type throttled struct{}

func (throttled) Error() string                 { return "Rate exceeded" }
func (throttled) ErrorCode() string             { return "ThrottlingException" }
func (throttled) ErrorMessage() string          { return "Rate exceeded" }
func (throttled) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

func (s *ssm) maybeThrottle() error {
	if s.throttle > 0 {
		s.throttle--
		return throttled{}
	}
	return nil
}

func (s *ssm) DescribeParameters(_ context.Context, in *awsssm.DescribeParametersInput, _ ...func(*awsssm.Options)) (*awsssm.DescribeParametersOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.maybeThrottle(); err != nil {
		return nil, err
	}
	if len(in.ParameterFilters) != 1 || aws.ToString(in.ParameterFilters[0].Key) != "Path" || aws.ToString(in.ParameterFilters[0].Option) != "Recursive" {
		return nil, errors.New("the listing must be a recursive Path filter")
	}
	prefix := in.ParameterFilters[0].Values[0] + "/"
	var names []string
	for n := range s.params {
		if strings.HasPrefix(n, prefix) {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	start := 0
	if in.NextToken != nil {
		for i, n := range names {
			if n == *in.NextToken {
				start = i
			}
		}
	}
	out := &awsssm.DescribeParametersOutput{}
	for i := start; i < len(names); i++ {
		if len(out.Parameters) == s.pageSize {
			out.NextToken = aws.String(names[i])
			break
		}
		p := s.params[names[i]]
		m := types.ParameterMetadata{Name: aws.String(names[i]), Type: p.typ, KeyId: aws.String(p.keyID), Version: p.version,
			Tier: p.tier, LastModifiedDate: aws.Time(p.modified)}
		if p.desc != "" {
			m.Description = aws.String(p.desc)
		}
		if p.typ != types.ParameterTypeSecureString {
			m.KeyId = nil
		}
		out.Parameters = append(out.Parameters, m)
	}
	return out, nil
}

func (s *ssm) GetParameters(_ context.Context, in *awsssm.GetParametersInput, _ ...func(*awsssm.Options)) (*awsssm.GetParametersOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.maybeThrottle(); err != nil {
		return nil, err
	}
	if len(in.Names) > 10 || !aws.ToBool(in.WithDecryption) {
		return nil, errors.New("up to 10 names, with decryption")
	}
	s.reads = append(s.reads, in.Names)
	out := &awsssm.GetParametersOutput{}
	for _, n := range in.Names {
		if p, ok := s.params[n]; ok {
			out.Parameters = append(out.Parameters, types.Parameter{Name: aws.String(n), Value: aws.String(p.value), Version: p.version})
		}
	}
	return out, nil
}

func (s *ssm) PutParameter(_ context.Context, in *awsssm.PutParameterInput, _ ...func(*awsssm.Options)) (*awsssm.PutParameterOutput, error) {
	if s.beforePut != nil {
		s.beforePut(aws.ToString(in.Name))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.maybeThrottle(); err != nil {
		return nil, err
	}
	s.puts = append(s.puts, in)
	p := s.params[aws.ToString(in.Name)]
	p.history = append(p.history, "v"+string(rune('0'+p.version))+" "+p.keyID)
	p.version++
	p.value, p.keyID, p.modified = aws.ToString(in.Value), aws.ToString(in.KeyId), t0
	return &awsssm.PutParameterOutput{Version: p.version}, nil
}

func (s *ssm) seed(n int) []string {
	var names []string
	for i := range n {
		name := root + "/internal/oidc/p" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		s.add(name, "VALUE-"+name, "alias/aws/ssm", t0.Add(-72*time.Hour))
		names = append(names, name)
	}
	return names
}

func opts() rekey.Options {
	return rekey.Options{Root: root, KeyID: key, Now: func() time.Time { return t0 },
		Sleep: func(context.Context, time.Duration) error { return nil }}
}

func TestADryRunListsWhatItWouldRewriteAndWritesNothing(t *testing.T) {
	s := newSSM()
	names := s.seed(3)
	s.add(root+"/external/oidc/already", "x", key, t0.Add(-72*time.Hour))
	s.add(root+"/external/oidc/by-arn", "x", "arn:aws:kms:eu-west-1:111122223333:alias/example-secrets", t0.Add(-72*time.Hour))
	s.params[root+"/internal/plain"] = &entry{typ: types.ParameterTypeString, value: "n"}
	s.add("/other/elsewhere", "x", "alias/aws/ssm", t0.Add(-72*time.Hour))
	o := opts()
	o.DryRun = true
	o.Before = func(context.Context, *rekey.Report) error { t.Error("a dry run called the hook"); return nil }
	r, err := rekey.Run(context.Background(), s, o)
	if err != nil || !r.OK {
		t.Fatalf("Run = %v", err)
	}
	if !slices.Equal(r.Rekey, names) || r.Considered != 5 || r.Current != 2 {
		t.Errorf("report = %+v, want %v selected, 5 considered, 2 current", r, names)
	}
	if len(s.puts) != 0 {
		t.Errorf("a dry run wrote %d parameters", len(s.puts))
	}
}

func TestARunRewritesEachParameterUnderTheKeyInBatchesOfTenAndNeverPrintsAValue(t *testing.T) {
	s := newSSM()
	s.pageSize = 7
	names := s.seed(23)
	var waits []time.Duration
	o := opts()
	o.Sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	hooks := 0
	o.Before = func(_ context.Context, r *rekey.Report) error {
		hooks++
		if len(s.puts) != 0 || !slices.Equal(r.Rekey, names) {
			t.Errorf("the hook ran after a write or saw %d names", len(r.Rekey))
		}
		return nil
	}
	r, err := rekey.Run(context.Background(), s, o)
	if err != nil || !r.OK || hooks != 1 {
		t.Fatalf("Run = %v after %d hook calls", err, hooks)
	}
	if r.Rekeyed != 23 || r.Batches != 3 || len(s.reads) != 3 || len(waits) != 3 {
		t.Errorf("rekeyed %d in %d batches, %d reads, %d waits: want 23, 3, 3, 3", r.Rekeyed, r.Batches, len(s.reads), len(waits))
	}
	for _, n := range names {
		p := s.params[n]
		if p.keyID != key || p.version != 4 || p.value != "VALUE-"+n {
			t.Errorf("%s: key %s, version %d, value kept = %v", n, p.keyID, p.version, p.value == "VALUE-"+n)
		}
	}
	for _, in := range s.puts {
		if !aws.ToBool(in.Overwrite) || in.Type != types.ParameterTypeSecureString || aws.ToString(in.KeyId) != key {
			t.Errorf("put = %+v", in)
		}
	}
	if strings.Contains(string(mustJSON(r)), "VALUE-"+root) {
		t.Errorf("the report holds a value")
	}
	// A second run finds nothing left.
	if r, err = rekey.Run(context.Background(), s, o); err != nil || len(r.Rekey) != 0 || r.Current != 23 {
		t.Errorf("second run = %v, %+v", err, r)
	}
}

func TestAHookThatRefusesStopsTheRunBeforeTheFirstWrite(t *testing.T) {
	s := newSSM()
	s.seed(3)
	refused := errors.New("the queue refused the record")
	o := opts()
	o.Before = func(context.Context, *rekey.Report) error { return refused }
	r, err := rekey.Run(context.Background(), s, o)
	if !errors.Is(err, refused) || !errors.Is(err, rekey.ErrNotStarted) || r.OK || len(s.puts) != 0 {
		t.Fatalf("Run = %v, %d writes", err, len(s.puts))
	}
}

func TestATierAndADescriptionAreKept(t *testing.T) {
	s := newSSM()
	n := s.seed(1)[0]
	s.params[n].tier, s.params[n].desc = types.ParameterTierAdvanced, "the state secret"
	if _, err := rekey.Run(context.Background(), s, opts()); err != nil {
		t.Fatal(err)
	}
	if in := s.puts[0]; in.Tier != types.ParameterTierAdvanced || aws.ToString(in.Description) != "the state secret" {
		t.Errorf("put = tier %v, description %q", in.Tier, aws.ToString(in.Description))
	}
}

func TestAParameterChangedWithinTheGraceWindowIsDeferredUnlessIncluded(t *testing.T) {
	s := newSSM()
	names := s.seed(2)
	s.params[names[1]].modified = t0.Add(-2 * time.Hour)
	r, err := rekey.Run(context.Background(), s, opts())
	if err != nil || !slices.Equal(r.Rekey, names[:1]) || !slices.Equal(r.Deferred, names[1:]) || s.params[names[1]].keyID != "alias/aws/ssm" {
		t.Fatalf("Run = %v, %+v", err, r)
	}
	o := opts()
	o.IncludeRecent = true
	if r, err = rekey.Run(context.Background(), s, o); err != nil || r.Rekeyed != 1 || s.params[names[1]].keyID != key {
		t.Fatalf("Run --include-recent = %v, %+v", err, r)
	}
}

func TestAThrottledRequestIsTriedAgainWithADoublingWait(t *testing.T) {
	s := newSSM()
	s.seed(2)
	s.throttle = 3
	var waits []time.Duration
	o := opts()
	o.Sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	o.Pause, o.Backoff = time.Millisecond, 100*time.Millisecond
	r, err := rekey.Run(context.Background(), s, o)
	if err != nil || r.Rekeyed != 2 {
		t.Fatalf("Run = %v", err)
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}
	if !slices.Equal(waits[:3], want) {
		t.Errorf("waits = %v, want %v first", waits, want)
	}
}

func TestAnErrorOtherThanThrottlingStopsTheRunAndSaysWhatIsLeft(t *testing.T) {
	s := newSSM()
	s.seed(25)
	api := &failingPut{ssm: s, after: 12}
	r, err := rekey.Run(context.Background(), api, opts())
	if err == nil || r.OK || r.Rekeyed != 12 || !strings.Contains(err.Error(), "re-keyed so far: 12") {
		t.Fatalf("Run = %v, %+v", err, r)
	}
}

type failingPut struct {
	*ssm
	after, n int
}

func (f *failingPut) PutParameter(ctx context.Context, in *awsssm.PutParameterInput, o ...func(*awsssm.Options)) (*awsssm.PutParameterOutput, error) {
	if f.n++; f.n > f.after {
		return nil, errors.New("AccessDeniedException: kms:Encrypt")
	}
	return f.ssm.PutParameter(ctx, in, o...)
}

func TestAParameterChangedAfterItWasListedIsSkippedAndOneChangedDuringTheWriteIsReported(t *testing.T) {
	s := newSSM()
	names := s.seed(2)
	// Rewritten by someone else between the listing and the read.
	listed := &afterList{ssm: s, hook: func() {
		s.mu.Lock()
		s.params[names[0]].version++
		s.mu.Unlock()
	}}
	r, err := rekey.Run(context.Background(), listed, opts())
	if err != nil || !slices.Equal(r.Skipped, names[:1]) || r.Rekeyed != 1 {
		t.Fatalf("Run = %v, %+v", err, r)
	}

	// Rewritten between the read and the write: the run says so and fails.
	s = newSSM()
	names = s.seed(1)
	s.beforePut = func(n string) {
		s.mu.Lock()
		s.params[n].version++
		s.mu.Unlock()
	}
	r, err = rekey.Run(context.Background(), s, opts())
	if err == nil || len(r.Concurrent) != 1 || !strings.Contains(r.Concurrent[0], names[0]) {
		t.Fatalf("Run = %v, %+v", err, r)
	}
}

type afterList struct {
	*ssm
	hook func()
}

func (a *afterList) DescribeParameters(ctx context.Context, in *awsssm.DescribeParametersInput, o ...func(*awsssm.Options)) (*awsssm.DescribeParametersOutput, error) {
	out, err := a.ssm.DescribeParameters(ctx, in, o...)
	if err == nil && a.hook != nil {
		a.hook()
		a.hook = nil
	}
	return out, err
}

func TestTheAwsManagedKeyAndAMissingRootAreRefused(t *testing.T) {
	for name, o := range map[string]rekey.Options{
		"aws key": {Root: root, KeyID: "alias/aws/ssm"},
		"no key":  {Root: root},
		"no root": {KeyID: key},
	} {
		if _, err := rekey.Run(context.Background(), newSSM(), o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSameKeyReadsAnAliasArnAsItsAlias(t *testing.T) {
	for _, c := range []struct {
		reported, want string
		same           bool
	}{
		{key, key, true},
		{"arn:aws:kms:eu-west-1:111122223333:alias/example-secrets", key, true},
		{"alias/aws/ssm", key, false},
		{"1234abcd-12ab-34cd-56ef-1234567890ab", key, false},
		{"", key, false},
	} {
		if got := rekey.SameKey(c.reported, c.want); got != c.same {
			t.Errorf("SameKey(%q, %q) = %v", c.reported, c.want, got)
		}
	}
}

func mustJSON(r *rekey.Report) []byte {
	raw, _ := json.Marshal(r)
	return raw
}
