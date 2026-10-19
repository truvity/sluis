//go:build !lambda

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/user"
	"regexp"

	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/port"
)

// An operator's command that changes an installation (`sluis maintenance on|off`)
// is audited or it does not run. The catalogue declares its actions `block`, so
// the record is delivered before the call returns, and a delivery that fails
// refuses the command with the queue and the principal the queue refused.
// `--no-audit` is the break-glass: the command goes ahead, says so, and writes
// the same record to stderr as JSON.

// noAuditFlag is the break-glass flag of those commands.
const noAuditFlag = "no-audit"

// operatorAudit delivers the records of one operator command.
type operatorAudit struct {
	// rec delivers a record: the installation's trail, or, with noAudit, a trail
	// that keeps nothing and only fills the record in.
	rec audit.Recorder
	// dest says where records go, for the refusal: "queue <url>" or "writer <addr>".
	dest    string
	noAudit bool
	stderr  io.Writer
	warned  bool
}

// openOperatorAudit opens the trail an operator command records to, from the
// configuration it runs with. With noAudit it opens none that is connected.
func openOperatorAudit(ctx context.Context, cfg *config.Serve, plan port.Table, version, instance string,
	noAudit bool, stderr io.Writer, log *slog.Logger) (*operatorAudit, func(), error) {
	ac := audit.Config{Version: version, Instance: instance, Log: log}
	if noAudit {
		trail, err := audit.Open(ctx, ac)
		if err != nil {
			return nil, nil, err
		}
		return &operatorAudit{rec: trail, noAudit: true, stderr: stderr, dest: "nowhere (--no-audit)"}, func() { _ = trail.Close() }, nil
	}
	if a := cfg.Audit; a != nil {
		ac.Writer, ac.TokenFile = a.Writer, a.TokenFile
	}
	ac, err := audit.FromPlan(ac, plan)
	if err != nil {
		return nil, nil, err
	}
	trail, err := audit.Open(ctx, ac)
	if err != nil {
		return nil, nil, err
	}
	dest := "the audit log (no installation is connected)"
	switch {
	case ac.SQS != nil:
		dest = "queue " + ac.SQS.QueueURL
	case ac.Writer != "":
		dest = "writer " + ac.Writer
	}
	return &operatorAudit{rec: trail, dest: dest, stderr: stderr}, func() { _ = trail.Close() }, nil
}

// deniedPrincipal finds the principal AWS names in an access denied answer
// ("User: arn:... is not authorized to perform: sqs:SendMessage on resource").
var deniedPrincipal = regexp.MustCompile(`User: (\S+) is not authorized`)

// ErrAuditRefused is a command refused because its audit record could not be
// delivered.
var ErrAuditRefused = errors.New("audit record not delivered")

// keep delivers the record, or refuses: what says what the command was about
// to do. With --no-audit it prints the record to stderr and goes on.
func (o *operatorAudit) keep(ctx context.Context, what string, r *record.Record) error {
	if o.noAudit {
		return o.printLocal(ctx, what, r)
	}
	err := o.rec.RecordDurable(ctx, r)
	if err == nil {
		return nil
	}
	who := "the caller"
	if m := deniedPrincipal.FindStringSubmatch(err.Error()); m != nil {
		who = m[1] + " (denied)"
	}
	return fmt.Errorf("%w: refusing to %s: the audit record could not be delivered to %s by %s: %v; "+
		"nothing was changed by this step. Fix the operator role's access to the destination, or, as break-glass, "+
		"repeat with --%s (the record is then printed to stderr and the command goes ahead unaudited)",
		ErrAuditRefused, what, o.dest, who, err, noAuditFlag)
}

// keepAfter is [keep] for a record of something that already happened: a failure
// to deliver it cannot refuse the command, so it says what was left unrecorded.
func (o *operatorAudit) keepAfter(ctx context.Context, what string, r *record.Record) error {
	if err := o.keep(ctx, what, r); err != nil {
		return fmt.Errorf("%s already happened, and its closing record was not kept: %w", what, err)
	}
	return nil
}

func (o *operatorAudit) printLocal(ctx context.Context, what string, r *record.Record) error {
	if !o.warned {
		o.warned = true
		_, _ = fmt.Fprintf(o.stderr, "WARNING: --%s: %s proceeds WITHOUT its audit record reaching the installation. "+
			"The record is printed below; keep it and report it.\n", noAuditFlag, what)
	}
	// The trail only fills the record in (id, times, observer) and logs it.
	o.rec.Record(ctx, r)
	line, err := record.Canonical(r)
	if err != nil {
		return fmt.Errorf("--%s: the record cannot be printed: %w", noAuditFlag, err)
	}
	_, _ = fmt.Fprintf(o.stderr, "%s\n", line)
	return nil
}

// operatorActor is who ran the command: --by, else the login on this machine.
func operatorActor(by, fallback string) audit.Actor {
	if by == "" {
		if u, err := user.Current(); err == nil {
			by = u.Username
		}
	}
	if by == "" {
		return audit.Workload(fallback)
	}
	return audit.Person(by)
}
