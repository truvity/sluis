package s3store_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"

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

func TestNothingIsRetriedUnlessItIsA403TheReauthAnsweredFor(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		err      error
		replaced bool
		rerr     error
		calls    int
	}{
		"a 500":                 {status(500), true, nil, 1},
		"a 404":                 {status(404), true, nil, 1},
		"a 403 not replaced":    {status(403), false, nil, 1},
		"a 403, reauth failing": {status(403), false, errors.New("cloudflare is down"), 1},
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
	// Without Reauth (static credentials) a 403 is never retried.
	f := &flaky{fake: &fake{}, errs: []error{status(403), status(403)}}
	s, err := s3store.New(f, s3store.Options{Bucket: "b", Lock: s3store.None})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Head(ctx, "k"); err == nil || f.calls != 1 {
		t.Errorf("static credentials: %v after %d calls", err, f.calls)
	}
}
