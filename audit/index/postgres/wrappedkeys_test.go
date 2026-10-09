package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/truvity/sluis/audit/index/postgres"
	"github.com/truvity/sluis/audit/internal/pgtest"
	"github.com/truvity/sluis/storage/keys/conformance"
	"github.com/truvity/sluis/storage/keys/kms"
)

// envKMS names a LocalStack (or any KMS endpoint). Unset, the tests that need
// KMS skip; hack/audit-service-tests.sh sets it and refuses a run that skipped.
const envKMS = "AUDIT_KMS_URL"

func kmsClient(t *testing.T) *awskms.Client {
	t.Helper()
	url := os.Getenv(envKMS)
	if url == "" {
		t.Skipf("%s is not set: no KMS to run against", envKMS)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
	cfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return awskms.NewFromConfig(cfg, func(o *awskms.Options) { o.BaseEndpoint = aws.String(url) })
}

func kmsKey(t *testing.T, c *awskms.Client, spec types.KeySpec, usage types.KeyUsageType) string {
	t.Helper()
	out, err := c.CreateKey(context.Background(), &awskms.CreateKeyInput{KeySpec: spec, KeyUsage: usage})
	if err != nil {
		t.Fatal(err)
	}
	r := make([]byte, 6)
	_, _ = rand.Read(r)
	alias := "alias/wrapped-" + hex.EncodeToString(r)
	if _, err := c.CreateAlias(context.Background(), &awskms.CreateAliasInput{AliasName: &alias, TargetKeyId: out.KeyMetadata.KeyId}); err != nil {
		t.Fatal(err)
	}
	return alias
}

// The kms adapter's whole suite — pseudonyms stable per tenant and purpose,
// tenants not joining, erasure that stays erased and is told from never seen —
// with the wrapped secrets in the deployment's Postgres and the keys in KMS.
func TestTheKMSAdapterOverThePostgresWrappedStore(t *testing.T) {
	c := kmsClient(t)
	db := pgtest.Open(t)
	conformance.Run(t, conformance.Subject{
		Backend:        kms.New(c, kms.WithWrappedStore(postgres.NewWrappedKeys(db))),
		Symmetric:      kmsKey(t, c, types.KeySpecSymmetricDefault, types.KeyUsageTypeEncryptDecrypt),
		OtherSymmetric: kmsKey(t, c, types.KeySpecSymmetricDefault, types.KeyUsageTypeEncryptDecrypt),
		Signing:        kmsKey(t, c, types.KeySpecEccNistP384, types.KeyUsageTypeSignVerify),
	})
}

func TestTheWrappedKeyStoreConvergesAndErases(t *testing.T) {
	db := pgtest.Open(t)
	ctx := context.Background()
	w := postgres.NewWrappedKeys(db)

	// Concurrent first uses end on one value.
	var wg sync.WaitGroup
	got := make([][]byte, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := w.PutIfAbsent(ctx, "pseudonym/acme", []byte{byte(i + 1)})
			if err != nil {
				t.Error(err)
			}
			got[i] = v
		}()
	}
	wg.Wait()
	for _, v := range got[1:] {
		if string(v) != string(got[0]) {
			t.Fatalf("first uses diverged: %v and %v", got[0], v)
		}
	}

	if err := w.Tombstone(ctx, "pseudonym/acme"); err != nil {
		t.Fatal(err)
	}
	if err := w.Tombstone(ctx, "pseudonym/acme"); err != nil {
		t.Fatalf("a second tombstone must succeed: %v", err)
	}
	if _, found, _ := w.Get(ctx, "pseudonym/acme"); found {
		t.Fatal("the wrapped key survived its tombstone")
	}
	if gone, err := w.Tombstoned(ctx, "pseudonym/acme"); err != nil || !gone {
		t.Fatalf("Tombstoned = %v, %v", gone, err)
	}
	if gone, _ := w.Tombstoned(ctx, "pseudonym/never"); gone {
		t.Fatal("an id never seen reads as destroyed")
	}
	// A first use after the erasure cannot bring the key back.
	if _, err := w.PutIfAbsent(ctx, "pseudonym/acme", []byte{9}); err == nil {
		t.Fatal("a destroyed id took a new wrapped key")
	}
	if _, found, _ := w.Get(ctx, "pseudonym/acme"); found {
		t.Fatal("the erased key came back")
	}
}
