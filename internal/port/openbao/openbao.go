// Package openbao is the [port.Export] adapter over an OpenBao (or Vault) KV
// version 2 mount: a copy of a secret is one key under the mount, written
// with the HTTP API and nothing else.
//
// It logs in as the service and holds no long-lived credential. Two methods
// share one request, `POST auth/<mount>/login {role, jwt}`: `kubernetes` (the
// Kubernetes auth method, whose JWT is the pod's ServiceAccount token) and
// `jwt` (the JWT/OIDC method, whose JWT is any token the deployment can read:
// a projected ServiceAccount token on Kubernetes, or the web identity token
// AWS issues a Lambda by outbound federation). The token a login returns is
// kept until most of its lease has passed, per OpenBao namespace, because a
// login inside a namespace opens a token for that namespace alone.
//
// The writes are shaped for the way copies are consumed (docs/decisions/0034):
//
//   - ExportReplace is `POST data/<path>`: the key holds exactly the
//     properties. A whole-secret copy.
//   - ExportPatch is `PATCH data/<path>` with a JSON merge patch: the given
//     properties are set and the others are left, atomically on the server. A
//     key that does not exist yet is created by a POST.
//
// Both read the key first and write nothing when it already holds what would
// be written, so the hourly reconcile makes no new KV version and still puts
// back what somebody changed. The policy the service needs is therefore
// `read`, `create`, `update` and `patch` on `<mount>/data/<prefix>/*`; Delete
// removes every version through `<mount>/metadata/<path>` and needs `delete`
// there, which the exporter never asks for.
//
// Nothing here logs or returns a value: an error names the method, the path
// and the status, and the server's own error text, which carries no data.
package openbao

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"

	"github.com/truvity/sluis/internal/port"
)

// Store is the Export adapter.
type Store struct{ *Client }

var _ port.Export = (*Store)(nil)

// New validates the configuration and returns the adapter. It does not
// connect: an OpenBao that is down at start must not stop the service, since
// an export is never a dependency.
func New(cfg Config) (*Store, error) {
	c, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Store{c}, nil
}

// Put implements [port.Export].
func (s *Store) Put(ctx context.Context, target port.ExportTarget, properties map[string]string, mode port.ExportMode) error {
	if err := port.CheckExport(target, properties, mode); err != nil {
		return err
	}
	ns := s.ns(target)
	have, found, err := s.read(ctx, ns, target.Path)
	if err != nil {
		return err
	}
	switch {
	case found && same(have, properties, mode):
		return nil
	case found && mode == port.ExportPatch:
		status, body, err := s.call(ctx, ns, "PATCH", s.dataPath(target.Path), "application/merge-patch+json", payload(properties))
		if err != nil {
			return err
		}
		if status != http.StatusNotFound { // gone since the read: create it below
			return answer("patch", target, status, body)
		}
	}
	status, body, err := s.call(ctx, ns, "POST", s.dataPath(target.Path), "application/json", payload(properties))
	if err != nil {
		return err
	}
	return answer("write", target, status, body)
}

// Delete implements [port.Export]: every version of the key, and its
// metadata.
func (s *Store) Delete(ctx context.Context, target port.ExportTarget) error {
	if err := port.CheckExportPath(target.Path); err != nil {
		return err
	}
	status, body, err := s.call(ctx, s.ns(target), "DELETE", s.mount+"/metadata/"+target.Path, "", nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return nil
	}
	return answer("delete", target, status, body)
}

func (s *Store) ns(target port.ExportTarget) string {
	if target.Namespace != "" {
		return target.Namespace
	}
	return s.namespace
}

func (s *Store) dataPath(path string) string { return s.mount + "/data/" + path }

func payload(properties map[string]string) []byte {
	raw, _ := json.Marshal(map[string]any{"data": properties}) // strings only
	return raw
}

// same reports whether writing properties in the mode would change nothing.
func same(have, properties map[string]string, mode port.ExportMode) bool {
	if mode == port.ExportReplace {
		return maps.Equal(have, properties)
	}
	for k, v := range properties {
		if cur, ok := have[k]; !ok || cur != v {
			return false
		}
	}
	return true
}

// read returns the current version of a key. A key that is absent, or whose
// current version is deleted, is not found.
func (s *Store) read(ctx context.Context, ns, path string) (map[string]string, bool, error) {
	status, body, err := s.call(ctx, ns, "GET", s.dataPath(path), "", nil)
	if err != nil {
		return nil, false, err
	}
	switch status {
	case http.StatusOK:
		var out struct {
			Data struct {
				Data map[string]any `json:"data"`
			} `json:"data"`
		}
		if err = json.Unmarshal(body, &out); err != nil {
			return nil, false, fmt.Errorf("%w: the answer to a read of %s is not JSON", port.ErrUnavailable, path)
		}
		have := make(map[string]string, len(out.Data.Data))
		for k, v := range out.Data.Data {
			text, ok := v.(string)
			if !ok {
				// A property that is not text is never what this service wrote:
				// the key is different, whatever else it holds.
				text = fmt.Sprint(v)
			}
			have[k] = text
		}
		return have, true, nil
	case http.StatusNotFound:
		return nil, false, nil
	default:
		return nil, false, answer("read", port.ExportTarget{Path: path}, status, body)
	}
}

// answer turns a status into an error. A 5xx, a 429 and a sealed or standby
// server are the store being down; any other refusal is the configuration's
// (a role, a policy, a path) and is said as it is.
func answer(what string, target port.ExportTarget, status int, body []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}
	detail := serverError(body)
	switch {
	case status >= 500, status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s %s: status %d%s", port.ErrUnavailable, what, target, status, detail)
	default:
		return fmt.Errorf("openbao: %s %s: status %d%s", what, target, status, detail)
	}
}

// serverError is the `errors` of an OpenBao error answer, which names the
// refusal and carries no data. Anything else is dropped.
func serverError(body []byte) string {
	var out struct {
		Errors []string `json:"errors"`
	}
	if json.Unmarshal(body, &out) != nil || len(out.Errors) == 0 {
		return ""
	}
	text := strings.Join(out.Errors, "; ")
	if len(text) > 200 {
		text = text[:200]
	}
	return ": " + text
}
