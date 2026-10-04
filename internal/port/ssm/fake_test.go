package ssm_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// fakeAPI is an in-memory Parameter Store with the behaviour the adapter
// relies on: versions that grow on every write, Overwrite=false, the tier
// limits, a hierarchy listing by whole segments with paging, and a refusal of
// a value that is not text. The real engine is a LocalStack or AWS run.
type fakeAPI struct {
	mu     sync.Mutex
	params map[string]*param
	// puts records every PutParameter, for a test that asserts on what was sent.
	puts []awsssm.PutParameterInput
	// beforePut runs after the version check of a conditional write has been
	// answered and before the write, to model a writer that lands in between.
	afterGet func()
}

type param struct {
	value   string
	version int64
	keyID   string
	tier    types.ParameterTier
}

func newFake() *fakeAPI { return &fakeAPI{params: map[string]*param{}} }

func (f *fakeAPI) GetParameter(_ context.Context, in *awsssm.GetParameterInput, _ ...func(*awsssm.Options)) (*awsssm.GetParameterOutput, error) {
	f.mu.Lock()
	p, ok := f.params[aws.ToString(in.Name)]
	var out *awsssm.GetParameterOutput
	if ok {
		out = &awsssm.GetParameterOutput{Parameter: &types.Parameter{
			Name: in.Name, Value: aws.String(p.value), Version: p.version, Type: types.ParameterTypeSecureString,
		}}
	}
	hook := f.afterGet
	f.mu.Unlock()
	if !ok {
		return nil, &types.ParameterNotFound{Message: aws.String("not found")}
	}
	if hook != nil {
		hook()
	}
	return out, nil
}

func (f *fakeAPI) PutParameter(_ context.Context, in *awsssm.PutParameterInput, _ ...func(*awsssm.Options)) (*awsssm.PutParameterOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, *in)
	name, value := aws.ToString(in.Name), aws.ToString(in.Value)
	if in.Type != types.ParameterTypeSecureString || value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return nil, errors.New("ValidationException: the value is not text")
	}
	limit := 4096
	if in.Tier == types.ParameterTierAdvanced || in.Tier == types.ParameterTierIntelligentTiering {
		limit = 8192
	}
	if len(value) > limit {
		return nil, errors.New("ValidationException: the value is too long for the tier")
	}
	p, ok := f.params[name]
	if ok && !aws.ToBool(in.Overwrite) {
		return nil, &types.ParameterAlreadyExists{Message: aws.String("exists")}
	}
	if !ok {
		p = &param{}
		f.params[name] = p
	}
	p.value, p.version, p.keyID = value, p.version+1, aws.ToString(in.KeyId)
	p.tier = types.ParameterTierStandard
	if len(value) > 4096 {
		p.tier = types.ParameterTierAdvanced
	}
	return &awsssm.PutParameterOutput{Version: p.version, Tier: p.tier}, nil
}

func (f *fakeAPI) DeleteParameter(_ context.Context, in *awsssm.DeleteParameterInput, _ ...func(*awsssm.Options)) (*awsssm.DeleteParameterOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.Name)
	if _, ok := f.params[name]; !ok {
		return nil, &types.ParameterNotFound{Message: aws.String("not found")}
	}
	delete(f.params, name)
	return &awsssm.DeleteParameterOutput{}, nil
}

func (f *fakeAPI) GetParametersByPath(
	_ context.Context, in *awsssm.GetParametersByPathInput, _ ...func(*awsssm.Options),
) (*awsssm.GetParametersByPathOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := aws.ToString(in.Path)
	if strings.HasSuffix(path, "/") || !strings.HasPrefix(path, "/") {
		return nil, errors.New("ValidationException: bad path")
	}
	var names []string
	for n := range f.params {
		if strings.HasPrefix(n, path+"/") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	start := 0
	if in.NextToken != nil {
		start = sort.SearchStrings(names, *in.NextToken)
	}
	limit := int(aws.ToInt32(in.MaxResults))
	if limit == 0 || limit > 10 {
		limit = 10
	}
	out := &awsssm.GetParametersByPathOutput{}
	end := start + limit
	if end < len(names) {
		out.NextToken = aws.String(names[end])
	} else {
		end = len(names)
	}
	for _, n := range names[start:end] {
		out.Parameters = append(out.Parameters, types.Parameter{Name: aws.String(n), Version: f.params[n].version})
	}
	return out, nil
}
