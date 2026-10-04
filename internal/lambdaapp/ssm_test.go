package lambdaapp_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/truvity/sluis/internal/lambdaapp"
)

type fakeSSM struct {
	values map[string]string
	asked  [][]string
}

func (f *fakeSSM) GetParameters(_ context.Context, in *ssm.GetParametersInput, _ ...func(*ssm.Options)) (*ssm.GetParametersOutput, error) {
	f.asked = append(f.asked, in.Names)
	if !aws.ToBool(in.WithDecryption) {
		return nil, errors.New("a SecureString is read decrypted")
	}
	out := &ssm.GetParametersOutput{}
	for _, n := range in.Names {
		if v, ok := f.values[n]; ok {
			out.Parameters = append(out.Parameters, types.Parameter{Name: aws.String(n), Value: aws.String(v)})
		} else {
			out.InvalidParameters = append(out.InvalidParameters, n)
		}
	}
	return out, nil
}

func resolve(t *testing.T, api *fakeSSM, environ ...string) (map[string]string, error) {
	t.Helper()
	got := map[string]string{}
	_, err := lambdaapp.ResolveEnv(context.Background(), environ,
		func(k, v string) error { got[k] = v; return nil },
		func(context.Context) (lambdaapp.ParameterAPI, error) { return api, nil })
	return got, err
}

func TestSecretsAreReadFromSSMIntoTheVariablesTheConfigNames(t *testing.T) {
	api := &fakeSSM{values: map[string]string{"/sluis/private/oauth/secret": "s3cret", "/sluis/export/x": "y"}}
	got, err := resolve(t, api, "PATH=/bin", "OAUTH=ssm:/sluis/private/oauth/secret", "EXP=ssm:/sluis/export/x", "PLAIN=value")
	if err != nil {
		t.Fatal(err)
	}
	if got["OAUTH"] != "s3cret" || got["EXP"] != "y" || len(got) != 2 {
		t.Errorf("resolved %v", got)
	}
}

func TestNoSSMVariableMeansNoSSMCall(t *testing.T) {
	_, err := lambdaapp.ResolveEnv(context.Background(), []string{"A=b"}, nil,
		func(context.Context) (lambdaapp.ParameterAPI, error) { return nil, errors.New("opened") })
	if err != nil {
		t.Errorf("a function with no ssm: variable needs no SSM: %v", err)
	}
}

func TestAPathOutsideTheServicesRootsIsRefusedWithoutAnyCall(t *testing.T) {
	for _, path := range []string{"/other/app/key", "/sluis/private/", "/sluis/privateX/a", "sluis/private/a", "/sluis/../x"} {
		api := &fakeSSM{}
		if _, err := resolve(t, api, "K=ssm:"+path); err == nil {
			t.Errorf("%q was accepted", path)
		}
		if len(api.asked) != 0 {
			t.Errorf("%q: SSM was called", path)
		}
	}
}

func TestAMissingParameterNamesThePathAndNeverAValue(t *testing.T) {
	_, err := resolve(t, &fakeSSM{values: map[string]string{}}, "K=ssm:/sluis/private/absent")
	if err == nil || !strings.Contains(err.Error(), "/sluis/private/absent") {
		t.Errorf("error %v", err)
	}
}

func TestMoreThanTenParametersAreReadInBatchesOfTen(t *testing.T) {
	api := &fakeSSM{values: map[string]string{}}
	var environ []string
	for i := range 23 {
		name := string(rune('A'+i)) + "_V"
		path := "/sluis/private/k" + string(rune('a'+i))
		api.values[path] = "v" + name
		environ = append(environ, name+"=ssm:"+path)
	}
	got, err := resolve(t, api, environ...)
	if err != nil || len(got) != 23 {
		t.Fatalf("%d resolved: %v", len(got), err)
	}
	if len(api.asked) != 3 || len(api.asked[0]) != 10 || len(api.asked[2]) != 3 {
		t.Errorf("batches %v", api.asked)
	}
}

func TestSecretFilesAreWrittenUnderTmpWithMode0600(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "sluis-secrets-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	api := &fakeSSM{values: map[string]string{"/sluis/private/issuer/state-secret": "c2VjcmV0"}}
	path := dir + "/sub/state-secret"
	spec := `[{"parameter":"/sluis/private/issuer/state-secret","path":"` + path + `"}]`
	paths, err := lambdaapp.WriteSecretFiles(context.Background(), spec,
		func(context.Context) (lambdaapp.ParameterAPI, error) { return api, nil })
	if err != nil || len(paths) != 1 {
		t.Fatalf("%v %v", paths, err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "c2VjcmV0" {
		t.Errorf("content %q, %v", b, err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
}

func TestSecretFilesRefuseAPathOutsideTmpAndAParameterOutsideTheRoots(t *testing.T) {
	open := func(context.Context) (lambdaapp.ParameterAPI, error) { return &fakeSSM{}, errors.New("must not open") }
	for _, spec := range []string{
		`[{"parameter":"/sluis/private/a","path":"/var/task/config/a"}]`,
		`[{"parameter":"/sluis/private/a","path":"/tmp/../etc/a"}]`,
		`[{"parameter":"/other/a","path":"/tmp/a"}]`,
		`not json`,
	} {
		if _, err := lambdaapp.WriteSecretFiles(context.Background(), spec, open); err == nil {
			t.Errorf("%s was accepted", spec)
		}
	}
	if paths, err := lambdaapp.WriteSecretFiles(context.Background(), "", open); err != nil || paths != nil {
		t.Errorf("empty: %v %v", paths, err)
	}
}
