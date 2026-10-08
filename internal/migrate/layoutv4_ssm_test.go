package migrate_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/migrate"
	ssmport "github.com/truvity/sluis/internal/port/ssm"
	"github.com/truvity/sluis/internal/secretstore"
	ssmstate "github.com/truvity/sluis/storage/state/ssm"
)

// The move against a real Parameter Store API: the v3 adapter, the v4 state
// backend and the plain config parameters, all on one root. It skips without
// STORAGE_LOCALSTACK_URL; hack/secrets-layout-conformance.sh sets it and fails
// if the test skipped.
func TestSecretsLayoutOnLocalStack(t *testing.T) {
	url := os.Getenv("STORAGE_LOCALSTACK_URL")
	if url == "" {
		t.Skip("STORAGE_LOCALSTACK_URL is not set: no SSM to test against")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("eu-west-1"))
	if err != nil {
		t.Fatal(err)
	}
	api := awsssm.NewFromConfig(cfg, func(o *awsssm.Options) { o.BaseEndpoint = aws.String(url) })
	root := fmt.Sprintf("/sluis/layout-%d", time.Now().UnixNano())

	v3, err := ssmport.NewWithAPI(api, ssmport.Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := clientcreds.Record{Current: "GENERATED-CURRENT", Previous: "GENERATED-PREVIOUS", PreviousValidUntil: time.Now().Add(time.Hour)}.Encode()
	for path, v := range map[string][]byte{
		"config/issuer/state-secret":         []byte("SEEDED-STATE-SECRET"),
		"config/clients/rp/secret":           []byte("SEEDED-CLIENT-SECRET"),
		"credentials/console/session-key":    {0, 1, 2, 3, 0xff},
		"credentials/oidc-client/gen/secret": rec,
	} {
		if _, err = v3.Put(ctx, path, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = v3.Put(ctx, "export/old-copy", []byte("EXPORT-COPY")); err != nil {
		t.Fatal(err)
	}
	dest := secretstore.FromStore(ssmstate.New(api, root), secretstore.LayoutV4, "")
	o := migrate.SecretsLayoutOptions{V3: v3, Dest: dest, Config: migrate.SSMConfig{API: api, Root: root}, Layout: "transition"}

	rep, err := migrate.MoveSecretsLayout(ctx, o)
	if err != nil || !rep.OK || rep.Written != 4 || rep.Verified != 4 {
		t.Fatalf("move = %v\n%s", err, rep.JSON())
	}
	noValues(t, "the report", string(rep.JSON()))
	if v, ok, err := (migrate.SSMConfig{API: api, Root: root}).Get(ctx, "issuer/state-secret"); err != nil || !ok || v != "SEEDED-STATE-SECRET" {
		t.Errorf("internal/config/issuer/state-secret = %q, %v, %v: the secrets source reads it as it is", v, ok, err)
	}
	cur, prev, err := dest.External.OIDC("gen").Rotating(ctx, time.Hour)
	if err != nil || cur.ClientSecret != "GENERATED-CURRENT" || prev == nil || prev.ClientSecret != "GENERATED-PREVIOUS" {
		t.Errorf("oidc/gen = %+v, %+v, %v", cur, prev, err)
	}
	again, err := migrate.MoveSecretsLayout(ctx, o)
	if err != nil || again.Written != 0 || again.Unchanged != 4 {
		t.Errorf("rerun = %v written %d unchanged %d", err, again.Written, again.Unchanged)
	}

	o.Layout = "v4"
	del, err := migrate.DeleteV3(ctx, o)
	if err != nil || del.Deleted != 4 {
		t.Fatalf("delete = %v\n%s", err, del.JSON())
	}
	left, err := v3.List(ctx, "")
	if err != nil || len(left) != 1 || left[0] != "export/old-copy" {
		t.Errorf("v3 holds %v (%v), want only the exports copy", left, err)
	}
}
