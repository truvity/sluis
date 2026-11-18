package orgsettings_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"

	"github.com/truvity/sluis/internal/githubroster/orgconfig"
	"github.com/truvity/sluis/internal/githubroster/orgsettings"
)

const (
	globexRole = "arn:aws:iam::111122223333:role/sluis-read"
	acmeRole   = "arn:aws:iam::444455556666:role/sluis-read"
)

// fakeSTS trusts only the roles it is told to let the controller assume.
type fakeSTS struct {
	mu          sync.Mutex
	trusted     map[string]bool
	externalIDs map[string]string
	calls       []string
	session     []string
	inputs      []*sts.AssumeRoleInput
}

func (f *fakeSTS) AssumeRole(_ context.Context, in *sts.AssumeRoleInput, _ ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	role := aws.ToString(in.RoleArn)
	f.calls = append(f.calls, role)
	f.session = append(f.session, aws.ToString(in.RoleSessionName))
	f.inputs = append(f.inputs, in)

	// A role whose trust policy asks for an external id refuses an assumption without it.
	if want := f.externalIDs[role]; want != "" && aws.ToString(in.ExternalId) != want {
		return nil, errors.New("AccessDenied: the external id does not match")
	}

	if !f.trusted[role] {
		return nil, errors.New("AccessDenied: the role does not trust the controller")
	}

	exp := time.Now().Add(time.Hour)

	return &sts.AssumeRoleOutput{Credentials: &ststypes.Credentials{
		AccessKeyId: aws.String("AKIA:" + role), SecretAccessKey: aws.String("secret"), SessionToken: aws.String("token"), Expiration: &exp,
	}}, nil
}

// account is one AWS account's parameter store, readable only as its role.
type account struct {
	role       string
	parameters map[string]string
	// plain are the parameters that are a String and not a SecureString.
	plain map[string]bool
}

type ssmFake struct {
	creds   aws.CredentialsProvider
	region  string
	account *account
	decrypt *[]bool
}

func (s *ssmFake) GetParameter(ctx context.Context, in *awsssm.GetParameterInput, _ ...func(*awsssm.Options)) (*awsssm.GetParameterOutput, error) {
	got, err := s.creds.Retrieve(ctx)
	if err != nil {
		return nil, err
	}

	*s.decrypt = append(*s.decrypt, aws.ToBool(in.WithDecryption))

	if got.AccessKeyID != "AKIA:"+s.account.role {
		return nil, errors.New("AccessDeniedException: the role may not read this account's parameters")
	}

	value, ok := s.account.parameters[aws.ToString(in.Name)]
	if !ok {
		return nil, errors.New("ParameterNotFound")
	}

	kind := types.ParameterTypeSecureString
	if s.account.plain[aws.ToString(in.Name)] {
		kind = types.ParameterTypeString
	}

	return &awsssm.GetParameterOutput{Parameter: &types.Parameter{Name: in.Name, Value: aws.String(value), Version: 3, Type: kind}}, nil
}

func TestTheDocumentIsReadAsTheOrganisationsRoleInItsOwnAccount(t *testing.T) {
	t.Parallel()

	assume := &fakeSTS{trusted: map[string]bool{globexRole: true}}
	globex := &account{role: globexRole, parameters: map[string]string{"/globex/doc": "org: {}\n", "/globex/credentials/key": "the-value"}}
	acme := &account{role: acmeRole, parameters: map[string]string{"/acme/doc": "other"}}

	var (
		decrypt []bool
		regions []string
	)

	reader := orgsettings.NewAWSReaderWith(map[string]orgconfig.Config{
		"globex": {Read: orgconfig.Read{RoleARN: globexRole, Region: "eu-west-1", Parameter: "/globex/doc"}, SecretPrefix: "/globex/credentials/"},
	}, assume, func(creds aws.CredentialsProvider, region string) orgsettings.ParametersAPI {
		regions = append(regions, region)

		return &ssmFake{creds: creds, region: region, account: globex, decrypt: &decrypt}
	})

	source, ok := reader.For("globex")
	if !ok {
		t.Fatal("no source for an organisation the installation names")
	}

	if _, ok = reader.For("acme"); ok {
		t.Error("a source for an organisation the installation does not name")
	}

	doc, err := source.Document(context.Background())
	if err != nil || string(doc) != "org: {}\n" {
		t.Fatalf("document = %q, %v", doc, err)
	}

	// The version is read without decrypting; the value is decrypted.
	if version, err := source.Version(context.Background(), "/globex/credentials/key"); err != nil || version != 3 {
		t.Fatalf("version = %d, %v", version, err)
	}

	if value, version, err := source.Value(context.Background(), "/globex/credentials/key"); err != nil || value != "the-value" || version != 3 {
		t.Fatalf("value = %q, %d, %v", value, version, err)
	}

	if want := []bool{true, false, true}; len(decrypt) != 3 || decrypt[0] != want[0] || decrypt[1] != want[1] || decrypt[2] != want[2] {
		t.Errorf("decrypt flags = %v, want %v", decrypt, want)
	}

	// One assumption serves the three reads, in the organisation's region,
	// under a session name that says whose it is.
	if len(assume.calls) != 1 || assume.calls[0] != globexRole || assume.session[0] != "sluis-github-globex" {
		t.Errorf("assumed %v as %v", assume.calls, assume.session)
	}

	if len(regions) != 1 || regions[0] != "eu-west-1" {
		t.Errorf("regions = %v", regions)
	}

	// Another account's parameters are not readable with this role.
	other := orgsettings.NewAWSReaderWith(map[string]orgconfig.Config{
		"globex": {Read: orgconfig.Read{RoleARN: globexRole, Region: "eu-west-1", Parameter: "/acme/doc"}},
	}, assume, func(creds aws.CredentialsProvider, region string) orgsettings.ParametersAPI {
		return &ssmFake{creds: creds, region: region, account: acme, decrypt: &decrypt}
	})

	src, _ := other.For("globex")
	if _, err = src.Document(context.Background()); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("the role read another account's parameter: %v", err)
	}
}

