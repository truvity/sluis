// Package outlinefake is an Outline workspace, in memory, answering the calls
// the Outline controller makes and changing as they change it.
//
// For tests only. It is a package rather than a test file because two
// packages test against it: the client, and the controller that drives it end
// to end.
//
// It models the semantics a reconciler trips over, as Outline v1.12.0 has
// them: an API key acts as its user with that user's role now, and the admin
// methods refuse anybody else (403); lists page by offset and limit, refuse a
// limit over 100, and hold suspended accounts only for an admin; an admin
// lists private collections it is not in only with `includeListOnly`, and
// never another person's personal collection; a group name is taken without
// regard to case; nobody changes their own role or suspends themselves, and
// the last admin is not demoted; the creator of a collection is its first
// manager. A test can make any method fail with a status or a 429, and reads
// back every call made.
package outlinefake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Outline is one workspace.
type Outline struct {
	mu sync.Mutex

	// PageSize caps every page, whatever `limit` asks; zero is the limit
	// asked.
	PageSize int

	Team        Team
	Users       []*User
	Groups      []*Group
	Collections []*Collection
	Documents   []*Document
	// Integrations are the `post` integrations.
	Integrations []Integration
	// Keys are the API keys, to the id of the user each belongs to.
	Keys map[string]string

	calls    []Call
	failures map[string][]Failure
	nextID   int
	server   *httptest.Server
}

// Team is the workspace's settings.
type Team struct {
	ID, Name               string
	Sharing                bool
	MemberCollectionCreate bool
	MembersCanInvite       bool
	DefaultUserRole        string
}

// User is one account.
type User struct {
	ID, Name, Email string
	Role            string
	Suspended       bool
}

// Group is one group and its members' ids.
type Group struct {
	ID, Name, Description string
	Members               []string
}

// Collection is one collection.
type Collection struct {
	ID, Name string
	// Permission is the workspace-wide permission; empty is private.
	Permission string
	// Owner is set on a personal collection.
	Owner string
	// Users and Groups are the direct grants, by id, to their permission.
	Users  map[string]string
	Groups map[string]string
}

// Document is one document and who it is shared with directly.
type Document struct {
	ID, Title, CollectionID string
	Users                   map[string]string
	Groups                  map[string]string
}

// Integration is one integration.
type Integration struct {
	ID, Service, CollectionID, Channel string
}

// Call is one request as received. The key is never recorded.
type Call struct {
	Method string
	Body   map[string]any
}

// Failure is a refusal a test arranges.
type Failure struct {
	Status     int
	Code       string
	RetryAfter int
}

// New is an empty workspace whose first account is an admin holding the key
// "key-admin".
func New(t testing.TB) *Outline {
	o := &Outline{
		Team: Team{ID: "team-1", Name: "Wiki", DefaultUserRole: "member"},
		Keys: map[string]string{}, failures: map[string][]Failure{},
	}
	admin := o.AddUser("Key Owner", "key.owner@example.com", "admin")
	o.Keys["key-admin"] = admin.ID
	o.server = httptest.NewServer(http.HandlerFunc(o.serve))
	t.Cleanup(o.server.Close)
	return o
}

// URL is the workspace's origin.
func (o *Outline) URL() string { return o.server.URL }

// Client is the HTTP client the server answers.
func (o *Outline) Client() *http.Client { return o.server.Client() }

func (o *Outline) id(prefix string) string {
	o.nextID++
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", o.nextID, len(prefix))
}

// AddUser adds an account.
func (o *Outline) AddUser(name, email, role string) *User {
	o.mu.Lock()
	defer o.mu.Unlock()
	u := &User{ID: o.id("user"), Name: name, Email: email, Role: role}
	o.Users = append(o.Users, u)
	return u
}

// AddGroup adds a group with members.
func (o *Outline) AddGroup(name string, members ...string) *Group {
	o.mu.Lock()
	defer o.mu.Unlock()
	g := &Group{ID: o.id("group"), Name: name, Members: slices.Clone(members)}
	o.Groups = append(o.Groups, g)
	return g
}

// AddCollection adds a collection.
func (o *Outline) AddCollection(name, permission string) *Collection {
	o.mu.Lock()
	defer o.mu.Unlock()
	c := &Collection{ID: o.id("coll"), Name: name, Permission: permission, Users: map[string]string{}, Groups: map[string]string{}}
	o.Collections = append(o.Collections, c)
	return c
}

// AddDocument adds a document to a collection.
func (o *Outline) AddDocument(collectionID, title string) *Document {
	o.mu.Lock()
	defer o.mu.Unlock()
	d := &Document{ID: o.id("doc"), Title: title, CollectionID: collectionID, Users: map[string]string{}, Groups: map[string]string{}}
	o.Documents = append(o.Documents, d)
	return d
}

