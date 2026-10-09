package chart_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An install with its services in the cluster (NATS, PostgreSQL, OpenBAO) needs
// no SQS, DynamoDB or SSM and no Lambda: S3 and a key are all that stay outside.
// These tests hold examples/in-cluster-services.yaml, and the shapes around it,
// to that.

// helmOutput renders the chart with the values laid over one another in order
// and returns the text, or helm's refusal.
func helmOutput(t *testing.T, values ...string) (string, string, error) {
	t.Helper()
	args := []string{"template", "audit", "."}
	for i, v := range values {
		p := v
		if strings.Contains(v, "\n") {
			p = filepath.Join(t.TempDir(), "values-"+string(rune('a'+i))+".yaml")
			if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		args = append(args, "-f", p)
	}
	cmd := exec.Command(helm(t), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// awsServices are the AWS services an in-cluster install replaces. Nothing the
// chart renders for it may name one.
var awsServices = []string{"sqs", "dynamodb", "ssm:", "lambda", "169.254."}

// awsOnly are, besides those, the spellings of what only AWS has: for a store at
// an endpoint and a key outside KMS, nothing may carry one.
var awsOnly = append([]string{"amazonaws", "arn:" + "aws", "alias/", "kmskey", "key_alias", "stateroot", "eks.amazonaws.com", "pod-identity"}, awsServices...)

func assertNone(t *testing.T, rendered string, words []string) {
	t.Helper()
	lower := strings.ToLower(rendered)
	for _, word := range words {
		if i := strings.Index(lower, word); i >= 0 {
			from := max(0, i-80)
			t.Errorf("the render carries %q:\n...%s...", word, rendered[from:min(len(rendered), i+80)])
		}
	}
}

func assertNoAWS(t *testing.T, rendered string) { t.Helper(); assertNone(t, rendered, awsOnly) }

func TestTheInClusterExampleReplacesSQSDynamoDBAndSSM(t *testing.T) {
	out, stderr, err := helmOutput(t, "examples/in-cluster-services.yaml")
	if err != nil {
		t.Fatalf("examples/in-cluster-services.yaml: %v\n%s", err, stderr)
	}
	assertNone(t, out, awsServices)
	for _, want := range []string{"nats://nats.nats.svc:4222", "adapter: kms", "key_alias: alias/audit-archive"} {
		if !strings.Contains(out, want) {
			t.Errorf("the render lacks %q", want)
		}
	}
}

// A store at an endpoint, whatever it is: a MinIO in the cluster, an R2 bucket.
func TestPresetsOnAnS3CompatibleEndpointRender(t *testing.T) {
	stores := map[string]string{
		"minio": "{bucket: audit-archive, region: us-east-1, endpoint: 'http://minio.storage.svc:9000', path_style: true}",
		"r2":    "{bucket: audit-archive, region: auto, endpoint: 'https://account.r2.example.test'}",
	}
	for store, spec := range stores {
		for _, preset := range []string{"operational", "standard"} {
			t.Run(store+"/"+preset, func(t *testing.T) {
				values := "presets:\n  " + preset + ": " + spec + "\n"
				if preset == "standard" {
					values = "profiles:\n  p:\n    frameworks: [security]\n" + values
				} else {
					values += "jobs:\n  notary:\n    enabled: false\n"
				}
				stderr, err := helmTemplate(t, values)
				if err != nil {
					t.Fatalf("%s on a %s endpoint was refused: %s", preset, store, stderr)
				}
			})
		}
	}
}

// Object Lock is S3's: the attested preset on an endpoint is refused, and the
// refusal says where it can go.
func TestTheAttestedPresetIsRefusedOnAnEndpoint(t *testing.T) {
	for _, endpoint := range []string{"http://minio.storage.svc:9000", "https://account.r2.example.test"} {
		values := "profiles:\n  p:\n    frameworks: [security]\n    preset: attested\n" +
			"presets:\n  attested: {bucket: audit-attested, region: auto, endpoint: '" + endpoint + "'}\n"
		stderr, err := helmTemplate(t, values)
		if err == nil {
			t.Fatalf("attested on %s rendered", endpoint)
		}
		for _, want := range []string{"presets.attested", endpoint, "no Object Lock", "Keep it on AWS S3"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("attested on %s: the refusal lacks %q: %s", endpoint, want, stderr)
			}
		}
	}
}

// Static credentials and a path style belong to a store at an endpoint; on AWS
// they are refused, so that the two shapes are not mixed up by omission.
func TestEndpointOnlyFieldsAreRefusedOnAWS(t *testing.T) {
	for field, want := range map[string]string{
		"credentials: internal/audit/r2": "presets.standard.credentials",
		"path_style: true":               "presets.standard.path_style",
	} {
		values := "profiles:\n  p:\n    frameworks: [security]\npresets:\n  standard: {bucket: audit-archive, " + field + "}\n"
		stderr, err := helmTemplate(t, values)
		if err == nil || !strings.Contains(stderr, want) {
			t.Errorf("%s on AWS S3: want a refusal naming %s, got err=%v %s", field, want, err, stderr)
		}
	}
}

// Both key providers that need no cloud render, and neither renders an AWS
// field: transit asks the same OpenBAO from every replica, local keeps its
// keys in a directory every replica sees.
func TestTheKeyProvidersThatNeedNoCloudRenderNoAWSField(t *testing.T) {
	t.Run("transit", func(t *testing.T) {
		out, stderr, err := helmOutput(t, "testdata/values/transit.yaml")
		if err != nil {
			t.Fatalf("%v\n%s", err, stderr)
		}
		// This file keeps an example NTP server and AWS-free presets; only the
		// fields that are AWS's own are looked for.
		assertNoAWS(t, out)
	})
	t.Run("local", func(t *testing.T) {
		const local = `
externalIdentifiersAreOpaque: false
writer:
  config:
    keys:
      adapter: local
      instance: audit
      pseudonym: audit-pseudonym
      rootFile: /etc/audit/keys/root
  secretMounts:
    - secretName: audit-key-root
      mountPath: /etc/audit/keys
keysVolume:
  enabled: true
  accessModes: [ReadWriteMany]
`
		out, stderr, err := helmOutput(t, "testdata/values/e2e.yaml", local)
		if err != nil {
			t.Fatalf("%v\n%s", err, stderr)
		}
		assertNoAWS(t, out)
		if !strings.Contains(out, "adapter: local") || !strings.Contains(out, "PersistentVolumeClaim") {
			t.Error("the local adapter's render has neither the adapter nor a keys volume")
		}
	})
}

// A local key directory is the only copy of the keys, so the chart insists it
// lives on a volume that every replica sees.
func TestLocalKeysOnReadWriteOnceAreRefusedWithSeveralWriters(t *testing.T) {
	const local = `
externalIdentifiersAreOpaque: false
writer:
  config:
    keys:
      adapter: local
      instance: audit
      pseudonym: audit-pseudonym
      rootFile: /etc/audit/keys/root
keysVolume:
  enabled: true
  accessModes: [ReadWriteOnce]
`
	_, stderr, err := helmOutput(t, "testdata/values/e2e.yaml", local)
	if err == nil || !strings.Contains(stderr, "ReadWriteMany") {
		t.Fatalf("want a refusal naming ReadWriteMany, got err=%v %s", err, stderr)
	}
}

// The kind tier's second lane lays e2e-kms.yaml over e2e.yaml: the notary then
// signs with a KMS key, and the render has the CronJob the suite triggers.
func TestTheKMSNotaryOverlayRendersTheNotary(t *testing.T) {
	out, stderr, err := helmOutput(t, "testdata/values/e2e.yaml", "testdata/values/e2e-kms.yaml")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	assertNone(t, out, awsServices)
	for _, want := range []string{"kind: CronJob", "name: audit-notary", "adapter: kms", "AWS_ENDPOINT_URL_KMS"} {
		if !strings.Contains(out, want) {
			t.Errorf("the render lacks %q", want)
		}
	}
}
