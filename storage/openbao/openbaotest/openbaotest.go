// Package openbaotest is the test harness of the OpenBao backends: it talks
// to a development server (`bao server -dev`) named by the environment and
// gives each test a [openbao.Client] that logs in with a real JWT under a
// role whose policy is only what the test asks for, so the suites run with
// the least privilege the documentation prescribes, not with the root token.
//
// The tests SKIP when the environment is not set so that `go test ./...`
// needs no server; hack/openbao-conformance.sh sets it and fails when
// anything skipped.
package openbaotest

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/storage/openbao"
)

// The environment of a test run.
const (
	// EnvAddr is the development server, http://127.0.0.1:8200.
	EnvAddr = "STORAGE_OPENBAO_ADDR"
	// EnvToken is its root token.
	EnvToken = "STORAGE_OPENBAO_ROOT_TOKEN"
)

const audience = "storage-test"

// Dev is a development server.
type Dev struct {
	Addr, RootToken string
}

var (
	setupOnce sync.Once
	setupErr  error
	signer    *ecdsa.PrivateKey
	// authMount is this process's JWT auth mount: one per test binary, so
	// packages that run in parallel against one server each trust their own key.
	authMount = Name("jwt")
)

// Env returns the server of the environment, or skips the test.
func Env(t testing.TB) *Dev {
	t.Helper()
	addr, root := os.Getenv(EnvAddr), os.Getenv(EnvToken)
	if addr == "" || root == "" {
		t.Skipf("%s and %s are not set: no OpenBao to test against", EnvAddr, EnvToken)
	}
	d := &Dev{Addr: strings.TrimRight(addr, "/"), RootToken: root}
	setupOnce.Do(func() { setupErr = d.setupJWT() })
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	return d
}

// Root sends a request with the root token and returns the status and body.
func (d *Dev) Root(t testing.TB, method, path string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, d.Addr+"/v1/"+path, r)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Vault-Token", d.RootToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// MustRoot is Root that fails the test unless the status is 2xx.
func (d *Dev) MustRoot(t testing.TB, method, path string, body any) []byte {
	t.Helper()
	st, raw := d.Root(t, method, path, body)
	if st/100 != 2 {
		t.Fatalf("%s %s: %d: %s", method, path, st, raw)
	}
	return raw
}

func (d *Dev) setupJWT() error {
	var err error
	if signer, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		return err
	}
	der, err := x509.MarshalPKIXPublicKey(&signer.PublicKey)
	if err != nil {
		return err
	}
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	if st, raw := d.rawRoot("POST", "sys/auth/"+authMount, map[string]string{"type": "jwt"}); st/100 != 2 {
		return &httpError{"sys/auth/" + authMount, st, raw}
	}
	st, raw := d.rawRoot("POST", "auth/"+authMount+"/config", map[string]any{"jwt_validation_pubkeys": []string{pubPEM}})
	if st/100 != 2 {
		return &httpError{"auth/" + authMount + "/config", st, raw}
	}
	return nil
}

type httpError struct {
	path   string
	status int
	body   []byte
}

func (e *httpError) Error() string {
	return "openbaotest: " + e.path + ": " + http.StatusText(e.status) + ": " + string(e.body)
}

func (d *Dev) rawRoot(method, path string, body any) (int, []byte) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(context.Background(), method, d.Addr+"/v1/"+path, bytes.NewReader(b))
	req.Header.Set("X-Vault-Token", d.RootToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// Name returns a name unique to this test run, for a mount, a policy or a
// role: prefix-<random>.
func Name(prefix string) string {
	var b [5]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}

// MountKV mounts a KV version 2 engine at a fresh path and returns it.
// maxVersions 0 leaves the server's default.
func (d *Dev) MountKV(t testing.TB, maxVersions int) string {
	t.Helper()
	mount := Name("kv")
	d.MustRoot(t, "POST", "sys/mounts/"+mount, map[string]any{"type": "kv", "options": map[string]string{"version": "2"}})
	if maxVersions > 0 {
		d.MustRoot(t, "POST", mount+"/config", map[string]any{"max_versions": maxVersions})
	}
	t.Cleanup(func() { d.rawRoot("DELETE", "sys/mounts/"+mount, nil) })
	return mount
}

// MountTransit mounts a transit engine at a fresh path and returns it.
func (d *Dev) MountTransit(t testing.TB) string {
	t.Helper()
	mount := Name("transit")
	d.MustRoot(t, "POST", "sys/mounts/"+mount, map[string]any{"type": "transit"})
	t.Cleanup(func() { d.rawRoot("DELETE", "sys/mounts/"+mount, nil) })
	return mount
}

// Client returns a client that logs in with a JWT under a fresh role whose
// only policy is policy (HCL).
func (d *Dev) Client(t testing.TB, policy string) *openbao.Client {
	t.Helper()
	name := Name("role")
	d.MustRoot(t, "PUT", "sys/policies/acl/"+name, map[string]string{"policy": policy})
	t.Cleanup(func() { d.rawRoot("DELETE", "sys/policies/acl/"+name, nil) })
	d.MustRoot(t, "POST", "auth/"+authMount+"/role/"+name, map[string]any{
		"role_type": "jwt", "bound_audiences": []string{audience}, "user_claim": "sub",
		"token_policies": []string{name}, "token_ttl": "1h",
	})
	t.Cleanup(func() { d.rawRoot("DELETE", "auth/"+authMount+"/role/"+name, nil) })
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(d.JWT(t, name)), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := openbao.New(openbao.Config{
		Address: d.Addr, AllowInsecureHTTP: true,
		Login: &openbao.Login{Mount: authMount, Role: name, TokenFile: file},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// JWT returns a token the dev server's jwt mount accepts for a subject.
func (d *Dev) JWT(t testing.TB, subject string) string {
	t.Helper()
	b64 := base64.RawURLEncoding.EncodeToString
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"sub": subject, "aud": audience, "iss": "storage-test",
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	signing := b64(header) + "." + b64(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, signer, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + b64(sig)
}
