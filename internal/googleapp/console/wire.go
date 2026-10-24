package console

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/modcall"
)

// Module is the module the methods belong to.
const Module = consolecall.ModuleGoogle

// The console methods, answered to the caller class `console` alone
// ([consolecall.MethodPrefix]), and the two the issuer calls.
const (
	MethodListWorkspaces   = "console.list_workspaces"
	MethodBeginConnect     = "console.begin_connect"
	MethodReconnect        = "console.reconnect"
	MethodCompleteConsent  = "console.complete_consent"
	MethodUploadKey        = "console.upload_key"
	MethodSetServedDomains = "console.set_served_domains"
	MethodSetSyncedGroups  = "console.set_synced_groups"
	MethodProbe            = "console.probe"
	MethodRefresh          = "console.refresh"
	MethodDisconnect       = "console.disconnect"

	// MethodIdentify turns a sign-in's code into the address that authenticated.
	// It answers the caller class `issuer` and no other: it needs the OAuth
	// client secret, and the console has no business with a sign-in.
	MethodIdentify = "identify"
	// MethodSignInURL is where the browser goes to sign in. It answers the
	// issuer alone, like [MethodIdentify]: the module holds the OAuth client, so
	// the issuer holds nothing to build the address from.
	MethodSignInURL = "sign_in_url"
)

// CallerIssuer is the caller class of the issuer, the only one that signs
// somebody in.
const CallerIssuer = "issuer"

// MaxKeyBytes bounds a service-account key uploaded through the console. A key
// file is about 2 KB; the bound is generous and exists so that nothing the
// issuer forwards can be large.
const MaxKeyBytes = 64 << 10

// The codes the module answers with besides modcall's own: each is a refusal
// of the person's request, and its message is safe to show them.
const (
	CodeInvalidArgument    = "invalid_argument"
	CodeNotFound           = "not_found"
	CodeFailedPrecondition = "failed_precondition"
	CodeUnimplemented      = "unimplemented"
	CodeUnauthenticated    = "unauthenticated"
)

// RefusalText is what a caller who may not is told, whatever the reason. The
// reason is the module's log's.
const RefusalText = "not authorised"

// BeginRequest starts a consent. State is the issuer's signed state, which the
// provider hands back at the callback; the module never signs one. Backend names
// the directory for a new connection; Bind names the workspace being
// reconnected (and Backend is then the one it was connected with).
type BeginRequest struct {
	Backend string `json:"backend,omitempty"`
	Bind    string `json:"bind,omitempty"`
	State   string `json:"state"`
}

// BeginResponse is where the browser goes to consent.
type BeginResponse struct {
	URL string `json:"url"`
}

// CompleteRequest is the consent's callback: the provider's code, which is
// single-use, and the workspace being reconnected, if any.
type CompleteRequest struct {
	Backend string `json:"backend"`
	Code    string `json:"code"`
	Bind    string `json:"bind,omitempty"`
}

// CompleteResponse is the workspace the consent connected.
type CompleteResponse struct {
	Workspace string `json:"workspace"`
	Backend   string `json:"backend"`
}

type identifyRequest struct {
	Code string `json:"code"`
	// State is the callback's, which the sign-in's nonce is derived from.
	State string `json:"state,omitempty"`
}

type identifyResponse struct {
	Email string `json:"email"`
}

type signInURLRequest struct {
	State string `json:"state"`
}

type signInURLResponse struct {
	URL string `json:"url"`
}

// handleJSON registers a console method whose request and response are not
// messages of the contract: the ones that carry a state or a code. It has
// [consolecall.Handle]'s rules: the caller class `console` alone, a *Problem
// told as data, any other error the transport's.
func handleJSON[Req, Resp any](
	s *modcall.Server, method string, fn func(ctx context.Context, caller consolecall.Caller, req Req) (Resp, error), opts ...modcall.Option,
) {
	opts = append(append([]modcall.Option{}, opts...), modcall.Allow(consolecall.Class))
	modcall.Handle(s, method, func(ctx context.Context, env consolecall.Envelope) (consolecall.Reply, error) {
		var req Req
		if len(env.Msg) > 0 {
			if err := json.Unmarshal(env.Msg, &req); err != nil {
				return consolecall.Reply{}, modcall.Coded(modcall.CodeBadRequest, "the request is not valid")
			}
		}
		resp, err := fn(ctx, env.Caller, req)
		var p *consolecall.Problem
		if errors.As(err, &p) {
			return consolecall.Reply{Problem: p}, nil
		}
		if err != nil {
			return consolecall.Reply{}, err
		}
		raw, err := json.Marshal(resp)
		if err != nil {
			return consolecall.Reply{}, modcall.CodedCause(modcall.CodeInternal, "", err)
		}
		return consolecall.Reply{Msg: raw}, nil
	}, opts...)
}

// callJSON is [handleJSON]'s caller.
func callJSON[Req, Resp any](
	ctx context.Context, c modcall.Caller, method string, caller consolecall.Caller, req Req,
) (Resp, error) {
	var zero Resp
	raw, err := json.Marshal(req)
	if err != nil {
		return zero, modcall.Coded(modcall.CodeInternal, "")
	}
	reply, err := modcall.Do[consolecall.Envelope, consolecall.Reply](ctx, c, Module, method,
		consolecall.Envelope{Caller: caller, Msg: raw})
	if err != nil {
		return zero, err
	}
	if reply.Problem != nil {
		return zero, reply.Problem
	}
	var out Resp
	if len(reply.Msg) > 0 {
		if err := json.Unmarshal(reply.Msg, &out); err != nil {
			return zero, modcall.Coded(modcall.CodeInternal, "")
		}
	}
	return out, nil
}