// Fail makes the next calls of method fail, one failure each.
func (o *Outline) Fail(method string, failures ...Failure) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.failures[method] = append(o.failures[method], failures...)
}

// Calls are the calls made so far.
func (o *Outline) Calls() []Call {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.calls)
}

// Called counts the calls of method.
func (o *Outline) Called(method string) int {
	n := 0
	for _, c := range o.Calls() {
		if c.Method == method {
			n++
		}
	}
	return n
}

// Mutations are the calls that change something, in order.
func (o *Outline) Mutations() []Call {
	var out []Call
	for _, c := range o.Calls() {
		switch c.Method {
		case "groups.create", "groups.add_user", "groups.remove_user", "users.update_role", "users.suspend", "users.activate",
			"collections.create", "collections.add_group", "collections.remove_group", "collections.remove_user":
			out = append(out, c)
		}
	}
	return out
}

// UserByEmail is the account of an address.
func (o *Outline) UserByEmail(email string) *User {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, u := range o.Users {
		if strings.EqualFold(u.Email, email) {
			return u
		}
	}
	return nil
}

// GroupByName is the group of a name.
func (o *Outline) GroupByName(name string) *Group {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, g := range o.Groups {
		if strings.EqualFold(g.Name, name) {
			return g
		}
	}
	return nil
}

// CollectionByName is the first collection of a name.
func (o *Outline) CollectionByName(name string) *Collection {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, c := range o.Collections {
		if c.Name == name {
			return c
		}
	}
	return nil
}

type apiError struct {
	status  int
	code    string
	message string
}

func refuse(status int, code, message string) *apiError { return &apiError{status, code, message} }

var forbidden = refuse(http.StatusForbidden, "authorization_error", "Authorization error")

func (o *Outline) serve(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, "/api/")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body == nil {
		body = map[string]any{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, Call{Method: method, Body: body})
	write := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(e *apiError) {
		write(e.status, map[string]any{"ok": false, "error": e.code, "message": e.message, "status": e.status})
	}
	if queued := o.failures[method]; len(queued) > 0 {
		f := queued[0]
		o.failures[method] = queued[1:]
		if f.RetryAfter > 0 {
			w.Header().Set("Retry-After", fmt.Sprint(f.RetryAfter))
		}
		code := f.Code
		if code == "" && f.Status == http.StatusTooManyRequests {
			code = "rate_limit_exceeded"
		}
		fail(refuse(f.Status, code, "arranged by the test"))
		return
	}
	key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	userID, known := o.Keys[key]
	caller := o.user(userID)
	if !ok || !known || caller == nil {
		fail(refuse(http.StatusUnauthorized, "authentication_required", "Authentication required"))
		return
	}
	if caller.Suspended {
		fail(refuse(http.StatusForbidden, "user_suspended", "Your access has been suspended"))
		return
	}
	data, pageable, err := o.handle(method, body, caller)
	if err != nil {
		fail(err)
		return
	}
	if rows, ok := pageable.(page); ok {
		limit, offset := intOf(body["limit"], 25), intOf(body["offset"], 0)
		if limit > 100 {
			fail(refuse(http.StatusBadRequest, "invalid_request", "Pagination limit is too large (max 100)"))
			return
		}
		if o.PageSize > 0 && limit > o.PageSize {
			limit = o.PageSize
		}
		data = rows.slice(offset, limit)
		write(http.StatusOK, map[string]any{"ok": true, "status": 200, "data": data, "pagination": map[string]any{"limit": limit, "offset": offset}})
		return
	}
	write(http.StatusOK, map[string]any{"ok": true, "status": 200, "data": data})
}

// page is a list answer, sliced by the pagination.
type page struct {
	rows []any
	// wrap puts a slice of rows into the shape the method answers with.
	wrap func([]any) any
}

func (p page) slice(offset, limit int) any {
	end := min(offset+limit, len(p.rows))
	rows := []any{}
	if offset < len(p.rows) {
		rows = p.rows[offset:end]
	}
	if p.wrap != nil {
		return p.wrap(rows)
	}
	return rows
}

func intOf(v any, fallback int) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return fallback
}

func str(body map[string]any, key string) string {
	s, _ := body[key].(string)
	return s
}

func (o *Outline) user(id string) *User {
	for _, u := range o.Users {
		if u.ID == id {
			return u
		}
	}
	return nil
}

func (o *Outline) group(id string) *Group {
	for _, g := range o.Groups {
		if g.ID == id {
			return g
		}
	}
	return nil
}

