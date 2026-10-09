package issuer_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

func asMetadataHandler(t *testing.T, issuerURL string, documents bool) http.Handler {
	t.Helper()

	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatalf("parse the demonstration policy: %v", err)
	}
	if documents {
		declared.ClientDocuments = policy.ClientDocuments{
			Origins:  []string{"clients.example"},
			Requires: []string{"rung:engineering"},
		}
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("policy set: %v", err)
	}
	iss := issuer.New(issuer.Config{URL: issuerURL, AllowInsecure: true}, set, &fakeDirectory{}, issuer.NewMemoryState())
	storage, err := issuer.NewStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	handler, err := handler(iss, storage)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	return handler
}

func asGet(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, "http://issuer.example"+path, nil))
	return rec
}

// RFC 8414 metadata is the same document as discovery, narrowed: every
// member it carries equals discovery's, and headers match.
func TestAuthorizationServerMetadataIsDiscoveryNarrowed(t *testing.T) {
	t.Parallel()

	handler := asMetadataHandler(t, "http://issuer.example", true)
	oidc := asGet(t, handler, http.MethodGet, "/.well-known/openid-configuration")
	meta := asGet(t, handler, http.MethodGet, "/.well-known/oauth-authorization-server")

	if meta.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", meta.Code, meta.Body)
	}
	if got, want := meta.Header().Get("Content-Type"), oidc.Header().Get("Content-Type"); got != want {
		t.Errorf("Content-Type = %q, discovery says %q", got, want)
	}
	for _, name := range []string{"Cache-Control", "Access-Control-Allow-Origin"} {
		if got, want := meta.Header().Get(name), oidc.Header().Get(name); got != want {
			t.Errorf("%s = %q, discovery says %q", name, got, want)
		}
	}

	var full, doc map[string]any
	if err := json.Unmarshal(oidc.Body.Bytes(), &full); err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if err := json.Unmarshal(meta.Body.Bytes(), &doc); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	t.Logf("served members: %v", doc)

	for _, name := range []string{"issuer", "authorization_endpoint", "token_endpoint", "jwks_uri",
		"response_types_supported", "grant_types_supported", "client_id_metadata_document_supported"} {
		if _, ok := doc[name]; !ok {
			t.Errorf("%s is missing", name)
		}
	}
	for name, value := range doc {
		want, ok := full[name]
		if !ok {
			t.Errorf("%s is served here but not by discovery", name)
			continue
		}
		a, _ := json.Marshal(value)
		b, _ := json.Marshal(want)
		if string(a) != string(b) {
			t.Errorf("%s = %s, discovery says %s", name, a, b)
		}
	}
	for _, oidcOnly := range []string{"userinfo_endpoint", "id_token_signing_alg_values_supported",
		"subject_types_supported", "acr_values_supported", "claims_supported"} {
		if _, ok := doc[oidcOnly]; ok {
			t.Errorf("%s is OIDC-only and must not be in RFC 8414 metadata", oidcOnly)
		}
	}
	if methods, _ := doc["code_challenge_methods_supported"].([]any); len(methods) != 1 || methods[0] != "S256" {
		t.Errorf("code_challenge_methods_supported = %v, want [S256]", methods)
	}
}

// Anything but the exact path, and any method discovery refuses, is
// answered as discovery answers it.
func TestAuthorizationServerMetadataRoutingMatchesDiscovery(t *testing.T) {
	t.Parallel()

	handler := asMetadataHandler(t, "http://issuer.example", false)
	for _, tc := range []struct{ method, suffix string }{
		{http.MethodGet, "/extra"},
		{http.MethodPost, ""},
		{http.MethodDelete, ""},
	} {
		oidc := asGet(t, handler, tc.method, "/.well-known/openid-configuration"+tc.suffix)
		meta := asGet(t, handler, tc.method, "/.well-known/oauth-authorization-server"+tc.suffix)
		if oidc.Code != meta.Code {
			t.Errorf("%s %s: status %d, discovery's equivalent %d", tc.method, tc.suffix, meta.Code, oidc.Code)
		}
	}

	rec := asGet(t, handler, http.MethodGet, "/.well-known/oauth-authorization-server")
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["client_id_metadata_document_supported"]; ok {
		t.Error("client documents are advertised on an installation that names no origin")
	}
}

// RFC 8414 3.1: an issuer with a path is served with the well-known segment
// inserted before that path.
func TestAuthorizationServerMetadataInsertsTheIssuerPath(t *testing.T) {
	t.Parallel()

	handler := asMetadataHandler(t, "http://issuer.example/tenant", false)
	if rec := asGet(t, handler, http.MethodGet, "/.well-known/oauth-authorization-server/tenant"); rec.Code != http.StatusOK {
		t.Errorf("inserted path = %d, want 200", rec.Code)
	}
	if rec := asGet(t, handler, http.MethodGet, "/.well-known/oauth-authorization-server"); rec.Code == http.StatusOK {
		t.Error("the bare path answered for an issuer that has a path")
	}
}

// Discovery names no surface this issuer does not serve: no introspection
// endpoint, and no signing-algorithm list for client authentication or
// request objects, which the library fills with its RS256 default.
func TestDiscoveryAdvertisesOnlyWhatIsServed(t *testing.T) {
	t.Parallel()

	handler := asMetadataHandler(t, "http://issuer.example", false)
	for _, path := range []string{"/.well-known/openid-configuration", "/.well-known/oauth-authorization-server"} {
		rec := asGet(t, handler, http.MethodGet, path)
		var doc map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for name := range doc {
			if strings.HasPrefix(name, "introspection_") || name == "request_object_signing_alg_values_supported" ||
				strings.HasSuffix(name, "_auth_signing_alg_values_supported") {
				t.Errorf("%s advertises %q, which this issuer does not serve", path, name)
			}
		}
	}
}
