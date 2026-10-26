package s3store_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/truvity/sluis/audit/store"
	"github.com/truvity/sluis/audit/store/s3store"
)

func status(code int) error {
	return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: code}},
		Err:      errors.New("refused"),
	}}
}

// flaky answers each request with the next of its errors, then succeeds.
type flaky struct {
	*fake
	errs  []error
	calls int
	// bodies are what each put carried when it arrived.
	bodies []string
}

func (f *flaky) next() error {
	f.calls++
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		return err
	}
	return nil
}

func (f *flaky) PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b, _ := io.ReadAll(in.Body)
	f.bodies = append(f.bodies, string(b))
	if err := f.next(); err != nil {
		return nil, err
	}
	rewound := strings.NewReader(string(b))
	in.Body = rewound
	return f.fake.PutObject(ctx, in, opts...)
}

func (f *flaky) HeadObject(ctx context.Context, in *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if err := f.next(); err != nil {
		return nil, err
	}
	return f.fake.HeadObject(ctx, in, opts...)
}

func newReauthStore(t *testing.T, f *flaky, reauth s3store.ReauthFunc) *s3store.Store {
	t.Helper()
	s, err := s3store.New(f, s3store.Options{Bucket: "b", Lock: s3store.None, Reauth: reauth})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A 403 is sent once more after the credentials were replaced, with the body
// from its start, and never a second time.
func TestAForbiddenRequestIsSentOnceMoreAfterTheCredentialsAreReplaced(t *testing.T) {
	ctx := context.Background()
	f := &flaky{fake: &fake{}, errs: []error{status(403)}}
	reauths := 0
	s := newReauthStore(t, f, func(context.Context) (bool, error) { reauths++; return true, nil })
	if err := s.Put(ctx, object()); err != nil {
		t.Fatalf("the retried put failed: %v", err)
	}
	if reauths != 1 || f.calls != 2 || len(f.bodies) != 2 || f.bodies[0] != f.bodies[1] || f.bodies[1] == "" {
		t.Errorf("reauths %d, calls %d, bodies %q", reauths, f.calls, f.bodies)
	}

	// Still forbidden: one retry, then the error.
	f = &flaky{fake: &fake{}, errs: []error{status(403), status(403), status(403)}}
	reauths = 0
	s = newReauthStore(t, f, func(context.Context) (bool, error) { reauths++; return true, nil })
	if err := s.Put(ctx, object()); err == nil {
		t.Fatal("two 403s were swallowed")
	}
	if reauths != 1 || f.calls != 2 {
		t.Errorf("a request was sent %d times after %d reauths, want 2 and 1", f.calls, reauths)
	}
}

// logs captures what the default logger writes during the test.
func logs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// An S3-compatible store can answer 401 rather than 403 for a key that was
// rotated away. That is sent once more after the credentials were replaced,
// like a 403, and the reauth is logged with its status.
func TestAnUnauthorizedRequestIsSentOnceMoreAfterTheCredentialsAreReplaced(t *testing.T) {
	ctx := context.Background()
	out := logs(t)
	f := &flaky{fake: &fake{}, errs: []error{status(401)}}
	reauths := 0
	s := newReauthStore(t, f, func(context.Context) (bool, error) { reauths++; return true, nil })
	if err := s.Put(ctx, object()); err != nil {
		t.Fatalf("the retried put failed: %v", err)
	}
	if reauths != 1 || f.calls != 2 || len(f.bodies) != 2 || f.bodies[0] != f.bodies[1] || f.bodies[1] == "" {
		t.Errorf("reauths %d, calls %d, bodies %q", reauths, f.calls, f.bodies)
	}
	for _, want := range []string{"reauthenticated", "status=401", "operation=PutObject", "replaced=true"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the log lacks %q: %s", want, out)
		}
	}
	if strings.Contains(out.String(), "failed again") {
		t.Errorf("a retry that succeeded was logged as failed: %s", out)
	}

	// A head (the seal probe) the same way.
	f = &flaky{fake: &fake{}, errs: []error{status(401)}}
	reauths = 0
	s = newReauthStore(t, f, func(context.Context) (bool, error) { reauths++; return true, nil })
	if _, err := s.Head(ctx, "k"); err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the retried head failed: %v", err)
	}
	if reauths != 1 || f.calls != 2 {
		t.Errorf("head: reauths %d, calls %d", reauths, f.calls)
	}
}

// A store that answers 401 again after the reauth gets one retry, not a loop,
// and the error is returned.
func TestAnUnauthorizedRequestIsRetriedOnlyOnce(t *testing.T) {
	ctx := context.Background()
	out := logs(t)
	f := &flaky{fake: &fake{}, errs: []error{status(401), status(401), status(401)}}
	reauths := 0
	s := newReauthStore(t, f, func(context.Context) (bool, error) { reauths++; return true, nil })
	if err := s.Put(ctx, object()); err == nil {
		t.Fatal("two 401s were swallowed")
	}
	if reauths != 1 || f.calls != 2 {
		t.Errorf("a request was sent %d times after %d reauths, want 2 and 1", f.calls, reauths)
	}
	if !strings.Contains(out.String(), "failed again") || !strings.Contains(out.String(), "status=401") {
		t.Errorf("the failed retry was not logged with its status: %s", out)
	}
}

func TestNothingIsRetriedUnlessItIsA403TheReauthAnsweredFor(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		err      error
		replaced bool
		rerr     error
		calls    int
	}{
		"a 500":                 {status(500), true, nil, 1},
		"a 503":                 {status(503), true, nil, 1},
		"a 400":                 {status(400), true, nil, 1},
		"a 404":                 {status(404), true, nil, 1},
		"a 429":                 {status(429), true, nil, 1},
		"not an HTTP error":     {errors.New("connection reset"), true, nil, 1},
		"a 403 not replaced":    {status(403), false, nil, 1},
		"a 403, reauth failing": {status(403), false, errors.New("cloudflare is down"), 1},
		"a 401 not replaced":    {status(401), false, nil, 1},
		"a 401, reauth failing": {status(401), false, errors.New("cloudflare is down"), 1},
	} {
		f := &flaky{fake: &fake{}, errs: []error{tc.err, tc.err}}
		s := newReauthStore(t, f, func(context.Context) (bool, error) { return tc.replaced, tc.rerr })
		if _, err := s.Head(ctx, "k"); err == nil {
			t.Errorf("%s: no error", name)
		}
		if f.calls != tc.calls {
			t.Errorf("%s: sent %d times, want %d", name, f.calls, tc.calls)
		}
	}
	// Without Reauth (static credentials) a 401 or 403 is never retried.
	for _, code := range []int{401, 403} {
		f := &flaky{fake: &fake{}, errs: []error{status(code), status(code)}}
		s, err := s3store.New(f, s3store.Options{Bucket: "b", Lock: s3store.None})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Head(ctx, "k"); err == nil || f.calls != 1 {
			t.Errorf("static credentials, %d: %v after %d calls", code, err, f.calls)
		}
	}
}
