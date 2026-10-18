package srp

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 5054 Appendix B is a SHA-1 vector
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"testing"
)

// The test vectors of RFC 5054 Appendix B: I = "alice", P = "password123", the
// 1024-bit group of Appendix A with g = 2, and SHA-1. The same code runs them
// as runs the 3072-bit SHA-256 default, through [Params].
const (
	rfcSalt = "BEB25379D1A8581EB5A727673A2441EE"
	rfcK    = "7556AA045AEF2CDD07ABAF0F665C3E818913186F"
	rfcX    = "94B7555AABE9127CC58CCF4993DB6CF84D16C124"
	rfcA    = "60975527035CF2AD1989806F0407210BC81EDC04E2762A56AFD529DDDA2D4393"
	rfcB    = "E487CB59D31AC550471E81F00F6928E01DDA08E974A004F49E61F5D105284D20"
	rfcU    = "CE38B9593487DA98554ED47D70A7AE5F462EF019"
)

const rfcN = "" +
	"EEAF0AB9ADB38DD69C33F80AFA8FC5E86072618775FF3C0B9EA2314C9C256576" +
	"D674DF7496EA81D3383B4813D692C6E0E0D5D8E250B98BE48E495C1D6089DAD1" +
	"5DC7D7B46154D6B6CE8EF4AD69B15D4982559B297BCF1885C529F566660E57EC" +
	"68EDBC3C05726CC02FD4CBF4976EAA9AFD5138FE8376435B9FC61D2FC0EB06E3"

const rfcV = "" +
	"7E273DE8696FFC4F4E337D05B4B375BEB0DDE1569E8FA00A9886D8129BADA1F1" +
	"822223CA1A605B530E379BA4729FDC59F105B4787E5186F5C671085A1447B52A" +
	"48CF1970B4FB6F8400BBF4CEBFBB168152E08AB5EA53D15C1AFF87B2B9DA6E04" +
	"E058AD51CC72BFC9033B564E26480D78E955A5E29E7AB245DB2BE315E2099AFB"

const rfcPubA = "" +
	"61D5E490F6F1B79547B0704C436F523DD0E560F0C64115BB72557EC44352E890" +
	"3211C04692272D8B2D1A5358A2CF1B6E0BFCF99F921530EC8E39356179EAE45E" +
	"42BA92AEACED825171E1E8B9AF6D9C03E1327F44BE087EF06530E69F66615261" +
	"EEF54073CA11CF5858F0EDFDFE15EFEAB349EF5D76988A3672FAC47B0769447B"

const rfcPubB = "" +
	"BD0C61512C692C0CB6D041FA01BB152D4916A1E77AF46AE105393011BAF38964" +
	"DC46A0670DD125B95A981652236F99D9B681CBF87837EC996C6DA04453728610" +
	"D0C6DDB58B318885D7D82C7F8DEB75CE7BD4FBAA37089E6F9C6059F388838E7A" +
	"00030B331EB76840910440B1B27AAEAEEB4012B7D7665238A8E3FB004B117B58"

const rfcPremaster = "" +
	"B0DC82BABCF30674AE450C0287745E7990A3381F63B387AAF271A10D233861E3" +
	"59B48220F7C4693C9AE12B0A6F67809F0876E2D013800D6C41BB59B6D5979B5C" +
	"00A172B4A2A5903A0BDCAF8A709585EB2AFAFA8F3499B200210DCC1F10EB3394" +
	"3CD67FC88A2F39A4BE5BEC4EC0A3212DC346D7E474B29EDE8A469FFECA686E5A"

func bigHex(t *testing.T, s string) *big.Int {
	t.Helper()
	v, ok := new(big.Int).SetString(s, 16)
	if !ok {
		t.Fatalf("not hex: %q", s)
	}
	return v
}

func rfcParams(t *testing.T) Params {
	t.Helper()
	return New(bigHex(t, rfcN), big.NewInt(2), sha1.New) //nolint:gosec // see above
}

