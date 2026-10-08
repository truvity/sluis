package s3store_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/truvity/sluis/audit/store"
	"github.com/truvity/sluis/audit/store/s3store"
)

// An S3-compatible endpoint (Cloudflare R2 is the one measured) is addressed
// path-style, signs with the region "auto", takes no Object Lock, and refuses
// the SDK's own checksum when the object carries a Content-Encoding. These
// tests put a compressed object through the real SDK client to a local server
// and read what went on the wire, which is where the store's claim lives; the
// same claim against a real store is internal/s3test's CompatibleStore check.

// wire is the last request the server saw.
type wire struct {
	mu     sync.Mutex
	method string
	path   string
	header http.Header
	body   []byte
}

func (w *wire) handler(rw http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	w.mu.Lock()
	w.method, w.path, w.header, w.body = r.Method, r.URL.Path, r.Header.Clone(), b
	w.mu.Unlock()
	rw.WriteHeader(http.StatusOK)
}

func openCompatible(t *testing.T, lock s3store.LockMode) (*s3store.Store, *wire, string) {
	t.Helper()
	w := &wire{}
	srv := httptest.NewServer(http.HandlerFunc(w.handler))
	t.Cleanup(srv.Close)
	cfg := aws.Config{
		Region:      s3store.AutoRegion,
		Credentials: credentials.NewStaticCredentialsProvider("key", "secret", ""),
	}
	s, err := s3store.FromConfig(cfg, s3store.Options{
		Bucket: "archive", Prefix: "audit", Endpoint: srv.URL, PathStyle: true, Lock: lock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, w, srv.URL
}

func TestCompressedPutOnACompatibleEndpointSendsTheChecksumValueOnly(t *testing.T) {
	s, w, _ := openCompatible(t, s3store.None)
	body := []byte("a compressed record")
	err := s.Put(context.Background(), store.Object{
		Key: "records/security/acme/one", Body: body, ContentType: "application/x-ndjson", Encoding: "zstd",
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if w.method != http.MethodPut || w.path != "/archive/audit/records/security/acme/one" {
		t.Fatalf("%s %s: want a path-style put of the prefixed key", w.method, w.path)
	}
	if got, want := w.header.Get("X-Amz-Checksum-Sha256"), checksumOf(body); got != want {
		t.Fatalf("x-amz-checksum-sha256 = %q, want the precomputed %q", got, want)
	}
	// With the algorithm asked of the SDK it would send the checksum as an
	// aws-chunked trailer and sign it differently, which R2 answers with 403.
	for _, h := range []string{"X-Amz-Sdk-Checksum-Algorithm", "X-Amz-Trailer", "X-Amz-Decoded-Content-Length"} {
		if v := w.header.Get(h); v != "" {
			t.Errorf("%s = %q: the SDK chose a checksum algorithm, which R2 signs differently", h, v)
		}
	}
	if ce := w.header.Get("Content-Encoding"); ce != "zstd" {
		t.Errorf("content-encoding = %q, want zstd alone (no aws-chunked)", ce)
	}
	if string(w.body) != string(body) {
		t.Errorf("body = %q, want it unframed", w.body)
	}
	for h := range w.header {
		if strings.HasPrefix(strings.ToLower(h), "x-amz-object-lock") {
			t.Errorf("%s sent to a store without Object Lock", h)
		}
	}
	// The region is "auto" in the credential scope.
	if auth := w.header.Get("Authorization"); !strings.Contains(auth, "/auto/s3/aws4_request") {
		t.Errorf("authorization %q does not sign for the region auto", auth)
	}
}

func TestObjectLockIsRefusedOnACompatibleEndpoint(t *testing.T) {
	for _, mode := range []s3store.LockMode{s3store.Compliance, s3store.Governance, ""} {
		_, err := s3store.New(&fake{}, s3store.Options{Bucket: "b", Endpoint: "https://x.example.test", Lock: mode})
		if err == nil || !strings.Contains(err.Error(), "S3-compatible endpoint") {
			t.Errorf("lock %q: err = %v, want the lock refused on an endpoint", mode, err)
		}
	}
	if _, err := s3store.New(&fake{}, s3store.Options{Bucket: "b", Endpoint: "https://x.example.test", Lock: s3store.None}); err != nil {
		t.Errorf("lock none on an endpoint: %v", err)
	}
	// AWS (no endpoint) keeps every mode.
	if _, err := s3store.New(&fake{}, s3store.Options{Bucket: "b", Lock: s3store.Compliance}); err != nil {
		t.Errorf("compliance on AWS: %v", err)
	}
}
