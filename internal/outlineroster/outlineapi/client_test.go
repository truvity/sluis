package outlineapi_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/outlineroster/outlineapi"
	"github.com/truvity/sluis/internal/outlineroster/outlinefake"
)

// A list longer than a page is read whole, a hundred at a time, and never
// asks for more than Outline allows.
func TestListsAreReadPastAHundred(t *testing.T) {
	t.Parallel()
	o := outlinefake.New(t)
	for i := range 250 {
		o.AddUser(fmt.Sprintf("User %d", i), fmt.Sprintf("user%03d@example.com", i), "member")
	}
	c := outlineapi.New(o.URL(), "key-admin", outlineapi.WithHTTPClient(o.Client()))
	users, err := c.Users(t.Context())
	if err != nil {
		t.Fatalf("Users: %v", err)
	}
	if len(users) != 251 {
		t.Errorf("read %d users, want 251", len(users))
	}
	if n := o.Called("users.list"); n != 3 {
		t.Errorf("users.list was called %d times, want 3 pages of at most 100", n)
	}
	for _, call := range o.Calls() {
		if limit, _ := call.Body["limit"].(float64); limit > outlineapi.PageSize {
			t.Errorf("a page asked for %v rows", limit)
		}
	}
}

// A page the server cuts shorter than asked ends the list only when it is
// short: a server that caps pages below the limit is still read whole.
func TestAServerCapOnPagesIsReadWhole(t *testing.T) {
	t.Parallel()
	o := outlinefake.New(t)
	for i := range 7 {
		o.AddGroup(fmt.Sprintf("g%d", i))
	}
	c := outlineapi.New(o.URL(), "key-admin", outlineapi.WithHTTPClient(o.Client()), outlineapi.WithPageSize(3))
	groups, err := c.Groups(t.Context())
	if err != nil || len(groups) != 7 {
		t.Fatalf("Groups = %d, %v; want 7", len(groups), err)
	}
}

// A 429 is an error that says so, with the wait Outline asked for.
func TestRateLimitIsAnError(t *testing.T) {
	t.Parallel()
	o := outlinefake.New(t)
	o.Fail("users.list", outlinefake.Failure{Status: http.StatusTooManyRequests, RetryAfter: 7})
	c := outlineapi.New(o.URL(), "key-admin", outlineapi.WithHTTPClient(o.Client()))
	_, err := c.Users(t.Context())
	if !outlineapi.RateLimited(err) {
		t.Fatalf("err = %v, want rate limited", err)
	}
	var e *outlineapi.Error
	if !asError(err, &e) || e.RetryAfter != 7*time.Second || e.Code != "rate_limit_exceeded" {
		t.Errorf("err = %+v, want Retry-After 7s and the code", e)
	}
}

// A key Outline does not know is unauthorized; a member's key is refused the
// admin methods.
func TestRefusals(t *testing.T) {
	t.Parallel()
	o := outlinefake.New(t)
	member := o.AddUser("M", "m@example.com", "member")
	o.Keys["key-member"] = member.ID
	if _, err := outlineapi.New(o.URL(), "nope", outlineapi.WithHTTPClient(o.Client())).AuthInfo(t.Context()); !outlineapi.Unauthorized(err) {
		t.Errorf("an unknown key: %v, want unauthorized", err)
	}
	err := outlineapi.New(o.URL(), "key-member", outlineapi.WithHTTPClient(o.Client())).Suspend(t.Context(), member.ID)
	var e *outlineapi.Error
	if !asError(err, &e) || e.Status != http.StatusForbidden {
		t.Errorf("a member suspending: %v, want 403", err)
	}
}

// auth.info names the key's user and the team settings; a private collection
// the admin is not in is listed, another person's personal one is not.
func TestAuthInfoAndCollections(t *testing.T) {
	t.Parallel()
	o := outlinefake.New(t)
	o.Team.Sharing = true
	o.AddCollection("Private", "")
	personal := o.AddCollection("Mine", "")
	personal.Owner = "someone-else"
	c := outlineapi.New(o.URL(), "key-admin", outlineapi.WithHTTPClient(o.Client()))
	info, err := c.AuthInfo(t.Context())
	if err != nil || info.User.Role != "admin" || info.User.Email != "key.owner@example.com" || !info.Team.Sharing {
		t.Fatalf("AuthInfo = %+v, %v", info, err)
	}
	collections, err := c.Collections(t.Context())
	if err != nil || len(collections) != 1 || collections[0].Name != "Private" || collections[0].Permission != nil {
		t.Errorf("Collections = %+v, %v; want the private one only", collections, err)
	}
}

func asError(err error, target **outlineapi.Error) bool {
	e, ok := err.(*outlineapi.Error) //nolint:errorlint // the client returns it unwrapped
	*target = e
	return ok
}