func TestRFC5054AppendixBVectors(t *testing.T) {
	p := rfcParams(t)
	salt, err := hex.DecodeString(rfcSalt)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := p.k(), bigHex(t, rfcK); got.Cmp(want) != 0 {
		t.Errorf("k = %x, want %x", got, want)
	}
	x := p.x(salt, "alice", "password123")
	if want := bigHex(t, rfcX); x.Cmp(want) != 0 {
		t.Errorf("x = %x, want %x", x, want)
	}
	v := p.Verifier(salt, "alice", "password123")
	if want := bigHex(t, rfcV); new(big.Int).SetBytes(v).Cmp(want) != 0 {
		t.Errorf("v = %x, want %x", v, want)
	}

	a, b := bigHex(t, rfcA), bigHex(t, rfcB)
	client := p.ResumeClient("alice", "password123", a.Bytes())
	if got, want := new(big.Int).SetBytes(client.Public()), bigHex(t, rfcPubA); got.Cmp(want) != 0 {
		t.Errorf("A = %x, want %x", got, want)
	}
	server := p.ResumeServer("alice", salt, v, b.Bytes())
	if got, want := new(big.Int).SetBytes(server.Public()), bigHex(t, rfcPubB); got.Cmp(want) != 0 {
		t.Errorf("B = %x, want %x", got, want)
	}
	if got, want := p.u(client.aPub, server.bPub), bigHex(t, rfcU); got.Cmp(want) != 0 {
		t.Errorf("u = %x, want %x", got, want)
	}

	// The premaster secret, from the client's side and from the server's.
	want := bigHex(t, rfcPremaster)
	clientS, _, err := client.secret(salt, server.bPub)
	if err != nil {
		t.Fatal(err)
	}
	if clientS.Cmp(want) != 0 {
		t.Errorf("client S = %x, want %x", clientS, want)
	}
	base := new(big.Int).Exp(server.v, p.u(client.aPub, server.bPub), p.n)
	base.Mul(base, client.aPub).Mod(base, p.n)
	if serverS := base.Exp(base, server.b, p.n); serverS.Cmp(want) != 0 {
		t.Errorf("server S = %x, want %x", serverS, want)
	}
}

