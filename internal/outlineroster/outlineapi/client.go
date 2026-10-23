// Package outlineapi is the part of Outline's API the Outline controller
// calls. The API is RPC-shaped: `POST <url>/api/<method>` with a JSON body
// and `Authorization: Bearer <api key>`, answered by `{data, pagination?}` or
// by `{ok: false, error, message, status}`.
//
// Every list is read whole: a list method pages by `offset` and `limit`, at
// most 100 a page (a larger limit is refused, not clamped), and the client
// asks for the next page until one comes back short. A read cut short is an
// error, never a shorter list.
//
// The methods and shapes are Outline v1.12.0's (server/routes/api).
package outlineapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// PageSize is how many rows one page asks for: Outline's maximum.
const PageSize = 100

// maxPages bounds a list: 100 000 rows, far past any workspace this serves,
// so a server that never ends a list cannot hold a pass forever.
const maxPages = 1000

// maxBody bounds one response.
const maxBody = 32 << 20

// Client calls one Outline workspace with one API key.
type Client struct {
	base string
	key  string
	http *http.Client
	// pageSize is PageSize except in a test.
	pageSize int
}

// Option changes a client.
type Option func(*Client)

// WithHTTPClient sends through h.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithPageSize asks for n rows a page; a test uses it to page small lists.
func WithPageSize(n int) Option { return func(c *Client) { c.pageSize = n } }

// New is a client of the workspace at base (its origin) acting with key.
func New(base, key string, opts ...Option) *Client {
	c := &Client{base: strings.TrimSuffix(base, "/"), key: key, http: &http.Client{Timeout: 30 * time.Second}, pageSize: PageSize}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Error is Outline's refusal of a call.
type Error struct {
	Method string
	// Status is the HTTP status, Code Outline's error id (`authorization_error`,
	// `rate_limit_exceeded`, ...).
	Status        int
	Code, Message string
	// RetryAfter is how long a rate-limited call asked to wait.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Code != "" {
		return fmt.Sprintf("outline %s: %s (%s, %d)", e.Method, msg, e.Code, e.Status)
	}
	return fmt.Sprintf("outline %s: %s (%d)", e.Method, msg, e.Status)
}

// RateLimited reports whether err is Outline's rate limiter refusing a call.
func RateLimited(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusTooManyRequests
}

// Unauthorized reports whether err says the key is not accepted at all.
func Unauthorized(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusUnauthorized
}

// ErrCutShort is a list that could not be read whole.
var ErrCutShort = errors.New("outline: a list could not be read whole")

// call POSTs body to method and decodes the response's `data` into out (nil
// skips it).
func (c *Client) call(ctx context.Context, method string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("outline %s: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/"+method, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("outline %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("outline %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("outline %s: read the response: %w", method, err)
	}
	if len(payload) > maxBody {
		return fmt.Errorf("outline %s: the response is larger than %d bytes", method, maxBody)
	}
	if resp.StatusCode >= 300 {
		e := &Error{Method: method, Status: resp.StatusCode}
		var envelope struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(payload, &envelope) == nil {
			e.Code, e.Message = envelope.Error, envelope.Message
		}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			e.RetryAfter = time.Duration(s) * time.Second
		}
		return e
	}
	if out == nil {
		return nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err = json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("outline %s: the response is not Outline's: %w", method, err)
	}
	if err = json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("outline %s: the response's data is not what was expected: %w", method, err)
	}
	return nil
}

// list reads every page of a list method. rows decodes one page's `data` and
// says how many rows it held.
func (c *Client) list(ctx context.Context, method string, body map[string]any, rows func(data json.RawMessage) (int, error)) error {
	for page := range maxPages {
		b := map[string]any{"offset": page * c.pageSize, "limit": c.pageSize}
		for k, v := range body {
			b[k] = v
		}
		var data json.RawMessage
		if err := c.call(ctx, method, b, &data); err != nil {
			return err
		}
		n, err := rows(data)
		if err != nil {
			return fmt.Errorf("outline %s: %w", method, err)
		}
		if n < c.pageSize {
			return nil
		}
	}
	return fmt.Errorf("%w: %s returned more than %d pages", ErrCutShort, method, maxPages)
}

// listOf reads every page of a method whose `data` is an array.
func listOf[T any](ctx context.Context, c *Client, method string, body map[string]any) ([]T, error) {
	var out []T
	err := c.list(ctx, method, body, func(data json.RawMessage) (int, error) {
		var page []T
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, err
		}
		out = append(out, page...)
		return len(page), nil
	})
	return out, err
}

