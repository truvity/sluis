package server

import (
	"context"
	"net/http"
	"path"
	"strconv"
	"strings"

	"connectrpc.com/connect/v2"

	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/port"
)

// Maintenance in the console (docs/decisions/0072, decision 9). Each module
// keeps its flag in its own table; the console writes records of several
// modules, so a write is refused by the flag of the module whose record it
// writes. A read is never refused: a person can still see what is there, and the
// banner says why nothing can be changed.

// maintenanceBody is the state whoami reports.
type maintenanceBody struct {
	// Module is the module under maintenance, named as in the layout.
	Module string `json:"module"`
	State  string `json:"state"`
	Since  string `json:"since,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// maintenanceBody is the first module under maintenance, or nil.
func (s *ConsoleServer) maintenanceBody(ctx context.Context) *maintenanceBody {
	m, f, ok := s.maintenance.Any(ctx)
	if !ok {
		return nil
	}
	b := &maintenanceBody{Module: string(m), State: f.State, Reason: f.Reason}
	if !f.Since.IsZero() {
		b.Since = f.Since.UTC().Format("2006-01-02T15:04:05Z")
	}
	return b
}

// rpcModule is the module whose records an operator RPC writes.
func rpcModule(procedure string) port.Module {
	service := path.Base(path.Dir(procedure))
	service = service[strings.LastIndex(service, ".")+1:]
	switch service {
	case "WorkspaceService":
		return port.ModuleGoogle
	case "GitHubService":
		return port.ModuleGitHub
	case "SlackAppService", "SlackSharedChannelService", "SlackChannelService", "SlackService":
		return port.ModuleSlack
	case "CloudflareService":
		return port.ModuleCloudflare
	case "BackupService":
		// Reads only; mapped so that a write added later is held by its own flag.
		return port.ModuleBackup
	case "OutlineService":
		// The Outline controller's records are the issuer's until it has a
		// function of its own (docs/decisions/0077).
		return port.ModuleOIDC
	}
	// Settings and access are the issuer's own and have no write: the policy in
	// force and who holds what.
	return port.ModuleOIDC
}

// rpcIsRead says an operator RPC changes nothing, by the name every read in the
// services has. Anything else is a write, so a method added without thought is
// refused in maintenance rather than served.
func rpcIsRead(procedure string) bool {
	method := path.Base(procedure)
	// Closing the break-glass door only ever reduces what can get in, and it
	// writes a record no restore touches (the kind `recovery` is regenerated): it
	// must work while a restore is running, which is when an operator may most need it.
	if method == "DisableRecovery" {
		return true
	}
	for _, p := range []string{"Get", "List", "Search", "Resolve", "Explain", "WhoAmI"} {
		if strings.HasPrefix(method, p) {
			return true
		}
	}
	return false
}

// maintenanceInterceptors refuses a write RPC of a module under maintenance,
// as Unavailable with the retry hint.
func maintenanceInterceptors(set maintenance.Set) []connect.ServerInterceptor {
	if len(set) == 0 {
		return nil
	}
	return []connect.ServerInterceptor{func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			proc := spec.Procedure
			if !rpcIsRead(proc) {
				if err := set.Of(rpcModule(proc)).Writable(ctx); err != nil {
					// The hint is a response header, which Connect sends with
					// the error.
					if info, ok := connect.CallInfoForServerContext(ctx); ok {
						info.ResponseHeader().Set("Retry-After", strconv.Itoa(maintenance.RetryAfter))
					}
					return connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
				}
			}
			return next(ctx, spec, stream)
		}
	}}
}

// pageModule is the module whose records a console page writes, or "" for a
// page that only reads. A flow that returns from GitHub, Slack or a directory's
// consent stores a record on a GET.
func pageModule(r *http.Request) port.Module {
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/connect/github/"):
		return port.ModuleGitHub
	case strings.HasPrefix(p, "/connect/slack/"):
		return port.ModuleSlack
	case strings.HasPrefix(p, "/connect/"):
		return port.ModuleGoogle
	case strings.HasPrefix(p, "/login/"), r.Method != http.MethodGet && r.Method != http.MethodHead:
		return port.ModuleOIDC
	}
	return ""
}

// withMaintenance refuses the pages that write while the module whose records
// they write is under maintenance. The RPCs are guarded by [maintenanceOptions]
// and pass here; the shell, the assets, whoami and the audit query only read.
func (s *ConsoleServer) withMaintenance(next http.Handler) http.Handler {
	if len(s.maintenance) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isRPCPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if m := pageModule(r); m != "" {
			if err := s.maintenance.Of(m).Writable(r.Context()); err != nil {
				maintenance.Refuse(w, r, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// isRPCPath is a Connect call: /<package.Service>/<Method>.
func isRPCPath(p string) bool {
	rest := strings.TrimPrefix(p, "/")
	service, method, ok := strings.Cut(rest, "/")
	return ok && method != "" && !strings.Contains(method, "/") && strings.Contains(service, ".") && !strings.HasPrefix(service, ".")
}