// The protocol end to end over the RFC's own parameters and over the default.
func TestAgreement(t *testing.T) {
	for name, p := range map[string]Params{"rfc 1024 sha1": rfcParams(t), "3072 sha256": Default} {
		t.Run(name, func(t *testing.T) {
			salt := NewSalt()
			v := p.Verifier(salt, "recovery", "correct horse battery staple")
			server, err := p.NewServer("recovery", salt, v, rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			client, err := p.NewClient("recovery", "correct horse battery staple", rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			m1, err := client.Respond(salt, server.Public())
			if err != nil {
				t.Fatal(err)
			}
			// The server continues from the private value alone, as another process does.
			resumed := p.ResumeServer("recovery", salt, v, server.Private())
			m2, key, err := resumed.Verify(client.Public(), m1)
			if err != nil {
				t.Fatal(err)
			}
			if err = client.VerifyServer(m2); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(key, client.Key()) {
				t.Error("the two sides ended with different keys")
			}
		})
	}
}

func TestWrongPasswordAndWrongUsername(t *testing.T) {
	p := Default
	salt := NewSalt()
	v := p.Verifier(salt, "recovery", "right")
	for name, c := range map[string][2]string{"password": {"recovery", "wrong"}, "username": {"someone", "right"}} {
		t.Run(name, func(t *testing.T) {
			server, _ := p.NewServer("recovery", salt, v, rand.Reader)
			client, _ := p.NewClient(c[0], c[1], rand.Reader)
			m1, err := client.Respond(salt, server.Public())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = server.Verify(client.Public(), m1); !errors.Is(err, ErrBadProof) {
				t.Errorf("err = %v, want ErrBadProof", err)
			}
		})
	}
}

func TestPublicValuesZeroModuloNAreRefused(t *testing.T) {
	p := Default
	salt := NewSalt()
	v := p.Verifier(salt, "recovery", "pw")
	zero := make([]byte, p.Size())
	n := p.pad(p.n) // N itself: zero modulo N
	twoN := new(big.Int).Lsh(p.n, 1)
	for name, a := range map[string][]byte{"zero": zero, "N": n, "2N": twoN.Bytes(), "empty": nil} {
		t.Run("A is "+name, func(t *testing.T) {
			server, _ := p.NewServer("recovery", salt, v, rand.Reader)
			if _, _, err := server.Verify(a, make([]byte, 32)); !errors.Is(err, ErrBadPublic) {
				t.Errorf("err = %v, want ErrBadPublic", err)
			}
		})
	}
	client, _ := p.NewClient("recovery", "pw", rand.Reader)
	for name, b := range map[string][]byte{"zero": zero, "N": n} {
		t.Run("B is "+name, func(t *testing.T) {
			if _, err := client.Respond(salt, b); !errors.Is(err, ErrBadPublic) {
				t.Errorf("err = %v, want ErrBadPublic", err)
			}
		})
	}
}

func TestDecoyIsStableAndFailsAsAWrongPasswordDoes(t *testing.T) {
	p := Default
	secret := []byte("a secret the server keeps")
	s1, v1 := p.Decoy(secret, "nobody")
	s2, v2 := p.Decoy(secret, "nobody")
	if !bytes.Equal(s1, s2) || !bytes.Equal(v1, v2) {
		t.Fatal("a decoy changed between two asks")
	}
	if s3, _ := p.Decoy(secret, "somebody"); bytes.Equal(s1, s3) {
		t.Error("two usernames share a decoy salt")
	}
	if len(v1) != p.Size() {
		t.Errorf("decoy verifier is %d bytes, want %d", len(v1), p.Size())
	}
	server, _ := p.NewServer("nobody", s1, v1, rand.Reader)
	client, _ := p.NewClient("nobody", "guess", rand.Reader)
	m1, _ := client.Respond(s1, server.Public())
	if _, _, err := server.Verify(client.Public(), m1); !errors.Is(err, ErrBadProof) {
		t.Errorf("err = %v, want ErrBadProof", err)
	}
}

func TestDefaultGroup(t *testing.T) {
	if got := Default.Size(); got != 384 {
		t.Errorf("group width = %d bytes, want 384", got)
	}
	if !Default.n.ProbablyPrime(20) || !new(big.Int).Rsh(Default.n, 1).ProbablyPrime(20) {
		t.Error("the 3072-bit modulus is not a safe prime")
	}
	if Default.h().Size() != sha256.Size {
		t.Error("the default hash is not SHA-256")
	}
}

func TestCredentialIsDeterministicAndRoundTrips(t *testing.T) {
	p := Default
	a, b := p.NewCredential("recovery", "pw"), p.NewCredential("recovery", "pw")
	if !bytes.Equal(a.Salt, b.Salt) || !bytes.Equal(a.Verifier, b.Verifier) {
		t.Fatal("the same username and password gave two credentials")
	}
	if c := p.NewCredential("conformance", "pw"); bytes.Equal(a.Salt, c.Salt) || bytes.Equal(a.Verifier, c.Verifier) {
		t.Error("two usernames share a salt or a verifier")
	}
	back, err := p.ParseCredential(a.Document())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Salt, a.Salt) || !bytes.Equal(back.Verifier, a.Verifier) {
		t.Error("the credential changed on the way through its document")
	}
	for name, doc := range map[string]string{
		"empty":         ``,
		"no salt":       `{"verifier":"` + hex.EncodeToString(a.Verifier) + `"}`,
		"short":         `{"salt":"` + hex.EncodeToString(a.Salt) + `","verifier":"00"}`,
		"zero":          `{"salt":"` + hex.EncodeToString(a.Salt) + `","verifier":"` + hex.EncodeToString(make([]byte, p.Size())) + `"}`,
		"unknown field": `{"salt":"` + hex.EncodeToString(a.Salt) + `","verifier":"` + hex.EncodeToString(a.Verifier) + `","password":"pw"}`,
	} {
		if _, err := p.ParseCredential([]byte(doc)); err == nil {
			t.Errorf("%s: a credential that is not one was read", name)
		}
	}
}
