package secretstore_test

import (
	"context"
	"testing"

	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
	ssmstate "github.com/truvity/sluis/storage/state/ssm"
)

// recordingSSM is a Parameter Store that holds nothing and remembers what it
// was asked to write.
type recordingSSM struct {
	ssmstate.API
	puts []*awsssm.PutParameterInput
}

func (r *recordingSSM) GetParameter(context.Context, *awsssm.GetParameterInput, ...func(*awsssm.Options)) (*awsssm.GetParameterOutput, error) {
	return nil, &types.ParameterNotFound{}
}

func (r *recordingSSM) PutParameter(_ context.Context, in *awsssm.PutParameterInput, _ ...func(*awsssm.Options)) (*awsssm.PutParameterOutput, error) {
	r.puts = append(r.puts, in)
	return &awsssm.PutParameterOutput{Version: 1}, nil
}

// secrets.kmsKeyId is the KeyId of every PutParameter the service makes at run
// time, in both layouts and in both namespaces; unset, none carries a KeyId and
// SSM uses alias/aws/ssm (D270).
func TestEveryRuntimeWriteCarriesTheConfiguredKeyID(t *testing.T) {
	const key = "alias/example-secrets"
	ctx := context.Background()
	write := func(t *testing.T, keyAlias string, v5 bool) []*awsssm.PutParameterInput {
		t.Helper()
		api := &recordingSSM{}
		root := ssmstate.New(api, "/sluis/example")
		put := func(v state.Value[[]byte]) {
			if _, err := v.Put(ctx, []byte("x"), ""); err != nil {
				t.Fatal(err)
			}
		}
		if v5 {
			s := secretstore.FromStoreV5(root, keyAlias)
			put(s.OIDC().StateSecret())
			put(s.OIDC().SignInClientID("google"))
		} else {
			s := secretstore.FromStore(root, keyAlias)
			if _, err := s.External.OIDC("local-dev").Put(ctx, secretstore.OIDCv1{ClientID: "local-dev", ClientSecret: "x"}, ""); err != nil {
				t.Fatal(err)
			}
		}
		return api.puts
	}
	for name, v5 := range map[string]bool{"layout v4": false, "layout v5": true} {
		t.Run(name, func(t *testing.T) {
			puts := write(t, key, v5)
			if len(puts) == 0 {
				t.Fatal("nothing was written")
			}
			for _, in := range puts {
				if in.KeyId == nil || *in.KeyId != key || in.Type != types.ParameterTypeSecureString {
					t.Errorf("%s: KeyId %v, type %v", *in.Name, in.KeyId, in.Type)
				}
			}
			for _, in := range write(t, "", v5) {
				if in.KeyId != nil {
					t.Errorf("%s: KeyId %s with no key configured", *in.Name, *in.KeyId)
				}
			}
		})
	}
}
