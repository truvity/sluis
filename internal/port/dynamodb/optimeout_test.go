package dynamodb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ddb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/truvity/sluis/internal/port"
)

var errNeverCut = errors.New("never cut")

// hang waits for the context to end, or gives up after a bound of its own so a
// call that is never cut fails the test instead of hanging it.
func hang(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return errNeverCut
	}
}

// hangAPI never answers a Query or GetItem until its context ends.
type hangAPI struct {
	API
	queries int
}

func (h *hangAPI) GetItem(ctx context.Context, _ *ddb.GetItemInput, _ ...func(*ddb.Options)) (*ddb.GetItemOutput, error) {
	return nil, hang(ctx)
}

func (h *hangAPI) Query(ctx context.Context, _ *ddb.QueryInput, _ ...func(*ddb.Options)) (*ddb.QueryOutput, error) {
	h.queries++
	return nil, hang(ctx)
}

// slowAPI answers a Query or Scan one record at a time, each page after a delay.
type slowAPI struct {
	API
	delay time.Duration
	pages int
	held  []map[string]types.AttributeValue
	sent  int
}

func (a *slowAPI) Query(ctx context.Context, in *ddb.QueryInput, o ...func(*ddb.Options)) (*ddb.QueryOutput, error) {
	a.pages++
	select {
	case <-time.After(a.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	in.Limit = aws.Int32(1)
	return a.API.Query(ctx, in, o...)
}

func (a *slowAPI) Scan(ctx context.Context, in *ddb.ScanInput, o ...func(*ddb.Options)) (*ddb.ScanOutput, error) {
	a.pages++
	select {
	case <-time.After(a.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if len(in.ExclusiveStartKey) == 0 {
		out, err := a.API.Scan(ctx, in, o...)
		if err != nil {
			return nil, err
		}
		a.held = out.Items
	}
	n := len(a.held) - a.sent
	if n == 0 {
		return &ddb.ScanOutput{}, nil
	}
	out := &ddb.ScanOutput{Items: a.held[a.sent : a.sent+1]}
	a.sent++
	if a.sent < len(a.held) {
		out.LastEvaluatedKey = map[string]types.AttributeValue{"i": &types.AttributeValueMemberS{Value: "more"}}
	}
	return out, nil
}

func shortOpTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := opTimeout
	opTimeout = d
	t.Cleanup(func() { opTimeout = old })
}

// An operation is cut at opTimeout even when the caller's deadline is far off,
// as an AWS Lambda invocation's is.
func TestOperationCappedDespiteLongCallerDeadline(t *testing.T) {
	shortOpTimeout(t, 100*time.Millisecond)
	s := &Store{api: &hangAPI{API: newFake()}, table: "t", now: time.Now}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	for name, call := range map[string]func() error{
		"Get":     func() error { _, err := s.Get(ctx, "issuer:k"); return err },
		"Members": func() error { _, err := s.Members(ctx, "issuer:sso-clients:abc"); return err },
	} {
		start := time.Now()
		err := call()
		if err == nil {
			t.Fatalf("%s: no error from a call that never answers", name)
		}
		if d := time.Since(start); d > time.Second {
			t.Fatalf("%s: ran %v, want about the cap", name, d)
		}
		if errors.Is(err, errNeverCut) {
			t.Fatalf("%s: not cut at opTimeout", name)
		}
	}
}

// A caller deadline shorter than opTimeout still wins.
func TestShorterCallerDeadlineWins(t *testing.T) {
	shortOpTimeout(t, time.Hour)
	s := &Store{api: &hangAPI{API: newFake()}, table: "t", now: time.Now}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Get(ctx, "issuer:k")
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("err=%v after %v, want the caller's deadline to cut the call", err, time.Since(start))
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("the caller's deadline did not end the call")
	}
}

// The timeout is per page: a loop of pages each shorter than opTimeout is not
// cut by the sum.
func TestTimeoutIsPerPage(t *testing.T) {
	shortOpTimeout(t, 200*time.Millisecond)
	f := newFake()
	s := fakeStore(t, f)
	for i := 0; i < 5; i++ {
		if _, err := s.Put(context.Background(), "issuer:p/"+string(rune('a'+i)), []byte("v"), time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	s.api = &slowAPI{API: f, delay: 80 * time.Millisecond}
	n := 0
	err := s.ExportState(context.Background(), "issuer:p/", func(port.Exported) error { n++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("exported %d records, want 5", n)
	}
	if s.api.(*slowAPI).pages < 5 {
		t.Fatalf("%d pages: the loop did not run long enough to prove anything", s.api.(*slowAPI).pages)
	}
}
