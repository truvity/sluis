package s3blob_test

import (
	"context"
	"crypto/md5" //nolint:gosec // Content-MD5 is the protocol's header
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/truvity/sluis/internal/port/s3blob"
)

type seen struct {
	header http.Header
	body   []byte
}

// wire runs the REAL SDK client against an httptest server and returns what
// the server received for the one PutObject.
func wire(t *testing.T, checksum string, body []byte) seen {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "k")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "s")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	var mu sync.Mutex
	var got seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = seen{header: r.Header.Clone(), body: b}
		mu.Unlock()
		w.Header().Set("ETag", `"x"`)
	}))
	t.Cleanup(srv.Close)
	b, err := s3blob.New(context.Background(), s3blob.Config{Bucket: "b", Region: "us-east-1", Endpoint: srv.URL, PathStyle: true, Checksum: checksum})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write(context.Background(), "k", body); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	return got
}

func assertNotChunked(t *testing.T, s seen) {
	t.Helper()
	if strings.Contains(s.header.Get("Content-Encoding"), "aws-chunked") || s.header.Get("X-Amz-Trailer") != "" ||
		strings.Contains(s.header.Get("X-Amz-Content-Sha256"), "STREAMING") {
		t.Errorf("the request is aws-chunked: %v", s.header)
	}
}

func TestPutCarriesAPrecomputedSHA256(t *testing.T) {
	body := []byte("the archive")
	s := wire(t, "", body)
	assertNotChunked(t, s)
	sum := sha256.Sum256(body)
	want := base64.StdEncoding.EncodeToString(sum[:])
	if got := s.header.Get("X-Amz-Checksum-Sha256"); got != want {
		t.Fatalf("x-amz-checksum-sha256 = %q, want %q", got, want)
	}
	if string(s.body) != string(body) {
		t.Errorf("body = %q", s.body)
	}
	// The header binds the bytes: a different body has a different hash, so a
	// store verifying it would refuse a body altered in flight.
	other := sha256.Sum256([]byte("another archive"))
	if base64.StdEncoding.EncodeToString(other[:]) == want {
		t.Fatal("the hash does not depend on the body")
	}
}

func TestMD5ChecksumSendsContentMD5(t *testing.T) {
	body := []byte("live blob")
	s := wire(t, "md5", body)
	assertNotChunked(t, s)
	sum := md5.Sum(body) //nolint:gosec // see above
	if got := s.header.Get("Content-Md5"); got != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Errorf("Content-MD5 = %q", got)
	}
	if s.header.Get("X-Amz-Checksum-Sha256") != "" {
		t.Error("md5 also sent a sha256")
	}
}

func TestNoneSendsNoChecksum(t *testing.T) {
	s := wire(t, "none", []byte("x"))
	assertNotChunked(t, s)
	if s.header.Get("X-Amz-Checksum-Sha256") != "" || s.header.Get("Content-Md5") != "" {
		t.Errorf("none sent a checksum: %v", s.header)
	}
}

func TestAnUnknownChecksumIsRefused(t *testing.T) {
	if _, err := s3blob.NewWithAPI(newFake(), s3blob.Config{Bucket: "b", Checksum: "crc32"}); err == nil {
		t.Fatal("crc32 was accepted")
	}
}
