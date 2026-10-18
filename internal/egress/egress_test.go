package egress_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/truvity/sluis/internal/egress"
)

func TestPublicRefusesWhatAClientMustNotReach(t *testing.T) {
	for _, a := range []string{
		"127.0.0.1", "127.1.2.3", "::1", "0.0.0.0", "::",
		"10.0.0.5", "172.16.9.9", "192.168.1.1", "fd00::1", "100.64.0.1",
		"169.254.169.254", "169.254.170.2", "fe80::1", "fd00:ec2::254",
		"224.0.0.1", "ff02::1", "255.255.255.255", "240.0.0.1", "198.18.0.1",
		"::ffff:127.0.0.1", "::ffff:169.254.169.254", "::ffff:10.0.0.1",
		"64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", "2002:7f00:1::1", "2002:a9fe:a9fe::1",
	} {
		if !egress.Public.Blocked(netip.MustParseAddr(a)) {
			t.Errorf("%s is reachable", a)
		}
	}
	for _, a := range []string{"93.184.216.34", "8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "2001:4860:4860::8888", "::ffff:8.8.8.8", "64:ff9b::808:808"} {
		if egress.Public.Blocked(netip.MustParseAddr(a)) {
			t.Errorf("%s is refused", a)
		}
	}
}

// A destination an operator declared may be inside the cluster, but never the
// loopback interface or the metadata service.
func TestDeclaredAdmitsPrivateAndNothingElse(t *testing.T) {
	for _, a := range []string{"10.1.2.3", "192.168.0.7", "fd12::1", "100.64.3.3", "93.184.216.34"} {
		if egress.Declared.Blocked(netip.MustParseAddr(a)) {
			t.Errorf("%s is refused for a declared destination", a)
		}
	}
	for _, a := range []string{
		"127.0.0.1", "::1", "169.254.169.254", "fe80::1", "0.0.0.0", "224.0.0.1",
		"::ffff:169.254.169.254", "fd00:ec2::254", "100.100.100.200",
	} {
		if !egress.Declared.Blocked(netip.MustParseAddr(a)) {
			t.Errorf("%s is reachable for a declared destination", a)
		}
	}
}

// The check is of the address connected to, so a name that resolves to loopback
// is refused as the address is, and nothing reaches the server.
func TestTheTransportRefusesAServerOnLoopback(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	t.Cleanup(srv.Close)

	for _, host := range []string{srv.Listener.Addr().String(), "localhost:" + portOf(t, srv)} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+host+"/", nil)
		resp, err := (&http.Client{Transport: egress.Public.Transport()}).Do(req)
		if err == nil {
			_ = resp.Body.Close()
			t.Fatalf("%s: the request went through", host)
		}
		if !errors.Is(err, egress.ErrBlocked) {
			t.Errorf("%s: %v, want ErrBlocked", host, err)
		}
	}
	if hit {
		t.Fatal("the server was reached")
	}

	// And with the development policy it is reached.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/", nil)
	resp, err := (&http.Client{Transport: egress.Development.Transport()}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !hit {
		t.Error("the development policy did not reach it")
	}
}

func portOf(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}
