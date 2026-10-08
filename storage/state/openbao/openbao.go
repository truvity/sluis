// Package openbao is a [state.Store] over a KV version 2 mount of an OpenBao
// (or Vault).
//
// # Mapping
//
// A key is the KV path mount/prefix/key. A stored document is a JSON object,
// and its top-level members become the KV secret's keys, each value kept as
// JSON ({"name":"a","n":{"x":[1]}} is the secret name="a", n={"x":[1]}), so a
// person reading the secret in the UI sees the fields, and a field added to a
// document needs no migration. Reading gives the members back as one object,
// with its keys sorted and no insignificant white space.
//
//	Get        GET    <mount>/data/<path>
//	GetRev     GET    <mount>/data/<path>?version=N
//	Put        POST   <mount>/data/<path> with options.cas
//	List       LIST   <mount>/metadata/<prefix>/
//	Delete     GET, then DELETE <mount>/metadata/<path>
//
// Rev is the KV version number as a decimal string, and Item.Modified is that
// version's created_time. Item.Previous is version-1 when the version is
// above 1, without asking the server whether it is still kept: Rotating
// treats a version the server no longer has as "no previous". A deleted key
// starts again at version 1, so after Delete a Rev can repeat; do not hold a
// Rev across a Delete.
//
// # Conditional Put
//
// Put sends the KV check-and-set: options.cas is 0 for "the key must not
// exist" (ifRev "") and N for "the current version is N". The server
// compares and writes in one step, so a lost race is [state.ErrConflict],
// never an overwrite. An ifRev that is not a version number is a conflict. A
// key whose latest version was soft-deleted from outside (kv delete) still
// has a current version for CAS purposes and reads as not found: Delete it
// (which removes its metadata) before creating it again.
//
// # Versions kept
//
// [state.Value.Rotating] reads the version before the current one, so the
// mount must keep at least two. [Open] reads the mount's configuration and
// refuses to start when max_versions is 1. (0 means the server's default,
// 10.) A secret can carry its own max_versions in its metadata, which
// overrides the mount's and is not checked here. With [Config.AssumeVersions]
// the check is skipped, for a policy that cannot read <mount>/config.
//
// # Policy
//
// The package documentation of [github.com/truvity/sluis/storage] gives the
// ACL policy a process needs. In short, on <mount>/data/<prefix>/* create,
// update, read; on <mount>/metadata/<prefix>/* read, list, delete; and read
// on <mount>/config for the check above.
package openbao

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/truvity/sluis/storage/openbao"
	"github.com/truvity/sluis/storage/state"
)

// DefaultMount is the KV mount used when [Config.Mount] is empty.
const DefaultMount = "kv"

// Config selects the mount and the prefix below it.
type Config struct {
	// Mount is the KV version 2 mount, for example "kv" or "team/kv".
	Mount string
	// Prefix is the path below the mount of the root store, for example
	// "sluis/state".
	Prefix string
	// AssumeVersions skips the max_versions check of [Open].
	AssumeVersions bool
}

// ErrTooFewVersions: the mount keeps a single version per key, so the
// previous version that Rotating reads would be gone.
var ErrTooFewVersions = errors.New("openbao: the KV mount keeps one version per key; max_versions must be at least 2")

type store struct {
	c      *openbao.Client
	mount  string // no slashes at the ends
	prefix string // "" or ends with "/"
}

// New returns the root store of cfg over c without any I/O. [Open] is the
// same plus the check of the mount.
func New(c *openbao.Client, cfg Config) state.Store {
	mount := strings.Trim(cfg.Mount, "/")
	if mount == "" {
		mount = DefaultMount
	}
	return &store{c: c, mount: mount, prefix: dir(cfg.Prefix)}
}

// Open checks that cfg.Mount is a KV version 2 mount that keeps at least two
// versions per key, and returns the store.
func Open(ctx context.Context, c *openbao.Client, cfg Config) (state.Store, error) {
	s := New(c, cfg).(*store)
	if cfg.AssumeVersions {
		return s, nil
	}
	resp, err := c.Request(ctx, http.MethodGet, openbao.EscapePath(s.mount)+"/config", nil)
	switch {
	case openbao.Status(err) == http.StatusNotFound:
		return nil, fmt.Errorf("openbao: %q is not a KV version 2 mount (it has no config)", s.mount)
	case err != nil:
		return nil, fmt.Errorf("openbao: read the configuration of mount %q (needs read on %s/config, or set AssumeVersions): %w",
			s.mount, s.mount, err)
	}
	var conf struct {
		MaxVersions int `json:"max_versions"`
	}
	if err := json.Unmarshal(resp.Data, &conf); err != nil {
		return nil, fmt.Errorf("openbao: the configuration of mount %q is not what KV version 2 returns: %w", s.mount, err)
	}
	if conf.MaxVersions == 1 {
		return nil, fmt.Errorf("%w (mount %q)", ErrTooFewVersions, s.mount)
	}
	return s, nil
}

