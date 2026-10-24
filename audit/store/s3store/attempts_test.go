package s3store_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/truvity/sluis/audit/store"
	"github.com/truvity/sluis/audit/store/s3store"
)

// A request that stalls is the one that decides how long a writer takes to
// start: the writer's start-up is a chain of puts, and a function platform
// ends a start-up that runs past its limit. With AttemptTimeout an attempt
// that gets no answer fails and is tried again, instead of holding the call.

// stalling answers each request in turn: the first `stalls` requests get no
// answer until the client gives up, the rest are answered at once with the
// status given.
type stalling struct {
	stalls   int64
	status   int
	requests atomic.Int64
}

func (s *stalling) handler(rw http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	if n := s.requests.Add(1); n <= s.stalls {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
		return
	}
	rw.WriteHeader(s.status)
}

func openBounded(t *testing.T, s *stalling, o s3store.Options) *s3store.Store {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	o.Bucket, o.Endpoint, o.PathStyle, o.Lock = "archive", srv.URL, true, s3store.None
	st, err := s3store.FromConfig(aws.Config{
		Region:      s3store.AutoRegion,
		Credentials: credentials.NewStaticCredentialsProvider("key", "secret", ""),
	}, o)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestAStalledAttemptIsTimedOutAndRetried(t *testing.T) {
	s := &stalling{stalls: 1, status: http.StatusOK}
	st := openBounded(t, s, s3store.Options{AttemptTimeout: 200 * time.Millisecond, MaxAttempts: 3, MaxBackoff: 10 * time.Millisecond})

	began := time.Now()
	err := st.Put(context.Background(), store.Object{Key: "schema/wide/1.0.0/a.json", Body: []byte("{}")})
	took := time.Since(began)
	if err != nil {
		t.Fatalf("a put whose first attempt stalled: %v", err)
	}
	if got := s.requests.Load(); got != 2 {
		t.Fatalf("the store saw %d requests, want the stalled one and its retry", got)
	}
	if took > 3*time.Second {
		t.Fatalf("the put took %s: the stalled attempt was not cut off", took)
	}
}

// A retried put whose first attempt did land finds its key taken: that is
// ErrExists, which the writer counts as the object being there.
func TestARetriedPutThatFindsItsKeyTakenIsErrExists(t *testing.T) {
	s := &stalling{stalls: 1, status: http.StatusPreconditionFailed}
	st := openBounded(t, s, s3store.Options{AttemptTimeout: 200 * time.Millisecond, MaxAttempts: 3, MaxBackoff: 10 * time.Millisecond})

	err := st.Put(context.Background(), store.Object{Key: "schema/wide/1.0.0/a.json", Body: []byte("{}")})
	if !errors.Is(err, store.ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
}

// A store that never answers fails the call after MaxAttempts, in about
// MaxAttempts times the bound, rather than waiting for the connection.
func TestAStoreThatNeverAnswersFailsWithinTheBound(t *testing.T) {
	s := &stalling{stalls: 1 << 30, status: http.StatusOK}
	st := openBounded(t, s, s3store.Options{AttemptTimeout: 200 * time.Millisecond, MaxAttempts: 3, MaxBackoff: 10 * time.Millisecond})

	began := time.Now()
	err := st.Put(context.Background(), store.Object{Key: "schema/wide/1.0.0/a.json", Body: []byte("{}")})
	took := time.Since(began)
	if err == nil {
		t.Fatal("a put to a store that never answers succeeded")
	}
	if got := s.requests.Load(); got != 3 {
		t.Fatalf("the store saw %d attempts, want MaxAttempts (3)", got)
	}
	if took > 3*time.Second {
		t.Fatalf("the put took %s, want about 3 x 200ms", took)
	}
}
