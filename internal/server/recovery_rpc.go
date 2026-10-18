package server

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/recoverygate"
	"github.com/truvity/sluis/storage/logattr"
)

// Turning the recovery sign-in off from the console (docs/decisions/0075).
//
// There is one action, and it only closes. Re-enabling is a change of the
// configuration (`recovery.generation`), so the console that an attacker with a
// stolen recovery session or a stolen operator session reaches has no button
// that opens the break-glass door, and a stale document cannot open it either.

// RecoveryEnder ends every browser sign-in and client session that recovery
// opened, and says how many. The issuer provides it; a console with no issuer
// beside it has none, and its recovery sessions (a signed cookie, no record)
// end by the switch alone.
type RecoveryEnder func(ctx context.Context) (signIns, sessions int, err error)

// recoveryEnd is the ender, set once after the issuer exists.
type recoveryEnd struct {
	mu sync.RWMutex
	fn RecoveryEnder
}

func (e *recoveryEnd) set(fn RecoveryEnder) { e.mu.Lock(); e.fn = fn; e.mu.Unlock() }
func (e *recoveryEnd) get() RecoveryEnder   { e.mu.RLock(); defer e.mu.RUnlock(); return e.fn }

// UseRecoveryEnder supplies the sweep that follows the switch, for the same
// reason and with the same timing as [ConsoleServer.UseSignedIn]: the issuer
// that owns the sessions does not exist yet when this console is built.
func (s *ConsoleServer) UseRecoveryEnder(fn RecoveryEnder) {
	if s.console != nil {
		s.console.recoveryEnd.set(fn)
	}
}

// Why a caller cannot turn recovery off, in words the console shows.
const (
	cannotRecovery = "You signed in with the recovery sign-in. Sign in through your identity provider to turn it off: " +
		"a break-glass session does not get to decide the break-glass door's fate."
	cannotNotPerson = "Only a person signed in through the identity provider can turn recovery off."
	cannotOperator  = "Turning recovery off needs the installation-wide operator role."
)

// recoveryDisableRefusal says why identity may not turn recovery off, or ""
// when it may. This is the whole of the authorisation, enforced here in the
// RPC and not by hiding a button:
//
//   - The sign-in must be the identity provider's (a gateway's forwarded
//     identity, the hub's own directory or OIDC sign-in). A recovery sign-in,
//     whether the console's cookie or the issuer's, is refused: it is a bypass
//     of the identity provider, and the decision to close it is not its to make.
//     A workload or an AWS role is refused too: it is no person at a console.
//   - The caller must hold the OPERATOR role over the installation. That is the
//     narrowest role this console has that already may change what signs people
//     in (connect a directory, register the OAuth client); recovery itself
//     grants exactly it. A viewer, and an operator of one workspace only, may
//     not close a door that opens the whole installation.
func recoveryDisableRefusal(id access.Identity) string {
	switch id.Source {
	case access.SourceRecovery:
		return cannotRecovery
	case access.SourceForwarded, access.SourceDirectory, access.SourceOIDC:
	case access.SourceWorkload:
		return cannotNotPerson
	default:
		return cannotNotPerson
	}
	if id.Email == "" {
		return cannotNotPerson
	}
	if !id.Can(access.RoleOperator) {
		return cannotOperator
	}
	return ""
}

// recoveryConfigured reports whether the configuration builds a recovery
// sign-in at all.
func (c *Console) recoveryConfigured() bool { return recoveryEnabled(c.deps.Recovery) }

// recoveryUsable reports whether the recovery sign-in can be used now.
func (c *Console) recoveryUsable(ctx context.Context) bool {
	if !c.recoveryConfigured() {
		return false
	}
	off, _ := c.deps.RecoveryGate.Off(ctx)
	return !off
}

// recoveryKindNow is the shape of the recovery sign-in while it can be used,
// and nothing once it has been turned off: a password that no longer opens
// anything is not a standing credential worth a banner.
func (c *Console) recoveryKindNow(ctx context.Context) string {
	if !c.recoveryUsable(ctx) {
		return ""
	}
	return recoveryKindOf(c.deps.Recovery)
}

