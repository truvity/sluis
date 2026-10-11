// Package console is the Slack module's half of the console: what an operator
// does to Slack through the console (connecting a workspace, creating and
// installing Apps, defining channels and Slack Connect channels) runs here, on
// the module's own records and credentials, as the caller the console
// authorized (docs/decisions/0071, v1.76 D-v176-1 b).
//
// The console keeps what is the browser's: it signs the person in, takes the
// coarse role, signs the state of an install and pins the browser to it, and
// renders the page a callback lands on. It asks this package for the rest
// through modcall, as the caller class `console` and no other; [Register] puts
// the methods on the module's server. Authorization against the module's own
// records (who owns a workspace) runs here, on the caller snapshot, through
// internal/consoleauthz.
//
// A handler returns a Connect error for what the person is told, as the console
// always did; [Register] turns it into what crosses the wire: a refusal the
// transport carries (forbidden, unauthenticated), a [consolecall.Problem] for
// every other answer to the question asked, and nothing but a code for a
// failure of the module's own.
package console

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/consoleauthz"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/slackapp"
	slackcatalogue "github.com/truvity/sluis/internal/slackapp/catalogue"
	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/storage/logattr"
)

// Module is the module name the methods are registered under.
const Module = "slack"

// Authorizer is the policy in force, which the module reads.
type Authorizer interface {
	Policy() *policy.Set
}

// Deps is everything the module's console methods need. A store that is nil is
// a deployment that keeps nothing of that kind, and the method says so.
type Deps struct {
	// Log is where what is not an audit record goes; nil is the default logger.
	Log *slog.Logger
	// Authorizer is the policy in force.
	Authorizer Authorizer
	// Directory reads the connected directories, which the module has no
	// snapshot of: the console supplies them. Nil is a deployment that reads
	// none, and a source or an owner cannot be checked.
	Directory Directory
	// SlackCatalogue is every Slack App the deployment declares. Nil declares
	// none.
	SlackCatalogue *slackcatalogue.Catalogue
	// SlackCatalogueApps is where catalogue Slack Apps are kept.
	SlackCatalogueApps CatalogueApps
	// SlackShared is where Slack Connect channel definitions are kept.
	SlackShared SharedRecords
	// SlackChannels is where console channels' records are kept.
	SlackChannels ChannelRecords
	// SlackStatus is what the Slack controller last reported.
	SlackStatus StatusReports
	// SlackWorkspaces is where connected workspaces and operators'
	// confirmations are kept.
	SlackWorkspaces Workspaces
	// SlackAPI configures the calls to Slack: a test points them at a fake.
	SlackAPI []slackapp.Option
	// Audit records what an identity did. Nil records nothing.
	Audit audit.Recorder
	// Trigger is told which workspace a write concerns, so the controller's pass
	// over it runs soon. A notification is a hint: failing to send one is
	// logged. Nil notifies nobody.
	Trigger port.Trigger
}

// Service is the Slack module's console methods. It reads its [Deps] afresh at
// every call, so a deployment that connects a store late still serves it.
type Service struct{ deps func() Deps }

// NewService is the service over deps, read at each call.
func NewService(deps func() Deps) *Service { return &Service{deps: deps} }

// Static is a service over deps that never change.
func Static(deps Deps) *Service { return NewService(func() Deps { return deps }) }

func (c *Service) d() Deps { return c.deps() }

func (c *Service) log() *slog.Logger {
	if l := c.d().Log; l != nil {
		return l
	}
	return slog.Default()
}

// record writes down something done through the console. A module assembled
// without a recorder — a test does it — records nothing.
func (c *Service) record(ctx context.Context, r *record.Record) {
	if rec := c.d().Audit; rec != nil {
		rec.Record(ctx, r)
	}
}

