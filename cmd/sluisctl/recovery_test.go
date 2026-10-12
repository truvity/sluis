package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
)

type fakeParameters struct {
	values map[string]string
	asked  []string
	err    error
}

func (f *fakeParameters) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	f.asked = append(f.asked, aws.ToString(in.Name))
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.values[aws.ToString(in.Name)]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "ParameterNotFound", Message: "no such parameter"}
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: aws.String(v)}}, nil
}

func recoveryDocument(password string) string {
	return `{"value":"` + base64.StdEncoding.EncodeToString([]byte(password)) + `"}`
}

func TestDecodeTheRecoveryPassword(t *testing.T) {
	for name, tc := range map[string]struct{ in, want, fail string }{
		"layout v5 document":    {in: recoveryDocument("p4ss/w0rd+=="), want: "p4ss/w0rd+=="},
		"document with padding": {in: recoveryDocument("ab"), want: "ab"},
		"layout v4 text":        {in: "plain-password\n", want: "plain-password"},
		"empty":                 {in: "", fail: "empty"},
		"empty value":           {in: `{"value":""}`, fail: "empty"},
		"no value":              {in: `{"other":"x"}`, fail: "without"},
		"not json":              {in: `{"value":`, fail: "without"},
		"not base64":            {in: `{"value":"***"}`, fail: "not base64"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodeRecoveryPassword(tc.in)
			if tc.fail != "" {
				if err == nil || !strings.Contains(err.Error(), tc.fail) {
					t.Fatalf("err = %v, want one containing %q", err, tc.fail)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestTheRecoveryPasswordIsReadFromTheV5ParameterAndFallsBackToV4(t *testing.T) {
	v5 := &fakeParameters{values: map[string]string{"/sluis/acme/internal/oidc/recovery-password": recoveryDocument("secret")}}
	if got, err := readRecoveryPassword(context.Background(), v5, "acme"); err != nil || got != "secret" {
		t.Fatalf("v5: %q, %v", got, err)
	}
	if len(v5.asked) != 1 {
		t.Errorf("v5 asked %v: the fallback must not be read when v5 answers", v5.asked)
	}

	v4 := &fakeParameters{values: map[string]string{"/sluis/acme/internal/config/recovery/password": "legacy"}}
	if got, err := readRecoveryPassword(context.Background(), v4, "acme"); err != nil || got != "legacy" {
		t.Fatalf("v4: %q, %v", got, err)
	}

	none := &fakeParameters{values: map[string]string{}}
	if _, err := readRecoveryPassword(context.Background(), none, "acme"); err == nil || !strings.Contains(err.Error(), "wrong --instance or --region") {
		t.Fatalf("missing: %v", err)
	}

	denied := &fakeParameters{err: &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no"}}
	_, err := readRecoveryPassword(context.Background(), denied, "acme")
	if err == nil || !errors.Is(explain(err), errNotGranted) {
		t.Fatalf("a refusal is not 'not granted': %v", err)
	}
	if len(denied.asked) != 1 {
		t.Errorf("a refusal fell back to v4: %v", denied.asked)
	}
}

func withRecovery(t *testing.T, params *fakeParameters, terminal bool) {
	t.Helper()
	oldClient, oldTerm := newSSMClient, stdoutIsTerminal
	newSSMClient = func(context.Context, string, string) (parameterAPI, error) { return params, nil }
	stdoutIsTerminal = func() bool { return terminal }
	t.Cleanup(func() { newSSMClient, stdoutIsTerminal = oldClient, oldTerm })
}

func TestTheRecoveryPasswordIsNotPrintedToATerminal(t *testing.T) {
	params := &fakeParameters{values: map[string]string{"/sluis/acme/internal/oidc/recovery-password": recoveryDocument("secret")}}
	withRecovery(t, params, true)

	var out bytes.Buffer
	err := recoveryPassword([]string{"--instance", "acme"}, &out)
	var usage usageError
	if !errors.As(err, &usage) || !strings.Contains(err.Error(), "--show") {
		t.Fatalf("err = %v, want a usage error naming --show", err)
	}
	if out.Len() != 0 || len(params.asked) != 0 {
		t.Errorf("printed %q and read %v before refusing", out.String(), params.asked)
	}

	if err = recoveryPassword([]string{"--instance", "acme", "--show"}, &out); err != nil {
		t.Fatalf("--show: %v", err)
	}
	if out.String() != "secret\n" {
		t.Errorf("--show printed %q", out.String())
	}
}

func TestTheRecoveryPasswordPipesAlone(t *testing.T) {
	params := &fakeParameters{values: map[string]string{"/sluis/acme/internal/oidc/recovery-password": recoveryDocument("secret")}}
	withRecovery(t, params, false)

	var out bytes.Buffer
	if err := recoveryPassword([]string{"--instance", "acme"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "secret\n" {
		t.Errorf("stdout = %q, want the password and a newline and nothing else", out.String())
	}
	if err := recoveryPassword([]string{"--instance", "../x"}, &out); err == nil {
		t.Error("an instance that is a path was accepted")
	}
}
