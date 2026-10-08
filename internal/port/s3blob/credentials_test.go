package s3blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type forbidden struct{}

func (forbidden) Error() string       { return "AccessDenied" }
func (forbidden) HTTPStatusCode() int { return 403 }

// flaky answers 403 until `good` is the key the credentials hold.
type flaky struct {
	API
	creds  *credentials
	good   string
	calls  int
	bodies []string
}

func (f *flaky) PutObject(ctx context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.calls++
	got, _ := io.ReadAll(in.Body)
	f.bodies = append(f.bodies, string(got))
	c, err := f.creds.cache.Retrieve(ctx)
	if err != nil {
		return nil, err
	}
	if c.AccessKeyID != f.good {
		return nil, forbidden{}
	}
	return &s3.PutObjectOutput{ETag: aws.String(`"e"`)}, nil
}

func TestA403ReadsTheCredentialsAgainAtMostOncePerMinute(t *testing.T) {
	now := time.Unix(1000, 0)
	key, reads := "old", 0
	read := func(context.Context) (Credentials, error) {
		reads++
		return Credentials{AccessKeyID: key, SecretAccessKey: "s"}, nil
	}
	creds, err := newCredentials(context.Background(), read, func() time.Time { return now })
	if err != nil || reads != 1 {
		t.Fatalf("start: %v, %d reads", err, reads)
	}
	f := &flaky{creds: creds, good: "new"}
	r := &reauth{API: f, creds: creds}
	put := func() error {
		_, err := r.PutObject(context.Background(), &s3.PutObjectInput{Body: bytes.NewReader([]byte("body"))})
		return err
	}

	// The key rotated, but the last read is fresh: no re-read, the 403 stands.
	key = "new"
	if err = put(); status(err) != 403 || reads != 1 || f.calls != 1 {
		t.Fatalf("within the minute: %v, %d reads, %d calls", err, reads, f.calls)
	}
	// A minute later the 403 re-reads the document and retries once, with the
	// whole body.
	now = now.Add(RefreshInterval + time.Second)
	if err = put(); err != nil || reads != 2 || f.calls != 3 {
		t.Fatalf("after the minute: %v, %d reads, %d calls", err, reads, f.calls)
	}
	if f.bodies[1] != "body" || f.bodies[2] != "body" {
		t.Fatalf("the retry lost the body: %q", f.bodies)
	}
	// A key that stays wrong is tried once per refresh, not in a loop.
	key, f.good = "other", "never"
	now = now.Add(2 * RefreshInterval)
	before := f.calls
	if err = put(); status(err) != 403 || f.calls != before+2 {
		t.Fatalf("a permanent 403: %v, %d calls", err, f.calls-before)
	}
}

func TestUnreadableCredentialsStopTheStartWithoutTheValue(t *testing.T) {
	_, err := New(context.Background(), Config{Bucket: "b", Endpoint: "http://127.0.0.1:1", Credentials: func(context.Context) (Credentials, error) {
		return Credentials{}, errors.New("no such document")
	}})
	if err == nil || !strings.Contains(err.Error(), "no such document") {
		t.Fatalf("New = %v", err)
	}
	_, err = New(context.Background(), Config{Bucket: "b", Endpoint: "http://127.0.0.1:1", Credentials: func(context.Context) (Credentials, error) {
		return Credentials{AccessKeyID: "id"}, nil
	}})
	if err == nil {
		t.Fatal("a half document was accepted")
	}
}
