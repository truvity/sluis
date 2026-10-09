package issuer

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/policy"
)

// The `resource` parameter, claimed in front of the library.
//
// RFC 8707 lets a client say what it wants a token FOR, and the Model
// Context Protocol requires its clients to send it. The library does not
// model it on an authorization request -- [oidc.AuthRequest] has no such
// field -- and its decoder ignores unknown parameters, so without this
// the parameter is silently dropped: a client asks for a token scoped to
// one service and is given one scoped to itself, with nothing said.
//
// Silence is the part that had to go. RFC 8707 says an authorization
// server that cannot honour a resource indicator answers
// `invalid_target`, and a refusal at the moment of the mistake is worth
// more than a token that fails later somewhere else for a reason nobody
// connects to this request.
//
// Claimed in front of the library rather than inside it, the way an
// installation token for a catalogue App already is: the library owns the
// flow, and what it does not model is read before it.

// resourceKey carries the validated resource down to CreateAuthRequest,
// which is the first place the library lets us write anything down.
type resourceContextKey struct{}

// resourceFromContext is the resource this request named, if any.
func resourceFromContext(ctx context.Context) string {
	value, _ := ctx.Value(resourceContextKey{}).(string)
	return value
}

// withResource carries a validated resource indicator on a request.
func withResource(ctx context.Context, resource string) context.Context {
	return context.WithValue(ctx, resourceContextKey{}, resource)
}

// resourceIndicators validates `resource` on the authorization and token
// endpoints, and refuses one this installation does not declare.
//
// One indicator, not many. RFC 8707 permits several and then leaves the
// authorization server to decide what a token for several audiences
// means; this issuer's answer is that it means nothing anybody should
// rely on, so more than one is refused rather than silently narrowed to
// the first.
func resourceIndicators(resources func(string) (policy.Resource, bool), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != authorizePath && !tokenPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		// The token endpoint carries its parameters in the body, and
		// ParseForm leaves them readable by whatever reads them next.
		if err := r.ParseForm(); err != nil {
			next.ServeHTTP(w, r)
			return
		}

		asked := r.Form["resource"]
		switch len(asked) {
		case 0:
			// The ordinary case: a client that names no resource gets a
			// token for itself, exactly as before.
			next.ServeHTTP(w, r)
			return
		case 1:
		default:
			refuseTarget(w, r, "more than one `resource` was asked for; this issuer mints a token for one resource at a time")
			return
		}

		wanted := asked[0]
		if err := validateIndicator(wanted); err != nil {
			refuseTarget(w, r, err.Error())
			return
		}
		// The scheme and the host are case-insensitive (RFC 3986 6.2.2.1,
		// RFC 8707), so `HTTPS://MCP.Example` names the same service as
		// `https://mcp.example`; the path is not folded. What was
		// declared as spelled is still matched, so a policy that already
		// holds a mixed-case id keeps working.
		if folded := policy.CanonicalResourceID(wanted); folded != wanted {
			if _, ok := resources(folded); ok {
				wanted = folded
			}
		}
		// Everything else stays exact: a trailing slash is part of the
		// path, so `https://mcp.example` and `https://mcp.example/` are
		// different resources, as the policy holds them to be.
		if _, ok := resources(wanted); !ok {
			// Naming it, because the alternative is somebody comparing
			// two URLs by eye for an afternoon.
			refuseTarget(w, r, fmt.Sprintf("%q is not a resource this installation declares; "+
				"a resource indicator is matched exactly, so a trailing slash or a different scheme "+
				"is a different resource", wanted))
			return
		}

		next.ServeHTTP(w, r.WithContext(withResource(r.Context(), wanted)))
	})
}

// validateIndicator holds what a client sends to the same shape the
// policy holds a declared id to, so the comparison between them is
// between two things of the same kind.
func validateIndicator(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("`resource` is not a URI: %v", err)
	}
	if !parsed.IsAbs() {
		return fmt.Errorf("`resource` must be an absolute URI naming the service a token is for")
	}
	if parsed.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("`resource` must carry no fragment (RFC 8707)")
	}
	return nil
}

// refuseTarget answers as RFC 8707 says to, in whichever form the caller
// reads: a page for a browser, the OAuth error for a program.
func refuseTarget(w http.ResponseWriter, r *http.Request, description string) {
	refuseWith(w, r, string(oidc.InvalidTarget), description)
}

// refuseRequest answers an authorization request that is malformed with
// `invalid_request`, in the same two forms.
func refuseRequest(w http.ResponseWriter, r *http.Request, description string) {
	refuseWith(w, r, string(oidc.InvalidRequest), description)
}

func refuseWith(w http.ResponseWriter, r *http.Request, code string, description string) {
	if wantsHTML(r) {
		_ = writePage(w, http.StatusBadRequest, "That sign-in request was not valid",
			`<p>`+html.EscapeString(description)+`</p>
	<p class="note">Nothing was signed in, and this is not something you can fix from here.
	Whoever runs the application that sent you will need to correct how it asks.</p>`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             code,
		"error_description": description,
	})
}

// defaultScope supplies `scope=openid` on an authorization request that
// names none.
//
// The library refuses an empty scope outright (`invalid_request`, "The
// scope of your request is missing"). A pure OAuth client -- a Model
// Context Protocol client reading a resource server whose RFC 9728
// metadata lists no scope sends none -- is blocked there for no reason
// that serves anybody: `scope` is OPTIONAL in RFC 6749, and an
// authorization server may apply a default.
//
// `openid` is the default, whether or not the request names a resource:
// this is an OpenID provider, and a request that asks for nothing gets
// the one scope that is always valid here. The access token is the same
// either way; `openid` only also makes an ID token available. A request
// that does carry a scope is never touched.
func defaultScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == authorizePath && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
			if err := r.ParseForm(); err == nil && strings.TrimSpace(r.Form.Get("scope")) == "" {
				// The library reads r.Form, which ParseForm has filled.
				r.Form.Set("scope", oidc.ScopeOpenID)
			}
		}

		next.ServeHTTP(w, r)
	})
}

// resourceWithinGrant holds a token request's `resource` to the grant's
// (RFC 8707 2.2): the request may name the resource the grant was
// authorized for, or none, and nothing else. Naming another is
// `invalid_target`, never a token for the grant's resource under a
// different name. This issuer mints for one resource at a time, so the
// subset of one is that one or nothing.
func resourceWithinGrant(ctx context.Context, request op.TokenRequest) error {
	asked := resourceFromContext(ctx)
	if asked == "" {
		return nil
	}
	if granted := resourceOf(request); asked != granted {
		return &oidc.Error{
			ErrorType:   oidc.InvalidTarget,
			Description: "`resource` is not within the resource this grant was authorized for",
		}
	}
	return nil
}
