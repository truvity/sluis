// Package console is the GitHub module's half of the console: the handlers
// for connecting an organisation, the Apps view, the catalogue and runner Apps,
// the link App and people's links, moved out of the shared console so that the
// process which serves the console holds no App private key, no webhook secret
// and no connection record (docs/decisions/0071, v1.76 D-v176-1 b).
//
// The console authenticates a person, takes a snapshot of who they are and
// calls these through the module's server ([Register]); the handlers act on
// that snapshot with the rules in internal/consoleauthz. A provider callback is
// still an HTTP route on the issuer, which verifies the signed state and the
// browser's cookie and forwards the one-time code to the module's `complete_*`
// method; the module never sees a session key.
package console

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/githubapp/catalogue"
	"github.com/truvity/sluis/internal/ownerrule"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/storage/logattr"
)

// Deps is what the module's console handlers act on.
type Deps struct {
	// Log is where the module logs what is not an audit record; nil is the
	// default logger.
	Log *slog.Logger
	// Audit records what a person did through the console, as the actor of the
	// caller snapshot. Nil records nothing.
	Audit audit.Recorder
	// Trigger is told which organisation a write concerns, so the module's own
	// tick runs soon (docs/decisions/0029): the self-tick after a write. Nil
	// notifies nobody.
	Trigger port.Trigger
	// Policy is the policy in force: which organisations it binds.
	Policy func() *policy.Set
	// PublicURL is where a browser reaches the console, and RootURL the origin
	// the bootstrap callbacks are at; empty falls back to PublicURL.
	PublicURL, RootURL string
	// Orgs is where connected organisations are kept. Nil is a deployment
	// keeping no state in Kubernetes, which can connect none.
	Orgs Connections
	// OrgAppName is the name the organisation's App is created with, empty for
	// the default (<org>-access-roster). Nil is all defaults.
	OrgAppName func(org string) string
	// Catalogue is every App the deployment declares. Nil declares none.
	Catalogue *catalogue.Catalogue
	// HTTP makes the calls to GitHub that connecting and disconnecting need.
	// Nil is a client with a short timeout.
	HTTP *http.Client
}

// Service is the module's console handlers.
type Service struct {
	deps Deps
}

// New returns the handlers over deps.
func New(deps Deps) *Service { return &Service{deps: deps} }

// log is the module's logger: the one it was given, or the default.
func (c *Service) log() *slog.Logger {
	if c.deps.Log != nil {
		return c.deps.Log
	}
	return slog.Default()
}

// record writes down what an identity did. Recording is never the thing to
// crash.
func (c *Service) record(ctx context.Context, r *record.Record) {
	if c == nil || c.deps.Audit == nil {
		return
	}
	c.deps.Audit.Record(ctx, r)
}

// notify asks for the tick of an organisation's controller after a write that
// concerns it: the module's self-tick. A notification is a hint, so failing to
// send one is logged, never the request's failure. The controller is always
// named (rc.13): a tick with none is ambiguous.
func (c *Service) notify(ctx context.Context, controller, target string) {
	if c.deps.Trigger == nil || target == "" {
		return
	}
	if err := port.NotifyController(ctx, c.deps.Trigger, controller, target); err != nil {
		c.log().WarnContext(ctx, "a tick could not be requested",
			logattr.SafeString("controller", controller), logattr.SafeString("target", target),
			logattr.SafeError("error", err))
	}
}

// directories are the connected directories as the console's snapshot of the
// call has them: the module reads no directory record.
func (c *Service) directories(ctx context.Context) ([]ownerrule.Directory, error) {
	return callerOf(ctx).Directories, nil
}

// bindings are the policy's organisation bindings.
func (c *Service) bindings() map[string]*binding {
	if c.deps.Policy == nil {
		return nil
	}
	return boundOrganisations(c.deps.Policy())
}

// timestampOf is a time as the contract carries it.
func timestampOf(at time.Time) *timestamppb.Timestamp { return timestamppb.New(at) }

// actorOf is whoever the console's caller is, as the trail names them.
func actorOf(ctx context.Context) audit.Actor {
	id, ok := identityFrom(ctx)
	if !ok {
		return audit.Anonymous()
	}
	return identityActor(id)
}

// identityActor is an identity as the trail names it: a person by their
// address, or by the actor the console vouched for.
func identityActor(id access.Identity) audit.Actor {
	switch {
	case id.Source == access.SourceWorkload:
		return audit.Workload(id.Subject)
	case id.Source == access.SourceRecovery:
		return audit.RecoveryIdentity(id.Who())
	case id.Email != "":
		return audit.Person(id.Email)
	default:
		return audit.Identified(id.Who())
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

var errNoOrgs = errors.New("this deployment keeps no connected organisations")
