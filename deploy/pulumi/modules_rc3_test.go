package sluispulumi_test

import (
	"testing"

	yaml "go.yaml.in/yaml/v3"

	sluisconfig "github.com/truvity/sluis/config"
	arp "github.com/truvity/sluis/deploy/pulumi"
)

// Every function builds the blobs adapter, which refuses to start without its
// store: with external blobs every module document names them, not the Google
// module's alone.
func TestEveryModuleDocumentCarriesTheExternalBlobs(t *testing.T) {
	in := moduleInstallation(t, "modules-full")
	in.AWS.Bucket = ""
	rec, _ := mustLambda(t, moduleEstate(t, in, func(a *arp.LambdaArgs) {
		ext := r2()
		ext.CredentialsRef = "internal/google/blobs-r2"
		a.Storage = &arp.StorageGrant{External: ext}
	}))
	for _, m := range sluisconfig.Modules() {
		var doc struct {
			Ports struct {
				Blob struct {
					Adapter string `yaml:"adapter"`
					S3      struct {
						Bucket   string `yaml:"bucket"`
						Endpoint string `yaml:"endpoint"`
					} `yaml:"s3"`
				} `yaml:"blob"`
			} `yaml:"ports"`
		}
		if err := yaml.Unmarshal([]byte(archiveFiles(t, rec.one(t, layerType, "staging-"+m+"-config"))["sluis/sluis.yaml"]), &doc); err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		if b := doc.Ports.Blob; b.Adapter != "s3" || b.S3.Bucket != "acme-blobs" || b.S3.Endpoint == "" {
			t.Errorf("%s: ports.blob is %+v, want the external store", m, b)
		}
	}
}

// Every function builds the audit adapter, at its start and in a tick of the
// directory refresh: every module document names the queue, the Google module's
// too.
func TestEveryModuleDocumentCarriesTheAuditQueue(t *testing.T) {
	in := moduleInstallation(t, "modules-full")
	rec, _ := mustLambda(t, moduleEstate(t, in))
	for _, m := range sluisconfig.Modules() {
		var doc struct {
			Adapters struct {
				Audit struct {
					Adapter  string `yaml:"adapter"`
					Settings struct {
						QueueURL string `yaml:"queueURL"`
					} `yaml:"settings"`
				} `yaml:"audit"`
				Trigger struct {
					Adapter string `yaml:"adapter"`
				} `yaml:"trigger"`
			} `yaml:"adapters"`
		}
		if err := yaml.Unmarshal([]byte(archiveFiles(t, rec.one(t, layerType, "staging-"+m+"-config"))["sluis/sluis.yaml"]), &doc); err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		if a := doc.Adapters.Audit; a.Adapter != "sqs" || a.Settings.QueueURL == "" {
			t.Errorf("%s: adapters.audit is %+v, want sqs with the queue", m, a)
		}
		if tr := doc.Adapters.Trigger.Adapter; tr != "memory" {
			t.Errorf("%s: adapters.trigger is %q, want memory", m, tr)
		}
	}
}
