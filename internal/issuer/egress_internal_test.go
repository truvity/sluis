package issuer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/truvity/sluis/internal/egress"
	"github.com/truvity/sluis/policy"
)

// The fetch of a client document connects under the production network policy:
// a document at an address on the loopback interface, which an allow-listed host
// name may resolve to, is not reached, and the refusal is the answer rather than
// a hiccup to retry.
func TestADocumentOnALoopbackAddressIsNeverFetched(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		goodDoc(w, r)
	}))
	t.Cleanup(srv.Close)

	resolver := newDocumentClients(policy.ClientDocuments{
		Origins: []string{strings.TrimPrefix(srv.URL, "https://")}, Requires: []string{"engineers"},
	})
	resolver.backoff = time.Millisecond

	_, err := resolver.Resolve(context.Background(), srv.URL+"/cimd.json")
	if !errors.Is(err, egress.ErrBlocked) {
		t.Fatalf("Resolve = %v, want the address refused", err)
	}
	if errors.Is(err, errTransient) {
		t.Error("a refused address was classed as transient, so it would be retried and could be answered from a stale copy")
	}
	if hits.Load() != 0 {
		t.Error("the server was reached")
	}
}

// A failed fetch is remembered for a minute and answered again unasked, so a
// caller that keeps presenting the same bad id does not make this process ask
// somebody else's server each time.
func TestAFailedDocumentIsNotFetchedAgainWithinTheMinute(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	resolver, srv := served(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	clock := time.Now()
	resolver.now = func() time.Time { return clock }
	id := srv.URL + "/cimd.json"

	for range 5 {
		if _, err := resolver.Resolve(context.Background(), id); err == nil {
			t.Fatal("a refused document was admitted")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("%d fetches in a minute, want 1", calls.Load())
	}
	clock = clock.Add(documentFailedFor + time.Second)
	_, _ = resolver.Resolve(context.Background(), id)
	if calls.Load() != 2 {
		t.Errorf("%d fetches after the minute, want 2", calls.Load())
	}
}

// One host serving documents at any number of paths is asked for a bounded
// number of them a minute, whatever the ids.
func TestOneHostIsFetchedFromAtABoundedRate(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	resolver, srv := served(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		goodDoc(w, r)
	}))
	clock := time.Now()
	resolver.now = func() time.Time { return clock }

	var throttled int
	for i := range documentHostRate + 10 {
		_, err := resolver.Resolve(context.Background(), fmt.Sprintf("%s/doc-%d.json", srv.URL, i))
		if errors.Is(err, errThrottled) {
			throttled++
		} else if err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}
	if throttled != 10 || calls.Load() != documentHostRate {
		t.Fatalf("throttled %d, fetched %d; want 10 and %d", throttled, calls.Load(), documentHostRate)
	}
	clock = clock.Add(documentHostWindow + time.Second)
	if _, err := resolver.Resolve(context.Background(), srv.URL+"/another.json"); err != nil {
		t.Errorf("after the window: %v", err)
	}
}

// The name a document gives itself is cleaned as a declared client's would be
// held: no bidirectional override, no formatting character, and a cut that does
// not split a character.
func TestADocumentNameIsCleanedAndCutOnACharacterBoundary(t *testing.T) {
	t.Parallel()

	target := mustParse(t, "https://clients.example/cimd.json")
	if got := documentName("Trusted \u202eBank\u202c App", target); strings.ContainsRune(got, '\u202e') || got != "Trusted Bank App" {
		t.Errorf("name = %q", got)
	}
	if got := documentName("\u202e\u200b", target); got != "clients.example" {
		t.Errorf("a name of nothing but formatting characters = %q, want the host", got)
	}
	got := documentName(strings.Repeat("\u4e16", documentNameMax*2), target)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != documentNameMax {
		t.Errorf("a cut name is %d characters, valid=%v; want %d whole characters", utf8.RuneCountInString(got), utf8.ValidString(got), documentNameMax)
	}
}

// A back-channel logout is posted to the address the policy declares and
// nowhere else: a redirect is a failure, and the address it redirects to is not
// followed.
func TestABackChannelRedirectIsRefused(t *testing.T) {
	t.Parallel()

	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	t.Cleanup(other.Close)
	declared := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(declared.Close)

	s := &Storage{announce: newAnnounceClient(egress.Development)}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, declared.URL, strings.NewReader("logout_token=x"))
	_, err := s.announce.Do(req)
	if !errors.Is(err, errBackChannelRedirected) {
		t.Fatalf("a redirect = %v", err)
	}
	if elsewhere.Load() != 0 {
		t.Error("the redirect was followed")
	}
}

// A declared address that resolves to loopback or the metadata service is not
// connected to; one in a private range is, since the policy may declare a service
// in the cluster.
func TestABackChannelAddressOnLoopbackIsNotConnectedTo(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)

	client := newAnnounceClient(egress.Declared)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader("logout_token=x"))
	if _, err := client.Do(req); !errors.Is(err, egress.ErrBlocked) {
		t.Fatalf("loopback = %v", err)
	}
	if hits.Load() != 0 {
		t.Error("the server on loopback was reached")
	}
	if !egress.Declared.Blocked(mustAddr(t, "169.254.169.254")) || egress.Declared.Blocked(mustAddr(t, "10.1.2.3")) {
		t.Error("the declared policy does not refuse the metadata service and admit a private address")
	}
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// `allowInsecure` is reachable from a production document, so it does not by
// itself lift the guards against loopback and the metadata service: the issuer
// URL has to be on loopback too.
func TestAllowInsecureLiftsTheEgressGuardsOnlyForALoopbackIssuer(t *testing.T) {
	t.Parallel()

	iss := func(url string, insecure bool) *Issuer {
		return &Issuer{cfg: Config{URL: url, AllowInsecure: insecure}}
	}
	cases := []struct {
		name string
		iss  *Issuer
		dev  bool
	}{
		{"loopback and insecure", iss("http://127.0.0.1:8080", true), true},
		{"localhost and insecure", iss("http://localhost:8080", true), true},
		{"ipv6 loopback and insecure", iss("http://[::1]:8080", true), true},
		{"a real host and insecure", iss("http://access.example.com", true), false},
		{"a private address and insecure", iss("http://10.0.0.5", true), false},
		{"loopback, not insecure", iss("http://127.0.0.1:8080", false), false},
		{"production", iss("https://access.example.com", false), false},
		{"a host that only starts with localhost", iss("http://localhost.evil.example", true), false},
	}
	for _, c := range cases {
		if got := egressFor(c.iss, egress.Public) == egress.Development; got != c.dev {
			t.Errorf("%s: development policy = %v, want %v", c.name, got, c.dev)
		}
	}
}
