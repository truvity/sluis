// Package openbao is the secrets adapter over an OpenBao (or Vault) KV version
// 2 mount, written with the HTTP API and nothing else (see [Secrets]).
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
// Nothing here logs or returns a value: an error names the method, the path
// and the status, and the server's own error text, which carries no data.
package openbao

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/truvity/sluis/internal/port"
)

// target is where a request went, for an error: the namespace and the path.
type target struct{ Namespace, Path string }

func (t target) String() string {
	if t.Namespace == "" {
		return t.Path
	}
	return t.Namespace + "/" + t.Path
}

// checkPath refuses a path that is empty, begins or ends with a slash, holds
// an empty or relative segment, or a character a URL path would change.
func checkPath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return fmt.Errorf("%w: path %q", port.ErrUnsupported, p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, "?#%\\ \t\n*") {
			return fmt.Errorf("%w: path %q", port.ErrUnsupported, p)
		}
	}
	return nil
}

// answer turns a status into an error. A 5xx, a 429 and a sealed or standby
// server are the store being down; any other refusal is the configuration's
// (a role, a policy, a path) and is said as it is.
func answer(what string, t target, status int, body []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}
	detail := serverError(body)
	switch {
	case status >= 500, status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s %s: status %d%s", port.ErrUnavailable, what, t, status, detail)
	default:
		return fmt.Errorf("openbao: %s %s: status %d%s", what, t, status, detail)
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
