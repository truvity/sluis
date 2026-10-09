//nolint:lll // messages and fixtures are prose and one-line tables
package cfapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/cloudflare/cfapi"
)

const acct = "0123456789abcdef0123456789abcdef"

type call struct {
	method, path, query, auth, body string
}

// A server that speaks Cloudflare's envelope for the five calls the minter makes.
func server(t *testing.T, calls *[]call) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*calls = append(*calls, call{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), string(b)})
		w.Header().Set("Content-Type", "application/json")
		base := "/accounts/" + acct + "/tokens"
		switch {
		case r.Method == http.MethodGet && r.URL.Path == base+"/proto":
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"proto","name":"p","status":"disabled","policies":[{"id":"p1","effect":"allow","resources":{"com.cloudflare.api.account.zone.z":"*"},"permission_groups":[{"id":"g1","name":"DNS Write"}]}],"condition":{"request_ip":{"in":["203.0.113.0/24"]}}}}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/missing"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":1003,"message":"not found"}],"result":null}`)
		case r.Method == http.MethodPost && r.URL.Path == base:
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"new1","name":"n","status":"active","value":"secret-value","expires_on":"2026-10-08T12:15:00Z"}}`)
		case r.Method == http.MethodDelete:
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"gone"}}`)
		case r.Method == http.MethodGet && r.URL.Path == base:
			if r.URL.Query().Get("page") == "1" {
				_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"a","name":"x","status":"expired","expires_on":"2026-10-08T10:00:00Z"}],"result_info":{"page":1,"total_pages":2}}`)
				return
			}
			_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"b","name":"y","status":"active"}],"result_info":{"page":2,"total_pages":2}}`)
		case r.Method == http.MethodGet && r.URL.Path == base+"/permission_groups":
			_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"g1","name":"DNS Write"},{"id":"g2","name":"Billing Read"}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotImplemented)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTheClientSpeaksTheMinterAPI(t *testing.T) {
	var calls []call
	srv := server(t, &calls)
	ctx := context.Background()
	api, err := cfapi.Dial(cfapi.WithBaseURL(srv.URL))(ctx, acct, "minter-token")
	if err != nil {
		t.Fatal(err)
	}

	proto, err := api.GetToken(ctx, "proto")
	if err != nil || proto.Status != cloudflare.StatusDisabled || !strings.Contains(string(proto.Policies), `"DNS Write"`) ||
		!strings.Contains(string(proto.Condition), "request_ip") {
		t.Fatalf("get = %+v %v", proto, err)
	}
	if calls[0].auth != "Bearer minter-token" {
		t.Errorf("authorization = %q", calls[0].auth)
	}
	if _, err = api.GetToken(ctx, "missing"); !errors.Is(err, cloudflare.ErrNotFound) {
		t.Errorf("a 404 = %v, want ErrNotFound", err)
	}

	exp := time.Date(2026, 10, 8, 12, 15, 0, 0, time.UTC)
	created, err := api.CreateToken(ctx, cloudflare.NewToken{
		Name: "sluis/i/p/2026", ExpiresOn: exp,
		Policies:  json.RawMessage(`[{"effect":"allow","resources":{"r":"*"},"permission_groups":[{"id":"g1"}]}]`),
		Condition: json.RawMessage(`{"request.ip":{"in":["203.0.113.0/24"]}}`),
	})
	if err != nil || created.ID != "new1" || created.Value != "secret-value" || !created.ExpiresOn.Equal(exp) {
		t.Fatalf("create = %+v %v", created, err)
	}
	var sent map[string]json.RawMessage
	var post call
	for _, c := range calls {
		if c.method == http.MethodPost {
			post = c
		}
	}
	if err = json.Unmarshal([]byte(post.body), &sent); err != nil {
		t.Fatalf("body %q: %v", post.body, err)
	}
	if string(sent["policies"]) != `[{"effect":"allow","resources":{"r":"*"},"permission_groups":[{"id":"g1"}]}]` ||
		string(sent["condition"]) != `{"request.ip":{"in":["203.0.113.0/24"]}}` ||
		string(sent["expires_on"]) != `"2026-10-08T12:15:00Z"` || string(sent["name"]) != `"sluis/i/p/2026"` {
		t.Errorf("the create body is not what was asked: %s", post.body)
	}

	if err = api.DeleteToken(ctx, "gone"); err != nil {
		t.Errorf("delete: %v", err)
	}
	list, err := api.ListTokens(ctx)
	if err != nil || len(list) != 2 || list[0].ID != "a" || list[0].Status != "expired" || list[1].ID != "b" {
		t.Fatalf("list = %+v %v", list, err)
	}
	for _, c := range calls {
		if c.method == http.MethodGet && c.path == "/accounts/"+acct+"/tokens" && !strings.Contains(c.query, "include_expired=true") {
			t.Errorf("a list without include_expired: %q", c.query)
		}
	}
	groups, err := api.PermissionGroups(ctx)
	if err != nil || groups["g1"] != "DNS Write" || len(groups) != 2 {
		t.Fatalf("groups = %v %v", groups, err)
	}
	if _, err = cfapi.Dial()(ctx, "", "t"); err == nil {
		t.Error("dial without an account")
	}
}

// A refusal that says to try again is repeated, except a create that might
// have been done; an error never carries the body, which can hold a value.
func TestRetriesAndErrors(t *testing.T) {
	ctx := context.Background()
	var gets, posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			gets++
			if gets < 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":10000,"message":"busy"}],"result":null}`)
				return
			}
			_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"g1","name":"DNS Write"}]}`)
		case http.MethodPost:
			posts++
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":1,"message":"upstream"}],"result":{"value":"leaked-value"}}`)
		}
	}))
	t.Cleanup(srv.Close)
	api, err := cfapi.Dial(cfapi.WithBaseURL(srv.URL), cfapi.WithBackoff(time.Millisecond))(ctx, acct, "minter-token")
	if err != nil {
		t.Fatal(err)
	}
	if groups, err := api.PermissionGroups(ctx); err != nil || groups["g1"] != "DNS Write" || gets != 3 {
		t.Fatalf("groups = %v %v after %d calls, want success on the third", groups, err, gets)
	}
	_, err = api.CreateToken(ctx, cloudflare.NewToken{Name: "n", Policies: json.RawMessage(`[]`), ExpiresOn: time.Now()})
	if err == nil || posts != 1 {
		t.Fatalf("create = %v after %d calls, want one refused call", err, posts)
	}
	if strings.Contains(err.Error(), "leaked-value") || !strings.Contains(err.Error(), "HTTP 502") || !strings.Contains(err.Error(), "upstream") {
		t.Errorf("error = %q", err)
	}
	if errors.Is(err, cloudflare.ErrNotFound) {
		t.Error("a 502 is not ErrNotFound")
	}
}
