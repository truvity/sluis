// Package egress keeps an outbound request to an address the caller chose from
// reaching an address the caller must not.
//
// A client document is fetched from a URL the client presented, and a
// back-channel logout token is posted to one the policy declares. Neither
// is a host this process vouches for, and a host name is only a promise about
// what it resolves to: it may resolve to the instance metadata service, to the
// loopback interface, or to a private address inside the network the process is
// in, and it may resolve to a public address when checked and to a private one
// when connected to (DNS rebinding). So the check is made on the address the
// socket is actually connecting to, after resolution, not on the name before it.
package egress

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrBlocked is a connection refused because of the address it was about to
// reach.
var ErrBlocked = errors.New("egress: the address is not one this process may connect to")

// Policy says which addresses a connection may not reach.
type Policy struct {
	// AllowPrivate admits the private ranges (RFC 1918, unique-local IPv6,
	// carrier-grade NAT) and nothing else of what is refused: it is for a
	// destination an operator declared, which may well be a service in the
	// cluster. Loopback, link-local (which holds the instance metadata
	// services), unspecified, multicast and the reserved ranges are refused
	// either way.
	AllowPrivate bool
	// AllowAll admits every address. It is for a development installation
	// (`allowInsecure`) that talks to its own loopback, and for tests.
	AllowAll bool
}

// Public refuses everything that is not a public unicast address: the policy
// for a destination a client chose.
var Public = Policy{}

// Declared admits private addresses as well: the policy for a destination an
// operator declared.
var Declared = Policy{AllowPrivate: true}

// Development admits every address.
var Development = Policy{AllowAll: true}

var (
	// 64:ff9b::/96 is NAT64: the last four bytes are an IPv4 address that the
	// gateway connects to.
	nat64 = netip.MustParsePrefix("64:ff9b::/96")
	// 2002::/16 is 6to4: bytes 2 to 5 are an IPv4 address.
	sixToFour = netip.MustParsePrefix("2002::/16")
	reserved  = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),     // "this network"
		netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
		netip.MustParsePrefix("192.0.2.0/24"),  // documentation
		netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"), // reserved, and the broadcast address
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("100::/64"), // discard-only
		// Metadata services that sit in ranges an operator may declare: AWS's
		// IPv6 one in unique-local space, Alibaba's in carrier-grade NAT space.
		netip.MustParsePrefix("fd00:ec2::/32"),
		netip.MustParsePrefix("100.100.100.200/32"),
	}
	carrierNAT = netip.MustParsePrefix("100.64.0.0/10")
)

// Blocked reports whether the policy refuses a connection to ip.
func (p Policy) Blocked(ip netip.Addr) bool {
	if p.AllowAll {
		return false
	}
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() {
		return true
	}
	// An address that embeds an IPv4 one is judged by the one it embeds.
	if ip.Is6() {
		b := ip.As16()
		switch {
		case nat64.Contains(ip):
			return p.Blocked(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		case sixToFour.Contains(ip):
			return p.Blocked(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
		}
	}
	for _, r := range reserved {
		if r.Contains(ip) {
			return true
		}
	}
	switch {
	case ip.IsLoopback(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip.IsInterfaceLocalMulticast(),
		ip.IsMulticast(), ip.IsUnspecified():
		return true
	case ip.IsPrivate(), carrierNAT.Contains(ip):
		return !p.AllowPrivate
	}
	return false
}

// Control is a net.Dialer's ControlContext hook: it runs after the name is
// resolved, with the address the socket is about to connect to, once for each
// address tried. A refused address fails the dial.
func (p Policy) Control(network, address string, _ syscall.RawConn) error {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return fmt.Errorf("%w: %s is not a TCP connection", ErrBlocked, network)
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %q is not an address", ErrBlocked, address)
	}
	if p.Blocked(ap.Addr()) {
		return fmt.Errorf("%w: %s", ErrBlocked, ap.Addr())
	}
	return nil
}

// Transport is an HTTP transport that connects only where the policy allows.
//
// It uses no proxy from the environment: with one, the address the socket
// reaches is the proxy's, and the check would be of the wrong host.
func (p Policy) Transport() *http.Transport {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second, Control: p.Control}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
}
