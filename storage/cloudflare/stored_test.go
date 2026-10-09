//nolint:lll // fixtures are one-line documents
package cloudflare_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/truvity/sluis/storage/cloudflare"
)

func storedDoc(id string, expires time.Time) []byte {
	return fmt.Appendf(nil, `{"schema":"cloudflare/v1","access_key_id":%q,"secret_access_key":"secret-%s","endpoint":"https://acct.r2.example.test","expires_on":%q}`,
		id, id, expires.UTC().Format(time.RFC3339))
}

// source is a document that changes when the test says, counting its reads.
type source struct {
	doc   []byte
	err   error
	reads int
}

func (s *source) read(context.Context) ([]byte, error) {
	s.reads++
	return s.doc, s.err
}

func TestParseStoredR2(t *testing.T) {
	exp := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	got, err := cloudflare.ParseStoredR2(storedDoc("k1", exp))
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessKeyID != "k1" || got.SecretAccessKey != "secret-k1" || !got.ExpiresOn.Equal(exp) || got.Endpoint == "" {
		t.Fatalf("parsed %+v", got)
	}
	for name, raw := range map[string]string{
		"not json":     `nope`,
		"other schema": `{"schema":"s3-credentials/v1","access_key_id":"a","secret_access_key":"b","expires_on":"2026-10-09T12:00:00Z"}`,
		"token preset": `{"schema":"cloudflare/v1","token":"t","expires_on":"2026-10-09T12:00:00Z"}`,
		"no secret":    `{"schema":"cloudflare/v1","access_key_id":"a","expires_on":"2026-10-09T12:00:00Z"}`,
		"bad expiry":   `{"schema":"cloudflare/v1","access_key_id":"a","secret_access_key":"b","expires_on":"tomorrow"}`,
	} {
		if _, err := cloudflare.ParseStoredR2([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), `"b"`) || strings.Contains(err.Error(), `"t"`) {
			t.Errorf("%s: the error carries a value: %v", name, err)
		}
	}
	// A field this version does not know is not breaking.
	if _, err := cloudflare.ParseStoredR2([]byte(`{"schema":"cloudflare/v1","access_key_id":"a","secret_access_key":"b","expires_on":"2026-10-09T12:00:00Z","new":"x"}`)); err != nil {
		t.Fatalf("an unknown field was refused: %v", err)
	}
}

func TestStoredProviderRereadsBeforeExpiry(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	src := &source{doc: storedDoc("k1", now.Add(10*time.Minute))}
	p, err := cloudflare.NewStoredProvider(cloudflare.StoredConfig{Read: src.read, Source: "/sluis/main/external/cloudflare/r2", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessKeyID != "k1" || c.SecretAccessKey != "secret-k1" || !c.CanExpire {
		t.Fatalf("credentials %+v", c)
	}
	// Ten minutes left: read again after the recheck, a minute.
	if want := now.Add(cloudflare.DefaultRecheck); !c.Expires.Equal(want) {
		t.Fatalf("expires %s, want %s", c.Expires, want)
	}

	// Close to its end: read again before the margin, never sooner than half
	// the time left.
	src.doc = storedDoc("k1", now.Add(90*time.Second))
	c, _ = p.Retrieve(context.Background())
	if want := now.Add(60 * time.Second); !c.Expires.Equal(want) {
		t.Fatalf("expires %s, want %s (margin)", c.Expires, want)
	}
	src.doc = storedDoc("k1", now.Add(20*time.Second))
	c, _ = p.Retrieve(context.Background())
	if want := now.Add(10 * time.Second); !c.Expires.Equal(want) {
		t.Fatalf("expires %s, want %s (half the time left)", c.Expires, want)
	}

	// A rotated document is picked up at the next read.
	src.doc = storedDoc("k2", now.Add(15*time.Minute))
	if c, _ = p.Retrieve(context.Background()); c.AccessKeyID != "k2" {
		t.Fatalf("rotated credential not read: %s", c.AccessKeyID)
	}
}

func TestStoredProviderRefusesExpired(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	src := &source{doc: storedDoc("k1", now.Add(-time.Second))}
	p, _ := cloudflare.NewStoredProvider(cloudflare.StoredConfig{Read: src.read, Source: "/etc/audit/sluis/external/cloudflare/r2", Now: func() time.Time { return now }})
	_, err := p.Retrieve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "expired") || !strings.Contains(err.Error(), "/etc/audit/sluis/external/cloudflare/r2") {
		t.Fatalf("an expired document: %v", err)
	}
}

func TestStoredProviderKeepsAGoodCredentialThroughAFailedRead(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	src := &source{doc: storedDoc("k1", now.Add(10*time.Minute))}
	p, _ := cloudflare.NewStoredProvider(cloudflare.StoredConfig{Read: src.read, Source: "x", Now: clock})
	if _, err := p.Retrieve(context.Background()); err != nil {
		t.Fatal(err)
	}
	src.err = errors.New("throttled")
	c, err := p.Retrieve(context.Background())
	if err != nil || c.AccessKeyID != "k1" || !c.Expires.Equal(now.Add(10*time.Second)) {
		t.Fatalf("a failed read with a good credential held: %+v, %v", c, err)
	}
	// Past the margin the failure is the answer.
	now = now.Add(10*time.Minute - 10*time.Second)
	if _, err := p.Retrieve(context.Background()); err == nil || !strings.Contains(err.Error(), "throttled") {
		t.Fatalf("a failed read with the credential running out: %v", err)
	}
}

func TestStoredProviderReauthenticate(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	src := &source{doc: storedDoc("k1", now.Add(10*time.Minute))}
	p, _ := cloudflare.NewStoredProvider(cloudflare.StoredConfig{Read: src.read, Source: "x", Now: func() time.Time { return now }})
	cache := aws.NewCredentialsCache(p)
	if c, err := cache.Retrieve(context.Background()); err != nil || c.AccessKeyID != "k1" {
		t.Fatalf("%+v %v", c, err)
	}
	// The same document after a 403: nothing replaced, the request is not sent again.
	if replaced, err := p.Reauthenticate(context.Background()); err != nil || replaced {
		t.Fatalf("unchanged document: replaced=%v err=%v", replaced, err)
	}
	// Within MinReauth the document is not read again.
	src.doc = storedDoc("k2", now.Add(15*time.Minute))
	reads := src.reads
	if replaced, _ := p.Reauthenticate(context.Background()); replaced || src.reads != reads {
		t.Fatal("read again within MinReauth")
	}
	now = now.Add(cloudflare.MinReauth)
	if replaced, err := p.Reauthenticate(context.Background()); err != nil || !replaced {
		t.Fatalf("rotated document: replaced=%v err=%v", replaced, err)
	}
	cache.Invalidate()
	if c, _ := cache.Retrieve(context.Background()); c.AccessKeyID != "k2" {
		t.Fatalf("after a reauthentication: %s", c.AccessKeyID)
	}
}