// User is an Outline account.
type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	// Role is admin, member, viewer or guest.
	Role        string `json:"role"`
	IsSuspended bool   `json:"isSuspended"`
}

// Team is the workspace's settings, as auth.info presents them.
type Team struct {
	ID                     string `json:"id"`
	Name                   string `json:"name"`
	URL                    string `json:"url"`
	Sharing                bool   `json:"sharing"`
	MemberCollectionCreate bool   `json:"memberCollectionCreate"`
	DefaultUserRole        string `json:"defaultUserRole"`
	// Preferences hold only the keys a team set; `membersCanInvite` is
	// false when absent.
	Preferences map[string]any `json:"preferences"`
}

// MembersCanInvite is the `membersCanInvite` preference.
func (t Team) MembersCanInvite() bool {
	v, _ := t.Preferences["membersCanInvite"].(bool)
	return v
}

// AuthInfo is who the key acts as, and the workspace.
type AuthInfo struct {
	User User `json:"user"`
	Team Team `json:"team"`
}

// AuthInfo is auth.info.
func (c *Client) AuthInfo(ctx context.Context) (AuthInfo, error) {
	var out AuthInfo
	err := c.call(ctx, "auth.info", map[string]any{}, &out)
	return out, err
}

// Users is users.list with the filter `all`, which, to an admin, includes the
// suspended accounts.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	return listOf[User](ctx, c, "users.list", map[string]any{"filter": "all", "sort": "createdAt", "direction": "ASC"})
}

// UpdateRole is users.update_role.
func (c *Client) UpdateRole(ctx context.Context, userID, role string) error {
	return c.call(ctx, "users.update_role", map[string]any{"id": userID, "role": role}, nil)
}

// Suspend is users.suspend.
func (c *Client) Suspend(ctx context.Context, userID string) error {
	return c.call(ctx, "users.suspend", map[string]any{"id": userID}, nil)
}

// Activate is users.activate.
func (c *Client) Activate(ctx context.Context, userID string) error {
	return c.call(ctx, "users.activate", map[string]any{"id": userID}, nil)
}

// Group is an Outline group.
type Group struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MemberCount int    `json:"memberCount"`
}

// Groups is groups.list.
func (c *Client) Groups(ctx context.Context) ([]Group, error) {
	var out []Group
	err := c.list(ctx, "groups.list", map[string]any{"sort": "createdAt", "direction": "ASC"}, func(data json.RawMessage) (int, error) {
		var page struct {
			Groups []Group `json:"groups"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, err
		}
		out = append(out, page.Groups...)
		return len(page.Groups), nil
	})
	return out, err
}

// GroupMembers is groups.memberships: the ids of a group's members.
func (c *Client) GroupMembers(ctx context.Context, groupID string) ([]string, error) {
	var out []string
	err := c.list(ctx, "groups.memberships", map[string]any{"id": groupID}, func(data json.RawMessage) (int, error) {
		var page struct {
			GroupMemberships []struct {
				UserID string `json:"userId"`
			} `json:"groupMemberships"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, err
		}
		for _, m := range page.GroupMemberships {
			out = append(out, m.UserID)
		}
		return len(page.GroupMemberships), nil
	})
	return out, err
}

// CreateGroup is groups.create.
func (c *Client) CreateGroup(ctx context.Context, name, description string) (Group, error) {
	var out Group
	err := c.call(ctx, "groups.create", map[string]any{"name": name, "description": description}, &out)
	return out, err
}

// AddGroupUser is groups.add_user, always as an ordinary member: Outline
// resets an existing member to `member` when the permission is left out, so
// it is sent.
func (c *Client) AddGroupUser(ctx context.Context, groupID, userID string) error {
	return c.call(ctx, "groups.add_user", map[string]any{"id": groupID, "userId": userID, "permission": "member"}, nil)
}

// RemoveGroupUser is groups.remove_user.
func (c *Client) RemoveGroupUser(ctx context.Context, groupID, userID string) error {
	return c.call(ctx, "groups.remove_user", map[string]any{"id": groupID, "userId": userID}, nil)
}

// Collection is an Outline collection.
type Collection struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Permission is the workspace-wide permission, nil for a private
	// collection.
	Permission *string `json:"permission"`
	// OwnerID is set on a personal collection, which only its owner reads.
	OwnerID *string `json:"ownerId"`
}

// Personal reports a personal collection.
func (c Collection) Personal() bool { return c.OwnerID != nil && *c.OwnerID != "" }

