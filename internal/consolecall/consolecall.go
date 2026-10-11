// Package consolecall is the plumbing for the console's provider operations
// once they live in the provider's module (docs/decisions/0071, v1.76 D-v176-1
// b): the console authenticates a person, snapshots who they are, and asks the
// module to do the work as that snapshot.
//
// A call is a proto-JSON envelope, `{caller:{actor,roles,workspaces}, msg}`,
// over [modcall]. The module trusts the snapshot because only the caller class
// `console` may reach a console method: [Handle] registers with
// `Allow("console")` and nothing else, and [Violations] says which methods of a
// server do not. The transport names the class, never the envelope, so a
// payload cannot claim to be the console.
//
// A refusal the person should read is a [Problem] in the reply, not a transport
// error: transport errors carry a code and no detail, since the caller is
// another trust domain.
package consolecall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/modcall"
)

// Class is the caller class of the console, the only one a console method
// answers.
const Class = "console"

// MethodPrefix starts the name of every console method, so a lint can tell
// them from the module's other methods.
const MethodPrefix = "console."

// The modules a console operation can reach.
const (
	ModuleGitHub = "github"
	ModuleSlack  = "slack"
	ModuleGoogle = "google"
)

// ListMaxBytes is the bound of a method that lists: above
// [modcall.DefaultMaxBytes], well under [modcall.HardMaxBytes].
const ListMaxBytes = 2 << 20

// Caller is the snapshot of who the console authorized, taken once per request.
// The module acts on it and re-checks nothing against the person's session.
type Caller struct {
	// Actor is who to write down: the address, else the subject.
	Actor string `json:"actor"`
	// Roles are the installation-wide roles held (the role and the roles it
	// implies are not expanded: [access.Role.Implies] does that).
	Roles []string `json:"roles,omitempty"`
	// Workspaces are the roles held over one directory workspace each.
	Workspaces map[string]string `json:"workspaces,omitempty"`
}

// Snapshot takes the caller of an authorized identity.
func Snapshot(id access.Identity) Caller {
	c := Caller{Actor: id.Who()}
	if id.Role != access.RoleNone {
		c.Roles = []string{string(id.Role)}
	}
	if len(id.Scopes) > 0 {
		c.Workspaces = make(map[string]string, len(id.Scopes))
		for w, r := range id.Scopes {
			if r != access.RoleNone {
				c.Workspaces[w] = string(r)
			}
		}
	}
	return c
}

// Identity is the snapshot as an identity for the module's own authorization:
// the strongest installation-wide role in Roles, and Workspaces as scopes.
func (c Caller) Identity() access.Identity {
	id := access.Identity{Email: c.Actor}
	for _, r := range c.Roles {
		if role := access.Role(r); role.Implies(id.Role) && role.Implies(access.RoleViewer) {
			id.Role = role
		}
	}
	if len(c.Workspaces) > 0 {
		id.Scopes = make(map[string]access.Role, len(c.Workspaces))
		for w, r := range c.Workspaces {
			id.Scopes[w] = access.Role(r)
		}
	}
	return id
}

// Envelope is a request on the wire: the caller and one proto message as
// proto-JSON.
type Envelope struct {
	Caller Caller          `json:"caller"`
	Msg    json.RawMessage `json:"msg,omitempty"`
}

// Problem is a refusal or a failure a person should read, told as data.
type Problem struct {
	// Summary is one line.
	Summary string `json:"summary"`
	// Detail is what went wrong, safe to show the signed-in person.
	Detail string `json:"detail,omitempty"`
	// Hints are what to try.
	Hints []string `json:"hints,omitempty"`
}

// Error implements error, so a handler can return a Problem.
func (p *Problem) Error() string {
	if p.Detail == "" {
		return p.Summary
	}
	return p.Summary + ": " + p.Detail
}

// Reply is the answer on the wire: a proto message, or a Problem.
type Reply struct {
	Msg     json.RawMessage `json:"msg,omitempty"`
	Problem *Problem        `json:"problem,omitempty"`
}

var marshal = protojson.MarshalOptions{UseProtoNames: true}

// Wrap is the envelope of a request.
func Wrap(c Caller, msg proto.Message) (Envelope, error) {
	raw, err := marshal.Marshal(msg)
	if err != nil {
		return Envelope{}, fmt.Errorf("console call: the request cannot be encoded: %w", err)
	}
	return Envelope{Caller: c, Msg: raw}, nil
}

