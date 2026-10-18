package srpweb_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/srpauth"
	"github.com/truvity/sluis/internal/srpweb"
	"github.com/truvity/sluis/internal/throttle"
	"github.com/truvity/sluis/srp"
)

func TestScriptIsServedAsTheOriginsOwnFile(t *testing.T) {
	srv := httptest.NewServer(srpweb.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
		t.Errorf("Content-Type = %q", got)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("the script is served without nosniff")
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("If-None-Match", resp.Header.Get("ETag"))
	again, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = again.Body.Close()
	if again.StatusCode != http.StatusNotModified {
		t.Errorf("a request with the tag: %d, want 304", again.StatusCode)
	}
}

// The script's group is the Go package's: a constant copied by hand that drifted
// would sign nobody in and say nothing.
func TestScriptCarriesTheGroupOfTheGoPackage(t *testing.T) {
	compact := strings.NewReplacer("'", "", "+", "", " ", "", "\n", "").Replace(string(srpweb.Script()))
	want := hex.EncodeToString(srp.Default.Modulus())
	if !strings.Contains(compact, want) {
		t.Error("srp.js does not carry the 3072-bit modulus of package srp")
	}
}

func TestScriptHasNoInlineHandlersNorOutsideReferences(t *testing.T) {
	src := string(srpweb.Script())
	for _, banned := range []string{"eval(", "new Function", "document.write", "innerHTML", "http://", "https://", "XMLHttpRequest"} {
		if strings.Contains(src, banned) {
			t.Errorf("srp.js contains %q", banned)
		}
	}
}

// harness is the page's two endpoints over a real exchange service, the way the
// issuer and the console serve them.
func harness(t *testing.T, password string) *httptest.Server {
	t.Helper()
	cred := srp.Default.NewCredential("recovery", password)
	svc := &srpauth.Service{
		Params:   srp.Default,
		Store:    srpauth.StateStore{State: memory.New()},
		DecoyKey: func() ([]byte, error) { return []byte("k"), nil },
		Refusals: throttle.Default(),
	}
	lookup := func(_ context.Context, user string) (srp.Credential, bool, error) {
		return cred, user == "recovery", nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /start", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ State, Username string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		begun, err := svc.Start(r.Context(), "test", in.State, in.Username, lookup)
		if err != nil {
			http.Error(w, `{"error":"no"}`, http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"handle": begun.Handle, "salt": hex.EncodeToString(begun.Salt), "B": hex.EncodeToString(begun.B)})
	})
	mux.HandleFunc("POST /finish", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ State, Handle, A, M1 string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		a, _ := hex.DecodeString(in.A)
		m1, _ := hex.DecodeString(in.M1)
		proved, err := svc.Finish(r.Context(), "test", in.State, in.Handle, a, m1, lookup)
		if err != nil {
			http.Error(w, `{"error":"no"}`, http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"M2": hex.EncodeToString(proved.M2), "next": "/done"})
	})
	return httptest.NewServer(mux)
}

func node(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node is not on PATH, and the cross test of the script has to run in CI")
		}
		t.Skip("node is not on PATH")
	}
	return path
}

// runScript runs srp.js's signIn under node against the harness and returns
// what the script printed.
func runScript(t *testing.T, base, username, password string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/srp.js", srpweb.Script(), 0o600); err != nil {
		t.Fatal(err)
	}
	driver := `
const srp = require(process.argv[2] + '/srp.js');
srp.signIn({username: process.argv[4], password: process.env.PW, state: 'bound-state',
  start: process.argv[3] + '/start', finish: process.argv[3] + '/finish'})
  .then((next) => console.log('next=' + next), (err) => { console.log('refused status=' + err.status); process.exit(3); });
`
	if err := os.WriteFile(dir+"/driver.js", []byte(driver), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node(t), dir+"/driver.js", dir, base, username)
	cmd.Env = append(os.Environ(), "PW="+password)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// The browser script and the Go package agree on every byte of the exchange:
// the script signs in against the Go server, which checks its M1, and the
// script checks the server's M2.
func TestScriptSignsInAgainstTheGoServer(t *testing.T) {
	const password = "correct horse battery staple, forty-ish chars"
	srv := harness(t, password)
	defer srv.Close()

	out, err := runScript(t, srv.URL, "recovery", password)
	if err != nil || out != "next=/done" {
		t.Fatalf("right password: %v: %q", err, out)
	}
	if out, err = runScript(t, srv.URL, "recovery", "wrong"); err == nil || !strings.Contains(out, "refused status=403") {
		t.Errorf("wrong password: %v: %q", err, out)
	}
	if out, err = runScript(t, srv.URL, "nobody", password); err == nil || !strings.Contains(out, "refused status=403") {
		t.Errorf("unknown username: %v: %q", err, out)
	}
}

// The Go client signs in against the script's own arithmetic too: the script's
// A, M1 for a fixed salt and B equal the Go client's for the same private value.
func TestScriptAndGoClientComputeTheSameProof(t *testing.T) {
	const user, password = "recovery", "a password"
	cred := srp.Default.NewCredential(user, password)
	server, err := srp.Default.NewServer(user, cred.Salt, cred.Verifier, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.WriteFile(dir+"/srp.js", srpweb.Script(), 0o600); err != nil {
		t.Fatal(err)
	}
	// A fixed "random" source, so the script's a is the one the Go client gets.
	a := make([]byte, srp.PrivateBytes)
	for i := range a {
		a[i] = byte(i + 1)
	}
	driver := `
const srp = require(process.argv[2] + '/srp.js');
(async () => {
  const a = Uint8Array.from(Buffer.from(process.argv[3], 'hex'));
  const c = await srp.client(process.argv[4], process.env.PW, {getRandomValues: (b) => { b.set(a); return b; }});
  const m1 = await c.respond(process.argv[5], process.argv[6]);
  console.log(JSON.stringify({A: c.A, M1: m1}));
})();
`
	if err = os.WriteFile(dir+"/driver.js", []byte(driver), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node(t), dir+"/driver.js", dir, hex.EncodeToString(a), user, hex.EncodeToString(cred.Salt), hex.EncodeToString(server.Public()))
	cmd.Env = append(os.Environ(), "PW="+password)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var got struct{ A, M1 string }
	if err = json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	client := srp.Default.ResumeClient(user, password, a)
	m1, err := client.Respond(cred.Salt, server.Public())
	if err != nil {
		t.Fatal(err)
	}
	if got.A != hex.EncodeToString(client.Public()) {
		t.Error("the script's A differs from the Go client's")
	}
	if got.M1 != hex.EncodeToString(m1) {
		t.Error("the script's M1 differs from the Go client's")
	}
}
