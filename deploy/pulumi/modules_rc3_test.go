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