// Collections is collections.list with `includeListOnly`, which to an admin
// lists every collection of the workspace (private ones it is not a member of
// included) except other people's personal collections.
func (c *Client) Collections(ctx context.Context) ([]Collection, error) {
	return listOf[Collection](ctx, c, "collections.list", map[string]any{"includeListOnly": true})
}

// CreateCollection is collections.create of a private collection: no
// workspace-wide permission.
func (c *Client) CreateCollection(ctx context.Context, name string) (Collection, error) {
	var out Collection
	err := c.call(ctx, "collections.create", map[string]any{"name": name, "sharing": false}, &out)
	return out, err
}

// Membership is one account's or group's permission on a collection or a
// document.
type Membership struct {
	UserID     string `json:"userId"`
	GroupID    string `json:"groupId"`
	Permission string `json:"permission"`
	// SourceID is set on a document grant inherited from a parent document.
	SourceID *string `json:"sourceId"`
}

func memberships(ctx context.Context, c *Client, method, field, id string) ([]Membership, error) {
	var out []Membership
	err := c.list(ctx, method, map[string]any{"id": id}, func(data json.RawMessage) (int, error) {
		var page map[string]json.RawMessage
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, err
		}
		var rows []Membership
		if raw, ok := page[field]; ok {
			if err := json.Unmarshal(raw, &rows); err != nil {
				return 0, err
			}
		}
		out = append(out, rows...)
		return len(rows), nil
	})
	return out, err
}

// CollectionMembers is collections.memberships: the direct members.
func (c *Client) CollectionMembers(ctx context.Context, collectionID string) ([]Membership, error) {
	return memberships(ctx, c, "collections.memberships", "memberships", collectionID)
}

// CollectionGroups is collections.group_memberships.
func (c *Client) CollectionGroups(ctx context.Context, collectionID string) ([]Membership, error) {
	return memberships(ctx, c, "collections.group_memberships", "groupMemberships", collectionID)
}

// AddCollectionGroup is collections.add_group, which changes an existing
// grant's permission in place.
func (c *Client) AddCollectionGroup(ctx context.Context, collectionID, groupID, permission string) error {
	return c.call(ctx, "collections.add_group", map[string]any{"id": collectionID, "groupId": groupID, "permission": permission}, nil)
}

// RemoveCollectionGroup is collections.remove_group.
func (c *Client) RemoveCollectionGroup(ctx context.Context, collectionID, groupID string) error {
	return c.call(ctx, "collections.remove_group", map[string]any{"id": collectionID, "groupId": groupID}, nil)
}

// RemoveCollectionUser is collections.remove_user.
func (c *Client) RemoveCollectionUser(ctx context.Context, collectionID, userID string) error {
	return c.call(ctx, "collections.remove_user", map[string]any{"id": collectionID, "userId": userID}, nil)
}

// Document is the part of a document a share scan reads.
type Document struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	CollectionID string `json:"collectionId"`
}

// Documents is documents.list of one collection's published documents.
func (c *Client) Documents(ctx context.Context, collectionID string) ([]Document, error) {
	return listOf[Document](ctx, c, "documents.list", map[string]any{"collectionId": collectionID, "sort": "createdAt", "direction": "ASC"})
}

// DocumentMembers is documents.memberships: the accounts a document is
// shared with directly (an inherited grant has a source and is left out).
func (c *Client) DocumentMembers(ctx context.Context, documentID string) ([]Membership, error) {
	rows, err := memberships(ctx, c, "documents.memberships", "memberships", documentID)
	return direct(rows), err
}

// DocumentGroups is documents.group_memberships, direct grants only.
func (c *Client) DocumentGroups(ctx context.Context, documentID string) ([]Membership, error) {
	rows, err := memberships(ctx, c, "documents.group_memberships", "groupMemberships", documentID)
	return direct(rows), err
}

func direct(rows []Membership) []Membership {
	out := rows[:0]
	for _, r := range rows {
		if r.SourceID == nil || *r.SourceID == "" {
			out = append(out, r)
		}
	}
	return out
}

// Integration is one integration. For a Slack `post` integration Outline
// shows the channel's name and never the Slack workspace it is in.
type Integration struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Service      string         `json:"service"`
	CollectionID string         `json:"collectionId"`
	Settings     map[string]any `json:"settings"`
}

// Integrations is integrations.list of the `post` integrations.
func (c *Client) Integrations(ctx context.Context) ([]Integration, error) {
	return listOf[Integration](ctx, c, "integrations.list", map[string]any{"type": "post"})
}