// notify asks for the tick of a workspace after a write that concerns it, by
// the Slack controller. A notification is a hint: failing to send one is logged,
// never the request's failure.
func (c *Service) notify(ctx context.Context, controller, target string) {
	trigger := c.d().Trigger
	if trigger == nil || target == "" {
		return
	}
	if err := port.NotifyController(ctx, trigger, controller, target); err != nil {
		c.log().WarnContext(ctx, "a tick could not be requested",
			logattr.SafeString("controller", controller), logattr.SafeString("target", target),
			logattr.SafeError("error", err))
	}
}

// Directory is what the module asks of the connected directories: the console
// answers from the hub it holds. The ids are the directory workspaces' own.
type Directory interface {
	// Directories are every connected directory, sorted by id.
	Directories(ctx context.Context) ([]consoleauthz.Directory, error)
	// Group is whether an address is a group of a connected directory, and of
	// which.
	Group(ctx context.Context, address string) (Group, error)
	// User is whether an address is an account a connected directory holds.
	User(ctx context.Context, address string) (User, error)
	// Groups are every group of every connected directory, and the domains
	// those directories serve.
	Groups(ctx context.Context) ([]GroupEntry, []Domain, error)
}

// Group is a directory group's standing.
type Group struct {
	Found     bool
	Workspace string
}

// User is a directory account's standing.
type User struct {
	Found     bool
	Suspended bool
	Workspace string
}

// GroupEntry is one group of a directory, with its size.
type GroupEntry struct {
	Email   string
	Domain  string
	Members int
}

// Domain is a domain a directory serves.
type Domain struct {
	Name          string
	Workspace     string
	Authoritative bool
}

// directories are the connected directories, none for a deployment that reads
// none.
func (c *Service) directories(ctx context.Context) ([]consoleauthz.Directory, error) {
	dir := c.d().Directory
	if dir == nil {
		return nil, nil
	}
	out, err := dir.Directories(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return out, nil
}

// slackTimeout bounds one call to Slack.
const slackTimeout = 15 * time.Second

func timestampOf(at time.Time) *timestamppb.Timestamp {
	if at.IsZero() {
		return nil
	}
	return timestamppb.New(at)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// said is an error as the person may read it: the message of a refusal, not the
// transport's prefix.
func said(err error) string {
	var coded *modcall.Error
	var cerr *connect.Error
	switch {
	case errors.As(err, &coded):
		return coded.Message
	case errors.As(err, &cerr):
		return cerr.Message()
	}
	return err.Error()
}

// told turns what a handler returned into what crosses the wire.
//
//   - A refusal of the consoleauthz kind is the transport's, with its message.
//   - A Connect PermissionDenied or Unauthenticated is the same.
//   - Any other Connect error is the answer to the question asked: a Problem
//     carrying the code, which the console puts back.
//   - Internal and every error that is not Connect's is the module's own
//     failure: logged here, and told as a code with no detail.
func (c *Service) told(ctx context.Context, method string, err error) error {
	var cerr *connect.Error
	var coded *modcall.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &coded):
		return err
	case errors.As(err, &cerr):
		switch cerr.Code() {
		case connect.CodePermissionDenied:
			return modcall.Coded(modcall.CodeForbidden, cerr.Message())
		case connect.CodeUnauthenticated:
			return modcall.Coded(consoleauthz.CodeUnauthenticated, cerr.Message())
		case connect.CodeInternal, connect.CodeUnknown:
		default:
			return &consolecall.Problem{Code: cerr.Code().String(), Summary: cerr.Message()}
		}
	}
	c.log().ErrorContext(ctx, "a console method failed", slog.String("method", method), logattr.SafeError("error", err))
	return modcall.Coded(modcall.CodeInternal, "")
}

// CodeOf is the Connect code a Problem names, a failed precondition when it
// names none.
func CodeOf(p *consolecall.Problem) connect.Code {
	var code connect.Code
	if p == nil || code.UnmarshalText([]byte(p.Code)) != nil || p.Code == "" {
		return connect.CodeFailedPrecondition
	}
	return code
}
