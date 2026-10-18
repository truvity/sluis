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
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
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

func (a *afterList) DescribeParameters(ctx context.Context, in *awsssm.DescribeParametersInput,
	o ...func(*awsssm.Options)) (*awsssm.DescribeParametersOutput, error) {
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

const (
	estateID  = "4c4cae27-0000-4000-8000-000000000001"
	estateARN = "arn:aws:kms:eu-central-1:111122223333:key/" + estateID
	otherID   = "9b9b9b9b-0000-4000-8000-000000000002"
	otherARN  = "arn:aws:kms:eu-central-1:111122223333:key/" + otherID
	ssmID     = "5a5a5a5a-0000-4000-8000-000000000003"
	ssmARN    = "arn:aws:kms:eu-central-1:111122223333:key/" + ssmID
)

// keys is KMS: every spelling of a key resolves to the key's ARN, a denied
// spelling is refused as AWS does, and an unknown one is not found.
type keys struct {
	mu     sync.Mutex
	arns   map[string]string
	denied map[string]bool
	calls  map[string]int
}

func newKeys() *keys {
	k := &keys{arns: map[string]string{}, denied: map[string]bool{}, calls: map[string]int{}}
	for _, key := range []struct{ id, arn string }{{estateID, estateARN}, {otherID, otherARN}, {ssmID, ssmARN}} {
		k.arns[key.id], k.arns[key.arn] = key.arn, key.arn
	}
	k.arns["alias/example-secrets"] = estateARN
	k.arns["arn:aws:kms:eu-central-1:111122223333:alias/example-secrets"] = estateARN
	k.arns["alias/other"] = otherARN
	k.arns["alias/aws/ssm"] = ssmARN
	k.arns["arn:aws:kms:eu-central-1:111122223333:alias/aws/ssm"] = ssmARN
	return k
}

func (k *keys) DescribeKey(_ context.Context, in *kms.DescribeKeyInput, _ ...func(*kms.Options)) (*kms.DescribeKeyOutput, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	id := aws.ToString(in.KeyId)
	k.calls[id]++
	if k.denied[id] {
		return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized to perform kms:DescribeKey on " + id}
	}
	arn, ok := k.arns[id]
	if !ok {
		return nil, &kmstypes.NotFoundException{Message: aws.String("Key '" + id + "' does not exist")}
	}
	return &kms.DescribeKeyOutput{KeyMetadata: &kmstypes.KeyMetadata{Arn: aws.String(arn), KeyId: aws.String(arn[strings.LastIndex(arn, "/")+1:])}}, nil
}

func kmsOpts(k *keys, want string) rekey.Options {
	o := opts()
	o.KeyID, o.KMS = want, k
	return o
}

// Every spelling of the estate key is the estate key; another key and the
// AWS-managed one are not. The configured key may be spelled any way.
func TestKeysAreComparedByTheirResolvedArnNotTheirSpelling(t *testing.T) {
	spellings := []string{"alias/example-secrets", "arn:aws:kms:eu-central-1:111122223333:alias/example-secrets", estateID, estateARN}
	for _, want := range spellings {
		s := newSSM()
		old := t0.Add(-72 * time.Hour)
		for i, sp := range spellings {
			s.add(root+"/internal/same"+string(rune('a'+i)), "x", sp, old)
		}
		s.add(root+"/internal/other-alias", "x", "alias/other", old)
		s.add(root+"/internal/other-id", "x", otherID, old)
		s.add(root+"/internal/aws-ssm", "x", "alias/aws/ssm", old)
		s.add(root+"/internal/aws-ssm-arn", "x", "arn:aws:kms:eu-central-1:111122223333:alias/aws/ssm", old)
		k := newKeys()
		o := kmsOpts(k, want)
		o.DryRun = true
		r, err := rekey.Run(context.Background(), s, o)
		if err != nil || !r.OK {
			t.Fatalf("want %s: Run = %v", want, err)
		}
		wantRekey := []string{root + "/internal/aws-ssm", root + "/internal/aws-ssm-arn", root + "/internal/other-alias", root + "/internal/other-id"}
		if r.Considered != 8 || r.Current != 4 || !slices.Equal(r.Rekey, wantRekey) || len(r.Unknown) != 0 || r.KeyARN != estateARN {
			t.Errorf("want %s: considered %d, current %d, rekey %v, unknown %v, key %q", want, r.Considered, r.Current, r.Rekey, r.Unknown, r.KeyARN)
		}
		// Each distinct spelling is asked once, and the configured one once.
		for id, n := range k.calls {
			if n != 1 {
				t.Errorf("want %s: DescribeKey(%s) called %d times", want, id, n)
			}
		}
		if len(r.Keys) != 8 {
			t.Errorf("want %s: %d keys reported, want 8: %+v", want, len(r.Keys), r.Keys)
		}
		for _, u := range r.Keys {
			if u.Current != (u.ARN == estateARN) {
				t.Errorf("want %s: %+v", want, u)
			}
		}
	}
}

// A real run leaves the parameters on the estate key alone, however spelled,
// and moves the rest, so a role that may not decrypt under the estate key's
// other spelling is never asked to.
func TestARunDoesNotRewriteWhatIsAlreadyUnderTheKeyUnderAnotherSpelling(t *testing.T) {
	s := newSSM()
	old := t0.Add(-72 * time.Hour)
	for i := range 18 {
		s.add(root+"/internal/arn"+string(rune('a'+i)), "x", estateARN, old)
	}
	s.add(root+"/internal/old", "x", "alias/aws/ssm", old)
	r, err := rekey.Run(context.Background(), s, kmsOpts(newKeys(), "alias/example-secrets"))
	if err != nil || r.Current != 18 || r.Rekeyed != 1 || len(s.puts) != 1 || len(s.reads) != 1 || len(s.reads[0]) != 1 {
		t.Fatalf("Run = %v, %+v, %d puts, reads %v", err, r, len(s.puts), s.reads)
	}
	if got := aws.ToString(s.puts[0].KeyId); got != "alias/example-secrets" {
		t.Errorf("written under %q", got)
	}
}

// A key DescribeKey refuses is compared by its spelling: the same spelling is
// the key; another is unknown, and unknown is not re-written unless asked.
func TestAKeyKMSWillNotDescribeIsUnknownAndLeftAloneUnlessIncluded(t *testing.T) {
	build := func() (*ssm, *keys) {
		s := newSSM()
		old := t0.Add(-72 * time.Hour)
		s.add(root+"/internal/spelled", "x", "alias/example-secrets", old)
		s.add(root+"/internal/by-arn", "x", estateARN, old)
		s.add(root+"/internal/by-id", "x", estateID, old)
		s.add(root+"/internal/aws", "x", "alias/aws/ssm", old)
		s.add(root+"/internal/other", "x", "alias/other", old)
		k := newKeys()
		for _, id := range []string{estateARN, estateID, "alias/aws/ssm", "alias/other"} {
			k.denied[id] = true
		}
		return s, k
	}
	s, k := build()
	r, err := rekey.Run(context.Background(), s, kmsOpts(k, "alias/example-secrets"))
	if err != nil || !r.OK {
		t.Fatalf("Run = %v", err)
	}
	// by-arn is the configured key's own ARN, which settles it though KMS refuses
	// to describe it; by-id and other cannot be settled, and the AWS-managed alias
	// is known to be another key whatever KMS says.
	if r.Current != 2 || !slices.Equal(r.Unknown, []string{root + "/internal/by-id", root + "/internal/other"}) ||
		!slices.Equal(r.Rekey, []string{root + "/internal/aws"}) || r.Rekeyed != 1 {
		t.Errorf("report = current %d, unknown %v, rekey %v, rekeyed %d", r.Current, r.Unknown, r.Rekey, r.Rekeyed)
	}
	for _, n := range []string{"by-id", "other"} {
		if p := s.params[root+"/internal/"+n]; p.version != 3 {
			t.Errorf("%s was rewritten", n)
		}
	}
	if !strings.Contains(strings.Join(r.Notes, "\n"), "unknown key") || !strings.Contains(strings.Join(r.Notes, "\n"), "AccessDeniedException") {
		t.Errorf("notes = %v", r.Notes)
	}

	s, k = build()
	o := kmsOpts(k, "alias/example-secrets")
	o.IncludeUnknown = true
	if r, err = rekey.Run(context.Background(), s, o); err != nil || r.Rekeyed != 3 || len(r.Unknown) != 2 {
		t.Fatalf("--include-unknown: %v, %+v", err, r)
	}
}

// When the configured key itself cannot be described, spellings decide, and the
// key ARN of the same key is told apart only when the configured key is an ARN.
func TestAConfiguredKeyKMSWillNotDescribeFallsBackToSpelling(t *testing.T) {
	s := newSSM()
	old := t0.Add(-72 * time.Hour)
	s.add(root+"/internal/spelled", "x", "alias/example-secrets", old)
	s.add(root+"/internal/alias-arn", "x", "arn:aws:kms:eu-central-1:111122223333:alias/example-secrets", old)
	s.add(root+"/internal/by-id", "x", estateID, old)
	k := newKeys()
	k.denied["alias/example-secrets"] = true
	o := kmsOpts(k, "alias/example-secrets")
	o.DryRun = true
	r, err := rekey.Run(context.Background(), s, o)
	if err != nil || r.Current != 2 || !slices.Equal(r.Unknown, []string{root + "/internal/by-id"}) || len(r.Rekey) != 0 {
		t.Fatalf("Run = %v, %+v", err, r)
	}
	if !strings.Contains(strings.Join(r.Notes, "\n"), "kms:DescribeKey on alias/example-secrets failed") {
		t.Errorf("notes = %v", r.Notes)
	}

	// Configured by key ARN: a resolved spelling is compared with it exactly.
	k = newKeys()
	k.denied[estateARN] = true
	o = kmsOpts(k, estateARN)
	o.DryRun = true
	r, err = rekey.Run(context.Background(), s, o)
	if err != nil || r.Current != 3 || len(r.Unknown) != 0 {
		t.Fatalf("by ARN: %v, %+v", err, r)
	}
}

// A configured key KMS does not know stops the run before anything is read.
func TestAConfiguredKeyThatDoesNotExistStopsTheRun(t *testing.T) {
	s := newSSM()
	s.seed(2)
	r, err := rekey.Run(context.Background(), s, kmsOpts(newKeys(), "alias/typo"))
	if err == nil || r.OK || len(s.puts) != 0 || len(s.reads) != 0 || !strings.Contains(err.Error(), "alias/typo") {
		t.Fatalf("Run = %v, %+v", err, r)
	}
}

func mustJSON(r *rekey.Report) []byte {
	raw, _ := json.Marshal(r)
	return raw
}
