package issuer_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// announceStorage is a Storage over a policy whose clients c0..c(n-1) each
// ask to be told at base + "/c<i>".
func announceStorage(t *testing.T, base string, n int) *issuer.Storage {
	t.Helper()

	var sb strings.Builder

	sb.WriteString("version: 1\nlifetimes:\n  default: 1h\nclients:\n")

	for i := range n {
		fmt.Fprintf(&sb, "  c%d:\n    kind: public\n    redirects: [\"http://localhost:8000/callback\"]\n"+
			"    requires: [\"all:everyone\"]\n    backchannel_logout_uri: %s/c%d\n", i, base, i)
	}

	sb.WriteString("groups:\n  all:everyone:\n    matchers:\n      - email: ada@north.example\n")

	declared, err := policy.Parse([]byte(sb.String()))
	if err != nil {
		t.Fatal(err)
	}

	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}

	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true},
		set, &fakeDirectory{}, issuer.NewMemoryState())

	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	return storage
}

func sessionsOf(n int) []issuer.Session {
	out := make([]issuer.Session, n)
	for i := range out {
		out[i] = issuer.Session{ClientID: fmt.Sprintf("c%d", i), Identity: "ada@north.example", SSO: "s"}
	}

	return out
}

// Slow clients are told together: the wait is about one client's, not the
// sum of them.
func TestLogoutTokensAreDeliveredConcurrently(t *testing.T) {
	t.Parallel()

	const (
		clients = 6
		slow    = 300 * time.Millisecond
	)

	var arrived atomic.Int32

	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(slow)
		arrived.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(listener.Close)

	storage := announceStorage(t, listener.URL, clients)

	start := time.Now()
	storage.AnnounceLogoutForTest(context.Background(), sessionsOf(clients))
	took := time.Since(start)

	if got := arrived.Load(); got != clients {
		t.Errorf("%d of %d clients were told before it returned", got, clients)
	}

	// Sequentially this is clients*slow = 1.8 s.
	if took > 3*slow {
		t.Errorf("took %v for %d clients each slow by %v: not concurrent", took, clients, slow)
	}
}

// A client that refuses does not stop the others being told.
func TestAFailingClientDoesNotStopTheOthersBeingTold(t *testing.T) {
	t.Parallel()

	var told atomic.Int32

	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/c1" {
			w.WriteHeader(http.StatusInternalServerError)

			return
		}

		told.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(listener.Close)

	storage := announceStorage(t, listener.URL, 4)
	storage.AnnounceLogoutForTest(context.Background(), sessionsOf(4))

	if got := told.Load(); got != 3 {
		t.Errorf("%d clients were told, want the 3 that answered", got)
	}
}