// GetRecovery implements the operator contract.
func (c *Console) GetRecovery(
	ctx context.Context, _ *connect.Request[directoryrosterv1.GetRecoveryRequest],
) (*connect.Response[directoryrosterv1.GetRecoveryResponse], error) {
	id, err := requireRole(ctx, access.RoleOperator)
	if err != nil {
		return nil, err
	}
	gate := c.deps.RecoveryGate
	status, statusErr := gate.Status(ctx)
	out := &directoryrosterv1.GetRecoveryResponse{
		Configured: c.recoveryConfigured(),
		Kind:       recoveryKindOf(c.deps.Recovery),
		Generation: status.Generation,
		Durable:    status.Durable,
	}
	if d := status.Disabled; d != nil {
		out.DisabledAt, out.DisabledBy = timestamppb.New(d.At), d.By
	}
	if d := status.Superseded; d != nil {
		out.SupersededAt, out.SupersededBy = timestamppb.New(d.At), d.By
	}
	// A switch that cannot be read counts as off: the sign-in refuses while
	// it cannot be checked, so it is not "effective" either.
	out.Effective = out.Configured && status.Disabled == nil && statusErr == nil
	switch {
	case !out.Configured:
		out.CannotDisableReason = "The recovery sign-in is not enabled on this installation."
	case status.Disabled != nil:
		out.CannotDisableReason = "The recovery sign-in is already turned off. Turning it back on is a change of recovery.generation in the configuration."
	case statusErr != nil && !errors.Is(statusErr, recoverygate.ErrUnsupported):
		out.CannotDisableReason = "The state that keeps the switch could not be read; try again shortly."
	case !status.Durable || errors.Is(statusErr, recoverygate.ErrUnsupported):
		out.Durable = false
		out.CannotDisableReason = "This installation's state cannot keep a turn-off across restarts. Set recovery.enabled to false in the configuration instead."
	default:
		out.CannotDisableReason = recoveryDisableRefusal(id)
		out.CanDisable = out.CannotDisableReason == ""
	}
	return connect.NewResponse(out), nil
}

// DisableRecovery implements the operator contract.
func (c *Console) DisableRecovery(
	ctx context.Context, _ *connect.Request[directoryrosterv1.DisableRecoveryRequest],
) (*connect.Response[directoryrosterv1.DisableRecoveryResponse], error) {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in first"))
	}
	gate := c.deps.RecoveryGate
	if why := recoveryDisableRefusal(id); why != "" {
		// A refused attempt is worth a record: somebody holding a recovery
		// session, or a viewer, asked to close the break-glass door.
		c.record(ctx, audit.RecoveryDisabled(actorOf(ctx), c.installation(), gate.Generation(), 0, 0, false, audit.Denied(why)))
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New(why))
	}
	if !c.recoveryConfigured() {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("the recovery sign-in is not enabled on this installation; there is nothing to turn off"))
	}

	// The switch first: it is the act, and nothing after it may undo it. The
	// audit trail is tried after, so an outage of the trail cannot stop an
	// operator closing the door.
	rec, created, err := gate.Disable(ctx, id.Who())
	switch {
	case errors.Is(err, recoverygate.ErrUnsupported):
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this installation's state cannot keep a turn-off across restarts, so it is not made; set recovery.enabled to false in the configuration"))
	case err != nil:
		c.log().ErrorContext(ctx, "recovery could not be turned off", logattr.SafeError("error", err))
		c.record(ctx, audit.RecoveryDisabled(actorOf(ctx), c.installation(), gate.Generation(), 0, 0, false, audit.Failed("the switch could not be kept")))
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("the switch could not be kept; recovery is still on. Try again"))
	}

	var signIns, sessions int
	if end := c.recoveryEnd.get(); end != nil {
		var endErr error
		if signIns, sessions, endErr = end(ctx); endErr != nil {
			// The switch is thrown and refuses every recovery session at its next
			// use, in every container; what is left is records that nothing reads.
			c.log().ErrorContext(ctx, "recovery is turned off, but the sessions it opened could not all be ended; they are refused at their next use",
				logattr.SafeError("error", endErr), slog.Int("sign_ins_ended", signIns), slog.Int("sessions_ended", sessions))
		}
	}

	out := &directoryrosterv1.DisableRecoveryResponse{
		DisabledAt:      timestamppb.New(rec.At),
		DisabledBy:      rec.By,
		AlreadyDisabled: !created,
		SignInsEnded:    int32(signIns),  //nolint:gosec // a count of sessions never overflows
		SessionsEnded:   int32(sessions), //nolint:gosec // likewise
	}
	if created || signIns+sessions > 0 {
		entry := audit.RecoveryDisabled(actorOf(ctx), c.installation(), rec.Generation, signIns, sessions, !created, audit.Succeeded())
		if err = c.recordDurable(ctx, entry); err != nil {
			// The door is shut regardless. A disable is the one act whose record
			// is not allowed to stop it, so the failure is loud instead, and the
			// record is queued to be retried.
			c.log().ErrorContext(ctx, "recovery was turned off, but the audit trail could not be written durably; the record is queued",
				logattr.SafeString("by", rec.By), logattr.SafeError("error", err))
			c.record(ctx, entry)
			out.AuditPending = true
		}
	}
	c.log().WarnContext(ctx, "recovery turned off from the console",
		logattr.SafeString("by", rec.By), slog.Bool("already", !created), slog.Int("sign_ins_ended", signIns), slog.Int("sessions_ended", sessions))
	return connect.NewResponse(out), nil
}

// installation is how the audit trail names this installation.
func (c *Console) installation() string {
	if c.deps.Instance != "" {
		return c.deps.Instance
	}
	return "sluis"
}