// Unwrap reads the message of an envelope into msg. Unknown fields are refused:
// a console and a module of the same release agree on the message.
func (e Envelope) Unwrap(msg proto.Message) error {
	if len(e.Msg) == 0 {
		return nil
	}
	return protojson.UnmarshalOptions{}.Unmarshal(e.Msg, msg)
}

// Proxy calls one console method of a module as caller and returns its reply.
// A Problem the module told comes back as a *[Problem] error; any other failure
// is the transport's [modcall.Error].
func Proxy[Req, Resp proto.Message](ctx context.Context, c modcall.Caller, module, method string, caller Caller, req Req) (Resp, error) {
	var zero Resp
	env, err := Wrap(caller, req)
	if err != nil {
		return zero, err
	}
	reply, err := modcall.Do[Envelope, Reply](ctx, c, module, method, env)
	if err != nil {
		return zero, err
	}
	if reply.Problem != nil {
		return zero, reply.Problem
	}
	out, ok := zero.ProtoReflect().Type().New().Interface().(Resp)
	if !ok {
		return zero, fmt.Errorf("console call %s.%s: the response type is not a message", module, method)
	}
	if len(reply.Msg) > 0 {
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(reply.Msg, out); err != nil {
			return zero, fmt.Errorf("console call %s.%s: the response is not valid: %w", module, method, err)
		}
	}
	return out, nil
}

// Handle registers a console method on a module's server. It answers the
// caller class [Class] and no other, whatever opts say about who may call: an
// [modcall.Allow] among them is overridden. A *[Problem] the function returns
// is told to the console as the reply's Problem; any other error is the
// transport's, with no detail.
func Handle[Req, Resp proto.Message](s *modcall.Server, method string, fn func(ctx context.Context, caller Caller, req Req) (Resp, error), opts ...modcall.Option) {
	if !strings.HasPrefix(method, MethodPrefix) {
		panic("consolecall: " + s.Module() + "." + method + " is a console method and must start with " + MethodPrefix)
	}
	opts = append(slices.Clone(opts), modcall.Allow(Class))
	modcall.Handle(s, method, func(ctx context.Context, env Envelope) (Reply, error) {
		var req Req
		req, ok := req.ProtoReflect().Type().New().Interface().(Req)
		if !ok {
			return Reply{}, modcall.Coded(modcall.CodeInternal, "")
		}
		if err := env.Unwrap(req); err != nil {
			return Reply{}, modcall.Coded(modcall.CodeBadRequest, "the request is not valid")
		}
		resp, err := fn(ctx, env.Caller, req)
		var p *Problem
		if errors.As(err, &p) {
			return Reply{Problem: p}, nil
		}
		if err != nil {
			return Reply{}, err
		}
		raw, err := marshal.Marshal(resp)
		if err != nil {
			return Reply{}, modcall.Coded(modcall.CodeInternal, "")
		}
		return Reply{Msg: raw}, nil
	}, opts...)
}

// List is the option of a method that lists: its bound is [ListMaxBytes].
func List() modcall.Option { return modcall.MaxBytes(ListMaxBytes) }

// Violations lists the methods of s that break the console rule: a method
// named like a console method that answers anyone but [Class], and a method
// that answers [Class] together with another class. A console method must
// answer the console alone because it trusts the caller snapshot; another
// class could send any snapshot it liked.
func Violations(s *modcall.Server) []string {
	var out []string
	for _, m := range s.Methods() {
		console := strings.HasPrefix(m.Name, MethodPrefix)
		only := len(m.Allow) == 1 && m.Allow[0] == Class
		switch {
		case console && !only:
			out = append(out, fmt.Sprintf("%s.%s must be Allow(%q) only, is %v", s.Module(), m.Name, Class, m.Allow))
		case !console && slices.Contains(m.Allow, Class) && len(m.Allow) == 1:
			out = append(out, fmt.Sprintf("%s.%s answers only the console and must start with %q", s.Module(), m.Name, MethodPrefix))
		}
	}
	sort.Strings(out)
	return out
}

// Callers is how the console reaches each provider module: one
// [modcall.Caller] each, sending as the class [Class].
type Callers struct {
	GitHub, Slack, Google modcall.Caller
}

// OrLocal fills each caller that is nil with the in-process servers, as the
// console. A deployment that has not split a module out calls it there.
func (c Callers) OrLocal(l modcall.Local) Callers {
	local := l.As(Class)
	if c.GitHub == nil {
		c.GitHub = local
	}
	if c.Slack == nil {
		c.Slack = local
	}
	if c.Google == nil {
		c.Google = local
	}
	return c
}
