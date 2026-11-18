//go:build sluis_test

package keyd

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	configv2alpha1 "github.com/truvity/sluis/v2/gen/go/sluis/config/v2alpha1"
	keyv2alpha1 "github.com/truvity/sluis/v2/gen/go/sluis/key/v2alpha1"
)

// verifies: REQ-PKI-057
// The token certificate is off unless the Boundary turns it on (decision X1 A): an installation that has an intermediate in its
// plan and no switch gives no chain, answers ca with nothing to certify, and signs and keeps no certificate. The refusal is the
// one of a purpose the installation does not enable.
func TestAnInstallationThatLeavesTheTokenCertificateOffGivesNoChainAndSignsNoCertificate(t *testing.T) {
	n := handoffNode(t, nil)
	if n.srv.tokenCertificate {
		t.Fatal("the switch is on without the Boundary asking for it")
	}
	if r := publicKeyOf(t, n); len(r.GetX5C()) != 0 || r.GetX5CWithdrawn() {
		t.Fatalf("PublicKey: %d certificates, withdrawn %v", len(r.GetX5C()), r.GetX5CWithdrawn())
	}
	res, err := n.client("ca").TokenKey(context.Background(), connect.NewRequest(&keyv2alpha1.TokenKeyRequest{}))
	if err != nil || len(res.Msg.GetSpkiDer()) != 0 || len(res.Msg.GetChain()) != 0 || len(res.Msg.GetIntermediateSpkiDer()) != 0 {
		t.Fatalf("TokenKey: %+v, %v: nothing is to be certified, and nothing is refused", res, err)
	}
	// Neither the signature of a certificate nor the keeping of one is allowed: the rule that refuses them is the switch, before
	// the content is looked at.
	_, err = signTBS(n, "ca", []byte{0x30, 0x00})
	wantRefusal(t, err, connect.CodeFailedPrecondition, "purpose")
	_, err = putCertificate(n, "ca", "token-signing", []byte{0x30, 0x00})
	wantRefusal(t, err, connect.CodeFailedPrecondition, "purpose")
}

// verifies: REQ-PKI-057
// A certificate that was stored while the switch was on is not given once it is off, and key@ says that it withdraws it, so
// that the key set drops the chain it carries. With the switch on again the chain is given as before.
func TestTurningTheTokenCertificateOffWithdrawsTheStoredChain(t *testing.T) {
	n, _, inter := certifiedNode(t)
	certifyToken(t, n, inter)
	if r := publicKeyOf(t, n); len(r.GetX5C()) != 2 || r.GetX5CWithdrawn() {
		t.Fatalf("on: %d certificates, withdrawn %v", len(r.GetX5C()), r.GetX5CWithdrawn())
	}
	e := n.e
	off := proto.Clone(e.boundary).(*configv2alpha1.Boundary)
	off.TokenCertificate = false
	e.putBoundary(off)
	restarted := e.startedNode(nodeOpts{name: "node-2", driver: n.driver})
	if restarted.srv.tokenCertificate {
		t.Fatal("the restarted key@ has the switch on")
	}
	if r := publicKeyOf(t, restarted); len(r.GetX5C()) != 0 || !r.GetX5CWithdrawn() {
		t.Fatalf("off: %d certificates, withdrawn %v", len(r.GetX5C()), r.GetX5CWithdrawn())
	}
	on := proto.Clone(e.boundary).(*configv2alpha1.Boundary)
	on.TokenCertificate = true
	e.putBoundary(on)
	again := e.startedNode(nodeOpts{name: "node-3", driver: n.driver})
	if r := publicKeyOf(t, again); len(r.GetX5C()) != 2 || r.GetX5CWithdrawn() {
		t.Fatalf("on again: %d certificates, withdrawn %v", len(r.GetX5C()), r.GetX5CWithdrawn())
	}
}
