package issuer

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testRealm = "https://issuer.example"

// RFC 6750 §3: a resource server refusing a bearer token MUST say so
// with a challenge. The library answers an unusable access token at
// /userinfo with a bare http.Error, and a 401 with no challenge is the
// one shape a conforming client cannot act on — it is told it is
// unauthenticated and not told what would fix it, so a client library
// reports a transport failure or retries the same token for ever.
func TestUserinfoRefusalCarriesAChallenge(t *testing.T) {
	t.Parallel()

	handler := challenges(testRealm, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/userinfo":
			http.Error(w, "access token invalid", http.StatusUnauthorized)
		case "/authorize":
			http.Error(w, "not signed in", http.StatusUnauthorized)
		default:
			_, _ = w.Write([]byte("fine"))
		}
	}))

	for name, tc := range map[string]struct {
		path      string
		status    int
		challenge bool
	}{
		"a bearer endpoint refusing a token": {"/userinfo", http.StatusUnauthorized, true},
		// Not every 401 in this service is a bearer failure. The
		// authorize endpoint takes a browser session, and offering it a
		// Bearer challenge would tell a browser to go and find a token.
		"an endpoint that takes no bearer": {"/authorize", http.StatusUnauthorized, false},
		"anything that succeeds":           {"/keys", http.StatusOK, false},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.status)
			}

			got := recorder.Header().Get("WWW-Authenticate") != ""
			if got != tc.challenge {
				t.Errorf("challenge present = %v, want %v", got, tc.challenge)
			}
		})
	}
}

// The library sets its own challenge on some paths. Ours must not
// replace a more specific one with a generic one.
func TestAnExistingChallengeIsLeftAlone(t *testing.T) {
	t.Parallel()

	handler := challenges(testRealm, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="openid"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/userinfo", nil))

	if got := recorder.Header().Get("WWW-Authenticate"); got != `Bearer error="insufficient_scope", scope="openid"` {
		t.Errorf("challenge = %q, want the one the handler set", got)
	}
}

// RFC 6750 3.1: a request with no authentication information is answered
// with a challenge that names no error. A token that was presented and is
// refused says `invalid_token`.
func TestAChallengeNamesAnErrorOnlyWhenATokenWasPresented(t *testing.T) {
	t.Parallel()

	refusing := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "access token invalid", http.StatusUnauthorized)
	})

	form := func(body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/userinfo", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		return r
	}
	header := func(value string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/userinfo", nil)
		r.Header.Set("Authorization", value)

		return r
	}

	const bare = `Bearer realm="https://issuer.example"`
	const invalid = bare + `, error="invalid_token"`

	for name, tc := range map[string]struct {
		request *http.Request
		want    string
	}{
		"nothing presented":          {httptest.NewRequest(http.MethodGet, "/userinfo", nil), bare},
		"an empty POST":              {form(""), bare},
		"a header of another scheme": {header("Basic Zm9vOmJhcg=="), bare},
		"an empty bearer":            {header("Bearer "), bare},
		"a bearer header":            {header("Bearer abc"), invalid},
		"a lower-case scheme":        {header("bearer abc"), invalid},
		"a token in the POST body":   {form("access_token=abc"), invalid},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			recorder := httptest.NewRecorder()
			challenges(testRealm, refusing).ServeHTTP(recorder, tc.request)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", recorder.Code)
			}

			if got := recorder.Header().Get("WWW-Authenticate"); got != tc.want {
				t.Errorf("WWW-Authenticate = %q, want %q", got, tc.want)
			}
		})
	}
}

// The library answers a token its storage will not serve (a session that
// ended, a record that is gone) with 403 and `{}`. RFC 6750 3.1: that is a
// 401 `invalid_token`. A failing storage is not the token's fault.
func TestAForbiddenUserinfoAnswerIsAnInvalidToken(t *testing.T) {
	t.Parallel()

	library := func(body string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(body))
		})
	}

	request := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/userinfo", nil)
		r.Header.Set("Authorization", "Bearer abc")

		return r
	}

	recorder := httptest.NewRecorder()
	challenges(testRealm, library(`{}`)).ServeHTTP(recorder, request())

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}

	if got := recorder.Header().Get("WWW-Authenticate"); got != `Bearer realm="https://issuer.example", error="invalid_token"` {
		t.Errorf("WWW-Authenticate = %q", got)
	}

	if body := recorder.Body.String(); !strings.Contains(body, `"error":"invalid_token"`) {
		t.Errorf("body = %s, want an invalid_token error", body)
	}

	recorder = httptest.NewRecorder()
	challenges(testRealm, library(`{"error":"server_error","error_description":"x"}`)).ServeHTTP(recorder, request())

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("a storage failure: status = %d, want 500", recorder.Code)
	}

	if got := recorder.Header().Get("WWW-Authenticate"); got != "" {
		t.Errorf("a storage failure carries the challenge %q", got)
	}
}

// RFC 9700 4.3: the access token is not accepted in a URL, and nothing
// reaches the library when it is sent there. The POST body stays, as
// OpenID Connect Core 5.3.1 has it.
func TestUserinfoRefusesATokenInTheQuery(t *testing.T) {
	t.Parallel()

	reached := false
	handler := challenges(testRealm, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true

		w.WriteHeader(http.StatusOK)
	}))

	for name, request := range map[string]*http.Request{
		"GET":                 httptest.NewRequest(http.MethodGet, "/userinfo?access_token=abc", nil),
		"GET with a header":   withBearer(httptest.NewRequest(http.MethodGet, "/userinfo?"+url.Values{"access_token": {"abc"}}.Encode(), nil)),
		"POST with the query": httptest.NewRequest(http.MethodPost, "/userinfo?access_token=abc", nil),
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, recorder.Code)
		}

		if got := recorder.Header().Get("WWW-Authenticate"); got != `Bearer realm="https://issuer.example", error="invalid_request"` {
			t.Errorf("%s: WWW-Authenticate = %q", name, got)
		}

		if !strings.Contains(recorder.Body.String(), `"error":"invalid_request"`) {
			t.Errorf("%s: body = %s", name, recorder.Body.String())
		}
	}

	if reached {
		t.Error("a request with the token in its query reached the library")
	}

	post := httptest.NewRequest(http.MethodPost, "/userinfo", strings.NewReader("access_token=abc"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, post)

	if recorder.Code != http.StatusOK || !reached {
		t.Errorf("a token in the POST body: status = %d, reached = %v, want it served", recorder.Code, reached)
	}
}

func withBearer(r *http.Request) *http.Request {
	r.Header.Set("Authorization", "Bearer abc")

	return r
}
