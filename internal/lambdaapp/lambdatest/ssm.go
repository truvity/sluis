package lambdatest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// SSM is SSM Parameter Store over its wire protocol (JSON 1.1, the operation
// in X-Amz-Target), enough for the secrets reads a function makes: the real
// client, with its retryer, talks to it. Parameters are by full name.
type SSM struct {
	srv *httptest.Server

	mu     sync.Mutex
	params map[string]string
	reads  []string
}

// NewSSM starts a fake holding params.
func NewSSM(t testing.TB, params map[string]string) *SSM {
	t.Helper()
	f := &SSM{params: map[string]string{}}
	for k, v := range params {
		f.params[k] = v
	}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

// URL is the endpoint to put in a document's `secrets.endpoint`.
func (f *SSM) URL() string { return f.srv.URL }

// Reads are the parameters and paths asked for so far, in order, by whatever
// reached this fake: a test that gives each function its own fake can say what
// that function never read.
func (f *SSM) Reads() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reads...)
}

// Put sets a parameter.
func (f *SSM) Put(name, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.params[name] = value
}

// Delete removes a parameter.
func (f *SSM) Delete(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.params, name)
}

// ServeHTTP implements http.Handler.
func (f *SSM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	op := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AmazonSSM.")
	body, _ := io.ReadAll(r.Body)
	var in struct {
		Name, Path string
		Names      []string
	}
	_ = json.Unmarshal(body, &in)
	f.mu.Lock()
	defer f.mu.Unlock()
	if in.Name != "" {
		f.reads = append(f.reads, in.Name)
	}
	f.reads = append(f.reads, in.Names...)
	if in.Path != "" {
		f.reads = append(f.reads, in.Path)
	}
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	param := func(name string) map[string]any {
		return map[string]any{"Name": name, "Value": f.params[name], "Type": "SecureString", "Version": 1}
	}
	switch op {
	case "GetParameter":
		name, _, _ := strings.Cut(in.Name, ":")
		if _, ok := f.params[name]; !ok {
			reply(http.StatusBadRequest, map[string]string{"__type": "ParameterNotFound", "message": "ParameterNotFound"})
			return
		}
		reply(http.StatusOK, map[string]any{"Parameter": param(name)})
	case "GetParameters":
		found, missing := []any{}, []string{}
		for _, n := range in.Names {
			if _, ok := f.params[n]; ok {
				found = append(found, param(n))
			} else {
				missing = append(missing, n)
			}
		}
		reply(http.StatusOK, map[string]any{"Parameters": found, "InvalidParameters": missing})
	case "GetParametersByPath":
		prefix := strings.TrimSuffix(in.Path, "/") + "/"
		var names []string
		for n := range f.params {
			if strings.HasPrefix(n, prefix) {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		out := []any{}
		for _, n := range names {
			out = append(out, param(n))
		}
		reply(http.StatusOK, map[string]any{"Parameters": out})
	default:
		reply(http.StatusBadRequest, map[string]string{"__type": "UnsupportedOperation", "message": op})
	}
}