func dir(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	return p + "/"
}

func (s *store) Child(prefix string, _ ...state.Option) state.Store {
	c := *s
	c.prefix = s.prefix + dir(prefix)
	return &c
}

func (s *store) secretPath(key string) (string, error) {
	if err := state.ValidateKey(key); err != nil {
		return "", err
	}
	return s.prefix + key, nil
}

func (s *store) dataURL(p string) string {
	return openbao.EscapePath(s.mount) + "/data/" + openbao.EscapePath(p)
}

func (s *store) metaURL(p string) string {
	return openbao.EscapePath(s.mount) + "/metadata/" + openbao.EscapePath(p)
}

func (s *store) Get(ctx context.Context, key string) (state.Item, error) {
	return s.read(ctx, key, "")
}

func (s *store) GetRev(ctx context.Context, key string, rev state.Rev) (state.Item, error) {
	if n, err := strconv.Atoi(string(rev)); err != nil || n < 1 {
		return state.Item{}, state.ErrNotFound // not a version this store makes
	}
	return s.read(ctx, key, rev)
}

type readData struct {
	Data     map[string]json.RawMessage `json:"data"`
	Metadata struct {
		Version     int       `json:"version"`
		CreatedTime time.Time `json:"created_time"`
	} `json:"metadata"`
}

func (s *store) read(ctx context.Context, key string, rev state.Rev) (state.Item, error) {
	p, err := s.secretPath(key)
	if err != nil {
		return state.Item{}, err
	}
	u := s.dataURL(p)
	if rev != "" {
		u += "?version=" + string(rev)
	}
	resp, err := s.c.Request(ctx, http.MethodGet, u, nil)
	if openbao.Status(err) == http.StatusNotFound {
		return state.Item{}, state.ErrNotFound // missing, soft-deleted or destroyed
	}
	if err != nil {
		return state.Item{}, err
	}
	var d readData
	if err := json.Unmarshal(resp.Data, &d); err != nil || d.Data == nil || d.Metadata.Version < 1 {
		return state.Item{}, fmt.Errorf("openbao: %s: not a KV version 2 answer (is %q a KV v2 mount?)", p, s.mount)
	}
	value, err := json.Marshal(d.Data)
	if err != nil {
		return state.Item{}, err
	}
	it := state.Item{Value: value, Rev: versionRev(d.Metadata.Version), Modified: d.Metadata.CreatedTime}
	if d.Metadata.Version > 1 {
		it.Previous = versionRev(d.Metadata.Version - 1)
	}
	return it, nil
}

func versionRev(v int) state.Rev { return state.Rev(strconv.Itoa(v)) }

func (s *store) Put(ctx context.Context, key string, value []byte, ifRev state.Rev) (state.Rev, error) {
	p, err := s.secretPath(key)
	if err != nil {
		return "", err
	}
	if err := state.ValidateObject(value); err != nil {
		return "", err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(value, &fields); err != nil || fields == nil {
		return "", state.ErrNotObject
	}
	cas := 0
	if ifRev != "" {
		n, err := strconv.Atoi(string(ifRev))
		if err != nil || n < 1 {
			return "", state.ErrConflict // not a version this store makes
		}
		cas = n
	}
	resp, err := s.c.Request(ctx, http.MethodPost, s.dataURL(p), map[string]any{
		"data":    fields,
		"options": map[string]any{"cas": cas},
	})
	if oe, ok := openbao.AsError(err); ok && oe.Status == http.StatusBadRequest && oe.Says("check-and-set") {
		return "", state.ErrConflict
	}
	if err != nil {
		return "", err
	}
	var out struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil || out.Version < 1 {
		return "", fmt.Errorf("openbao: put %s: the answer has no version", p)
	}
	return versionRev(out.Version), nil
}

func (s *store) Delete(ctx context.Context, key string) error {
	p, err := s.secretPath(key)
	if err != nil {
		return err
	}
	// DELETE on metadata answers 204 for a path that is not there; the read
	// tells a missing key from a deleted one.
	_, err = s.c.Request(ctx, http.MethodGet, s.metaURL(p), nil)
	if openbao.Status(err) == http.StatusNotFound {
		return state.ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = s.c.Request(ctx, http.MethodDelete, s.metaURL(p), nil)
	return err
}

func (s *store) List(ctx context.Context) ([]string, error) {
	resp, err := s.c.Request(ctx, "LIST", s.metaURL(s.prefix), nil)
	if openbao.Status(err) == http.StatusNotFound {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		return nil, fmt.Errorf("openbao: list %s: %w", s.prefix, err)
	}
	names := []string{}
	for _, k := range out.Keys {
		if !strings.HasSuffix(k, "/") { // a folder: keys nested deeper are not listed
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return names, nil
}
