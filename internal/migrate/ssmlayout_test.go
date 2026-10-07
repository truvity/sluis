package migrate_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/truvity/sluis/internal/migrate"
)

type layoutSSM struct {
	params    map[string]string
	keys      map[string]string
	overwrite map[string]bool
	putKey    map[string]string
}

func (f *layoutSSM) GetParametersByPath(
	_ context.Context, in *awsssm.GetParametersByPathInput, _ ...func(*awsssm.Options),
) (*awsssm.GetParametersByPathOutput, error) {
	out := &awsssm.GetParametersByPathOutput{}
	for n, v := range f.params {
		if strings.HasPrefix(n, aws.ToString(in.Path)+"/") {
			out.Parameters = append(out.Parameters, types.Parameter{Name: aws.String(n), Value: aws.String(v)})
		}
	}
	return out, nil
}

func (f *layoutSSM) PutParameter(_ context.Context, in *awsssm.PutParameterInput, _ ...func(*awsssm.Options)) (*awsssm.PutParameterOutput, error) {
	name := aws.ToString(in.Name)
	if _, ok := f.params[name]; ok && !aws.ToBool(in.Overwrite) {
		return nil, &types.ParameterAlreadyExists{}
	}
	f.params[name] = aws.ToString(in.Value)
	if f.overwrite == nil {
		f.overwrite, f.putKey = map[string]bool{}, map[string]string{}
	}
	f.overwrite[name], f.putKey[name] = aws.ToBool(in.Overwrite), aws.ToString(in.KeyId)
	return &awsssm.PutParameterOutput{}, nil
}

func (f *layoutSSM) DescribeParameters(
	_ context.Context, in *awsssm.DescribeParametersInput, _ ...func(*awsssm.Options),
) (*awsssm.DescribeParametersOutput, error) {
	out := &awsssm.DescribeParametersOutput{}
	path := in.ParameterFilters[0].Values[0]
	for n := range f.params {
		if strings.HasPrefix(n, path+"/") {
			key := f.keys[n]
			if key == "" {
				key = "alias/aws/ssm"
			}
			out.Parameters = append(out.Parameters, types.ParameterMetadata{Name: aws.String(n), KeyId: aws.String(key)})
		}
	}
	return out, nil
}

func TestTheConfigurationSecretsMoveToLayoutV3(t *testing.T) {
	f := &layoutSSM{params: map[string]string{
		"/sluis/private/config/oauth/client-id":       "id",
		"/sluis/private/config/oauth/client-secret":   "secret",
		"/sluis/private/config/clients/console":       "client",
		"/sluis/private/config/issuer/state-secret":   "seed",
		"/sluis/private/config/recovery/password":     "pw",
		"/sluis/private/credentials/github-org/a/key": "a credential, the ports' to move",
	}, keys: map[string]string{"/sluis/private/config/issuer/state-secret": "arn:aws:kms:eu-west-1:111122223333:key/cmk"}}
	ctx := context.Background()
	o := migrate.SSMLayoutOptions{From: "/sluis", To: "/sluis/example", DryRun: true}
	r, err := migrate.MoveSSMLayout(ctx, f, o)
	if err != nil || len(r.Copied) != 5 || len(f.params) != 6 {
		t.Fatalf("dry run: %+v %v (%d params)", r, err, len(f.params))
	}
	o.DryRun = false
	if _, err = migrate.MoveSSMLayout(ctx, f, o); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"/sluis/example/private/config/providers/google/default/client-id":     "id",
		"/sluis/example/private/config/providers/google/default/client-secret": "secret",
		"/sluis/example/private/config/clients/console/secret":                 "client",
		"/sluis/example/private/config/issuer/state-secret":                    "seed",
		"/sluis/example/private/config/recovery/password":                      "pw",
	} {
		if f.params[name] != want {
			t.Errorf("%s = %q, want %q", name, f.params[name], want)
		}
	}
	if f.putKey["/sluis/example/private/config/issuer/state-secret"] != "arn:aws:kms:eu-west-1:111122223333:key/cmk" ||
		f.putKey["/sluis/example/private/config/recovery/password"] != "" {
		t.Errorf("each copy keeps its parameter's key: %v", f.putKey)
	}
	for name, ow := range f.overwrite {
		if ow {
			t.Errorf("%s was written with Overwrite, though it was absent", name)
		}
	}
	if _, ok := f.params["/sluis/example/private/credentials/github-org/a/key"]; ok {
		t.Error("a credential was copied: it is the ports' to move")
	}
	r, err = migrate.MoveSSMLayout(ctx, f, o)
	if err != nil || len(r.Unchanged) != 5 || len(r.Copied) != 0 {
		t.Errorf("a second run: %+v %v", r, err)
	}
	f.params["/sluis/example/private/config/recovery/password"] = "changed"
	if _, err = migrate.MoveSSMLayout(ctx, f, o); err == nil || !strings.Contains(err.Error(), "recovery/password") {
		t.Errorf("a different value was overwritten without --overwrite: %v", err)
	}
	if strings.Contains(err.Error(), "changed") || strings.Contains(err.Error(), "pw") {
		t.Errorf("the refusal quotes a value: %v", err)
	}
	o.Overwrite = true
	if _, err = migrate.MoveSSMLayout(ctx, f, o); err != nil || f.params["/sluis/example/private/config/recovery/password"] != "pw" {
		t.Errorf("--overwrite: %v", err)
	}
	for _, to := range []string{"/sluis/export", "/sluis/private", "/a/private/b"} {
		if _, err := migrate.MoveSSMLayout(ctx, f, migrate.SSMLayoutOptions{From: "/sluis", To: to, DryRun: true}); err == nil {
			t.Errorf("--to-root %s was accepted", to)
		}
	}
}