func (o *Outline) collection(id string) *Collection {
	for _, c := range o.Collections {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (o *Outline) document(id string) *Document {
	for _, d := range o.Documents {
		if d.ID == id {
			return d
		}
	}
	return nil
}

func presentUser(u *User, admin bool) map[string]any {
	out := map[string]any{"id": u.ID, "name": u.Name, "role": u.Role, "isSuspended": u.Suspended}
	if admin {
		out["email"] = u.Email
	}
	return out
}

func (o *Outline) presentTeam() map[string]any {
	return map[string]any{
		"id": o.Team.ID, "name": o.Team.Name, "sharing": o.Team.Sharing, "memberCollectionCreate": o.Team.MemberCollectionCreate,
		"defaultUserRole": o.Team.DefaultUserRole, "preferences": map[string]any{"membersCanInvite": o.Team.MembersCanInvite},
	}
}

func presentCollection(c *Collection) map[string]any {
	out := map[string]any{"id": c.ID, "name": c.Name, "permission": nil, "ownerId": nil}
	if c.Permission != "" {
		out["permission"] = c.Permission
	}
	if c.Owner != "" {
		out["ownerId"] = c.Owner
	}
	return out
}

func memberRows(grants map[string]string, field string) []any {
	var ids []string
	for id := range grants {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var rows []any
	for _, id := range ids {
		rows = append(rows, map[string]any{field: id, "permission": grants[id], "sourceId": nil})
	}
	return rows
}

// canRead is whether the caller may read a collection.
func canRead(c *Collection, caller *User) bool {
	switch {
	case c.Owner != "":
		return c.Owner == caller.ID
	case caller.Role == "admin", c.Permission != "":
		return true
	}
	_, member := c.Users[caller.ID]
	return member
}

func (o *Outline) handle(method string, body map[string]any, caller *User) (any, any, *apiError) {
	admin := caller.Role == "admin"
	needAdmin := func() *apiError {
		if !admin {
			return refuse(http.StatusForbidden, "admin_required", "Admin role required")
		}
		return nil
	}
	switch method {
	case "auth.info":
		return map[string]any{"user": presentUser(caller, true), "team": o.presentTeam()}, nil, nil

	case "users.list":
		var rows []any
		for _, u := range o.Users {
			if u.Suspended && !admin {
				continue
			}
			rows = append(rows, presentUser(u, admin))
		}
		return nil, page{rows: rows}, nil
	case "users.update_role":
		if e := needAdmin(); e != nil {
			return nil, nil, e
		}
		u := o.user(str(body, "id"))
		role := str(body, "role")
		switch {
		case u == nil, u != nil && u.Suspended:
			return nil, nil, forbidden
		case u.ID == caller.ID:
			return nil, nil, refuse(http.StatusBadRequest, "validation_error", "You cannot change your own role")
		case u.Role == role:
			return nil, nil, refuse(http.StatusBadRequest, "validation_error", "User is already in that role")
		case role == "guest":
			return nil, nil, forbidden
		}
		if u.Role == "admin" {
			admins := 0
			for _, other := range o.Users {
				if other.Role == "admin" {
					admins++
				}
			}
			if admins <= 1 {
				return nil, nil, refuse(http.StatusBadRequest, "validation_error", "At least one admin is required")
			}
		}
		u.Role = role
		return presentUser(u, true), nil, nil
	case "users.suspend", "users.activate":
		if e := needAdmin(); e != nil {
			return nil, nil, e
		}
		u := o.user(str(body, "id"))
		if u == nil {
			return nil, nil, refuse(http.StatusNotFound, "not_found", "Resource not found")
		}
		if u.ID == caller.ID {
			return nil, nil, forbidden
		}
		u.Suspended = method == "users.suspend"
		return presentUser(u, true), nil, nil

	case "groups.list":
		var rows []any
		for _, g := range o.Groups {
			rows = append(rows, map[string]any{"id": g.ID, "name": g.Name, "description": g.Description, "memberCount": len(g.Members)})
		}
		return nil, page{rows: rows, wrap: func(r []any) any { return map[string]any{"groups": r, "groupMemberships": []any{}} }}, nil
	case "groups.memberships":
		g := o.group(str(body, "id"))
		if g == nil {
			return nil, nil, forbidden
		}
		var rows []any
		for _, id := range g.Members {
			rows = append(rows, map[string]any{"id": id + "-" + g.ID, "userId": id, "groupId": g.ID, "permission": "member"})
		}
		return nil, page{rows: rows, wrap: func(r []any) any { return map[string]any{"groupMemberships": r, "users": []any{}} }}, nil
	case "groups.create":
		if e := needAdmin(); e != nil {
			return nil, nil, e
		}
		name := str(body, "name")
		for _, g := range o.Groups {
			if strings.EqualFold(g.Name, name) {
				return nil, nil, refuse(http.StatusBadRequest, "validation_error", "The name of this group is already in use")
			}
		}
		g := &Group{ID: o.id("group"), Name: name, Description: str(body, "description")}
		o.Groups = append(o.Groups, g)
		return map[string]any{"id": g.ID, "name": g.Name, "description": g.Description}, nil, nil
	case "groups.add_user", "groups.remove_user":
		if e := needAdmin(); e != nil {
			return nil, nil, e
		}
		g, u := o.group(str(body, "id")), o.user(str(body, "userId"))
		if g == nil || u == nil {
			return nil, nil, forbidden
		}
		if method == "groups.add_user" {
			if str(body, "permission") == "" {
				return nil, nil, refuse(http.StatusBadRequest, "validation_error", "the fake wants the permission sent")
			}
			if !slices.Contains(g.Members, u.ID) {
				g.Members = append(g.Members, u.ID)
			}
		} else {
			g.Members = slices.DeleteFunc(g.Members, func(id string) bool { return id == u.ID })
		}
		return map[string]any{}, nil, nil

	case "collections.list":
		listAll := admin && body["includeListOnly"] == true
		var rows []any
		for _, c := range o.Collections {
			_, member := c.Users[caller.ID]
			switch {
			case c.Owner != "" && c.Owner != caller.ID:
				continue
			case c.Owner == "" && c.Permission == "" && !member && !listAll:
				continue
			}
			rows = append(rows, presentCollection(c))
		}
		return nil, page{rows: rows}, nil
	case "collections.create":
		c := &Collection{ID: o.id("coll"), Name: str(body, "name"), Permission: str(body, "permission"),
			Users: map[string]string{caller.ID: "admin"}, Groups: map[string]string{}}
		o.Collections = append(o.Collections, c)
		return presentCollection(c), nil, nil
	case "collections.memberships", "collections.group_memberships":
		c := o.collection(str(body, "id"))
		if c == nil || !canRead(c, caller) {
			return nil, nil, forbidden
		}
		if method == "collections.memberships" {
			return nil, page{rows: memberRows(c.Users, "userId"), wrap: func(r []any) any { return map[string]any{"memberships": r, "users": []any{}} }}, nil
		}
		return nil, page{rows: memberRows(c.Groups, "groupId"), wrap: func(r []any) any { return map[string]any{"groupMemberships": r, "groups": []any{}} }}, nil
	case "collections.add_group", "collections.remove_group", "collections.remove_user":
		c := o.collection(str(body, "id"))
		if c == nil || c.Owner != "" || (!admin && c.Users[caller.ID] != "admin") {
			return nil, nil, forbidden
		}
		switch method {
		case "collections.add_group":
			if o.group(str(body, "groupId")) == nil {
				return nil, nil, forbidden
			}
			c.Groups[str(body, "groupId")] = str(body, "permission")
		case "collections.remove_group":
			if _, ok := c.Groups[str(body, "groupId")]; !ok {
				return nil, nil, refuse(http.StatusBadRequest, "validation_error", "This group is not a member of the collection")
			}
			delete(c.Groups, str(body, "groupId"))
		case "collections.remove_user":
			if _, ok := c.Users[str(body, "userId")]; !ok {
				return nil, nil, refuse(http.StatusBadRequest, "validation_error", "User is not a collection member")
			}
			delete(c.Users, str(body, "userId"))
		}
		return map[string]any{"success": true}, nil, nil

	case "documents.list":
		c := o.collection(str(body, "collectionId"))
		var rows []any
		if c != nil && (c.Permission != "" || c.Users[caller.ID] != "") && c.Owner == "" {
			for _, d := range o.Documents {
				if d.CollectionID == c.ID {
					rows = append(rows, map[string]any{"id": d.ID, "title": d.Title, "collectionId": d.CollectionID, "text": "..."})
				}
			}
		}
		return nil, page{rows: rows}, nil
	case "documents.memberships", "documents.group_memberships":
		d := o.document(str(body, "id"))
		if d == nil {
			return nil, nil, forbidden
		}
		c := o.collection(d.CollectionID)
		if c == nil || (c.Permission == "" && c.Users[caller.ID] == "") {
			return nil, nil, forbidden
		}
		if method == "documents.memberships" {
			return nil, page{rows: memberRows(d.Users, "userId"), wrap: func(r []any) any { return map[string]any{"memberships": r, "users": []any{}} }}, nil
		}
		return nil, page{rows: memberRows(d.Groups, "groupId"), wrap: func(r []any) any { return map[string]any{"groupMemberships": r, "groups": []any{}} }}, nil

	case "integrations.list":
		var rows []any
		for _, i := range o.Integrations {
			rows = append(rows, map[string]any{"id": i.ID, "type": "post", "service": i.Service, "collectionId": i.CollectionID,
				"settings": map[string]any{"channel": i.Channel}})
		}
		return nil, page{rows: rows}, nil
	}
	return nil, nil, refuse(http.StatusNotFound, "not_found", "Resource not found")
}
