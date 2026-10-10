package openbaotest

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/truvity/sluis/storage/openbao"
)

// FakeTransit is an in-process transit engine for tests that need no
// development server: it keeps real keys per version, creates a key on
// encrypt when asked to, and enforces the versions the way the engine does
// (min_decryption_version, min_encryption_version and trim), which is what
// erasing a key relies on. It knows encrypt, decrypt, hmac and keys/<name>
// with rotate, config and trim, for symmetric keys; there is no policy.
type FakeTransit struct {
	srv   *httptest.Server
	mount string

	mu       sync.Mutex
	keys     map[string]*fakeKey
	requests []string
}

type fakeKey struct {
	versions             map[int][]byte
	latest, minDecrypt   int
	minEncrypt, minAvail int
}

// NewFakeTransit starts a fake engine mounted at "transit".
func NewFakeTransit(t testing.TB) *FakeTransit {
	t.Helper()
	f := &FakeTransit{mount: "transit", keys: map[string]*fakeKey{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// Client is a client of the fake.
func (f *FakeTransit) Client(t testing.TB) *openbao.Client {
	t.Helper()
	c, err := openbao.New(openbao.Config{Address: f.srv.URL, Token: "fake", AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Requests are the "METHOD path" of every request so far, with the mount
// removed ("POST encrypt/a", "GET keys/a/rotate").
func (f *FakeTransit) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

// Names are the keys that exist, sorted.
func (f *FakeTransit) Names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.keys))
	for n := range f.keys {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Latest is the latest version of a key, 0 if there is none.
func (f *FakeTransit) Latest(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if k := f.keys[name]; k != nil {
		return k.latest
	}
	return 0
}

func (k *fakeKey) rotate() {
	s := make([]byte, 32)
	_, _ = rand.Read(s)
	k.latest++
	k.versions[k.latest] = s
}

func reply(w http.ResponseWriter, status int, data any, errs ...string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if len(errs) > 0 {
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": errs})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func (f *FakeTransit) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1/"+f.mount+"/")
	f.requests = append(f.requests, r.Method+" "+path)
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	str := func(k string) string { s, _ := body[k].(string); return s }
	num := func(k string) int { n, _ := body[k].(float64); return int(n) }

	op, rest, _ := strings.Cut(path, "/")
	name, sub, _ := strings.Cut(rest, "/")
	k := f.keys[name]
	switch {
	case op == "keys" && sub == "" && r.Method == http.MethodGet:
		if k == nil {
			reply(w, http.StatusNotFound, nil, "")
			return
		}
		vs := map[string]int{}
		for v := range k.versions {
			vs[strconv.Itoa(v)] = 1
		}
		reply(w, 200, map[string]any{"type": "aes256-gcm96", "derived": false, "latest_version": k.latest,
			"min_available_version": k.minAvail, "min_decryption_version": k.minDecrypt,
			"min_encryption_version": k.minEncrypt, "keys": vs})
	case op == "keys" && k != nil && sub == "rotate":
		k.rotate()
		reply(w, 200, nil)
	case op == "keys" && k != nil && sub == "config":
		k.minDecrypt, k.minEncrypt = num("min_decryption_version"), num("min_encryption_version")
		reply(w, 200, nil)
	case op == "keys" && k != nil && sub == "trim":
		minAvail := num("min_available_version")
		if minAvail > k.minDecrypt {
			reply(w, 400, nil, "minimum available version cannot be greater than the minimum decryption version")
			return
		}
		for v := range k.versions {
			if v < minAvail {
				delete(k.versions, v)
			}
		}
		k.minAvail = minAvail
		reply(w, 200, nil)
	case op == "encrypt":
		if k == nil {
			k = &fakeKey{versions: map[int][]byte{}, minDecrypt: 1, minEncrypt: 1, minAvail: 1}
			k.rotate()
			f.keys[name] = k
		}
		ver := num("key_version")
		if ver == 0 {
			ver = k.latest
		}
		pt, _ := base64.StdEncoding.DecodeString(str("plaintext"))
		secret, ok := k.versions[ver]
		if !ok || ver < k.minEncrypt {
			reply(w, 400, nil, "requested version for encryption is less than the minimum encryption key version")
			return
		}
		gcm := fakeGCM(secret)
		nonce := make([]byte, gcm.NonceSize())
		_, _ = rand.Read(nonce)
		ad, _ := base64.StdEncoding.DecodeString(str("associated_data"))
		ct := gcm.Seal(nonce, nonce, pt, ad)
		reply(w, 200, map[string]any{"ciphertext": fmt.Sprintf("vault:v%d:%s", ver, base64.StdEncoding.EncodeToString(ct))})
	case op == "decrypt":
		if k == nil {
			reply(w, 400, nil, "encryption key not found")
			return
		}
		parts := strings.SplitN(str("ciphertext"), ":", 3)
		ver, _ := strconv.Atoi(strings.TrimPrefix(parts[1], "v"))
		secret, ok := k.versions[ver]
		if !ok || ver < k.minDecrypt {
			reply(w, 400, nil, "ciphertext or signature version is disallowed by policy (too old)")
			return
		}
		raw, _ := base64.StdEncoding.DecodeString(parts[2])
		gcm := fakeGCM(secret)
		ad, _ := base64.StdEncoding.DecodeString(str("associated_data"))
		if len(raw) < gcm.NonceSize() {
			reply(w, 400, nil, "invalid ciphertext")
			return
		}
		pt, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], ad)
		if err != nil {
			reply(w, 400, nil, "message authentication failed")
			return
		}
		reply(w, 200, map[string]any{"plaintext": base64.StdEncoding.EncodeToString(pt)})
	case op == "hmac":
		if k == nil {
			reply(w, 400, nil, "encryption key not found")
			return
		}
		ver := num("key_version")
		if ver == 0 {
			ver = k.latest
		}
		secret, ok := k.versions[ver]
		if !ok || ver < k.minDecrypt {
			reply(w, 400, nil, fmt.Sprintf("cannot use key version %d: version is too old (disallowed by policy)", ver))
			return
		}
		in, _ := base64.StdEncoding.DecodeString(str("input"))
		m := hmac.New(sha256.New, secret)
		m.Write(in)
		reply(w, 200, map[string]any{"hmac": fmt.Sprintf("vault:v%d:%s", ver, base64.StdEncoding.EncodeToString(m.Sum(nil)))})
	default:
		reply(w, 404, nil, "unsupported path "+path)
	}
}

func fakeGCM(secret []byte) cipher.AEAD {
	blk, _ := aes.NewCipher(secret)
	g, _ := cipher.NewGCM(blk)
	return g
}