func TestARoleThatDoesNotTrustTheControllerIsAnError(t *testing.T) {
	t.Parallel()

	assume := &fakeSTS{trusted: map[string]bool{}}
	account := &account{role: globexRole, parameters: map[string]string{"/globex/doc": "org: {}\n"}}

	var decrypt []bool

	reader := orgsettings.NewAWSReaderWith(map[string]orgconfig.Config{
		"globex": {Read: orgconfig.Read{RoleARN: globexRole, Region: "eu-west-1", Parameter: "/globex/doc"}, SecretPrefix: "/globex/credentials/"},
	}, assume, func(creds aws.CredentialsProvider, region string) orgsettings.ParametersAPI {
		return &ssmFake{creds: creds, region: region, account: account, decrypt: &decrypt}
	})

	src, _ := reader.For("globex")

	_, err := src.Document(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not trust the controller") || !strings.Contains(err.Error(), globexRole) {
		t.Fatalf("error = %v, want the refusal naming the role", err)
	}
}

func TestTheRoleIsAssumedWithTheExternalIDAndASessionPolicy(t *testing.T) {
	t.Parallel()

	assume := &fakeSTS{trusted: map[string]bool{globexRole: true}, externalIDs: map[string]string{globexRole: "ext-123"}}
	account := &account{role: globexRole, parameters: map[string]string{"/globex/doc": "org: {}\n"}}

	var decrypt []bool

	build := func(external string) *orgsettings.AWSReader {
		return orgsettings.NewAWSReaderWith(map[string]orgconfig.Config{
			"globex": {
				Read:         orgconfig.Read{RoleARN: globexRole, Region: "eu-west-1", Parameter: "/globex/doc", ExternalID: external},
				SecretPrefix: "/globex/credentials/",
			},
		}, assume, func(creds aws.CredentialsProvider, region string) orgsettings.ParametersAPI {
			return &ssmFake{creds: creds, region: region, account: account, decrypt: &decrypt}
		})
	}

	// Without the external id the role's trust policy refuses.
	src, _ := build("").For("globex")
	if _, err := src.Document(context.Background()); err == nil || !strings.Contains(err.Error(), "external id") {
		t.Fatalf("an assumption without the external id: %v", err)
	}

	src, _ = build("ext-123").For("globex")
	if _, err := src.Document(context.Background()); err != nil {
		t.Fatal(err)
	}

	in := assume.inputs[len(assume.inputs)-1]
	if aws.ToString(in.ExternalId) != "ext-123" {
		t.Errorf("external id = %q", aws.ToString(in.ExternalId))
	}

	// The session policy limits the role to the document and the secrets'
	// parameters, in the role's own account and partition.
	policy := aws.ToString(in.Policy)
	for _, want := range []string{
		"ssm:GetParameter",
		":ssm:eu-west-1:111122223333:parameter/globex/doc",
		":ssm:eu-west-1:111122223333:parameter/globex/credentials/*",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("session policy %s lacks %q", policy, want)
		}
	}

	if strings.Contains(policy, `"ssm:*"`) || strings.Contains(policy, "parameter/*") {
		t.Errorf("session policy is too wide: %s", policy)
	}
}

func TestASecretsParameterMustBeASecureString(t *testing.T) {
	t.Parallel()

	assume := &fakeSTS{trusted: map[string]bool{globexRole: true}}
	account := &account{
		role: globexRole, parameters: map[string]string{"/globex/doc": "org: {}\n", "/globex/credentials/plain": "v"},
		plain: map[string]bool{"/globex/credentials/plain": true, "/globex/doc": true},
	}

	var decrypt []bool

	reader := orgsettings.NewAWSReaderWith(map[string]orgconfig.Config{
		"globex": {Read: orgconfig.Read{RoleARN: globexRole, Region: "eu-west-1", Parameter: "/globex/doc"}, SecretPrefix: "/globex/credentials/"},
	}, assume, func(creds aws.CredentialsProvider, region string) orgsettings.ParametersAPI {
		return &ssmFake{creds: creds, region: region, account: account, decrypt: &decrypt}
	})

	src, _ := reader.For("globex")

	// The document may be a String.
	if _, err := src.Document(context.Background()); err != nil {
		t.Fatalf("the document as a String: %v", err)
	}

	if _, err := src.Version(context.Background(), "/globex/credentials/plain"); err == nil || !strings.Contains(err.Error(), "SecureString") {
		t.Errorf("a String secret was read: %v", err)
	}

	if _, _, err := src.Value(context.Background(), "/globex/credentials/plain"); err == nil {
		t.Error("a String secret's value was read")
	}
}
