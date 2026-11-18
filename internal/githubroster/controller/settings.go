package controller

import (
	"context"
	"log/slog"

	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubroster/orgsettings"
	"github.com/truvity/sluis/internal/githubroster/status"
	"github.com/truvity/sluis/storage/logattr"
)

// SettingsReader gives the source of one organisation's settings document and
// secrets (ADR 0080), or false when the installation names none for it.
type SettingsReader interface {
	For(org string) (orgsettings.Source, bool)
}

// maxAuditReason bounds the reason an audit record carries.
const maxAuditReason = 500

// settings is one organisation's settings pass: it reads the organisation's
// document, reconciles GitHub with it in the organisation's mode, and puts
// the outcome on the report. It is a failure domain of its own: a document
// that cannot be read or fails its checks is reported and audited and never
// fails the membership tick.
func (c *Controller) settings(ctx context.Context, org string, client githubapp.Org, token string, report *status.Org) {
	cfg, configured := c.cfg.OrgSettings[org]
	if !configured {
		return
	}

	var source orgsettings.Source

	if c.deps.Settings != nil {
		if s, found := c.deps.Settings.For(org); found {
			source = s
		}
	}

	previous := c.journal.Previous(ctx, org).Settings
	out := orgsettings.Pass(ctx, orgsettings.PassInput{
		Org: org, Settings: cfg, Source: source, Target: orgsettings.Target{Org: client, Token: token},
		Previous: previous, Now: c.deps.Now(), Enabled: c.cfg.Enabled[org],
	})
	report.Settings = &out

	c.deps.Log.InfoContext(ctx, "passed over an organisation's settings", slog.String("org", org), slog.String("mode", out.Mode),
		slog.String("document", string(out.Document.Outcome)), slog.Int("changes", len(out.Changes)))

	if out.Document.Outcome != status.DocumentApplied {
		c.deps.Log.WarnContext(ctx, "an organisation's settings document was not applied", slog.String("org", org),
			slog.String("outcome", string(out.Document.Outcome)), logattr.SafeString("reason", out.Document.Error))
	}

	c.report(ctx, settingsEvents(org, out, previous))
}

// settingsEvents are the audit records of a settings pass: one per change
// made (GH6), one per difference or refusal newly found, and one when the
// document is not applied. A difference or refusal that stands from the last
// pass is not recorded again. A record names the setting and never a value.
func settingsEvents(org string, out status.Settings, previous *status.Settings) []*record.Record {
	var events []*record.Record

	if out.Document.Outcome != status.DocumentApplied {
		if previous == nil || previous.Document != out.Document {
			events = append(events, audit.GitHubOrgSettingsRefused(org, string(out.Document.Outcome), out.Mode, reasonOf(out.Document.Error)))
		}

		return events
	}

	standing := map[status.SettingChange]bool{}

	if previous != nil && previous.Document.Outcome == status.DocumentApplied {
		for _, change := range previous.Changes {
			standing[change] = true
		}
	}

	for _, change := range out.Changes {
		if change.State != status.SettingApplied && standing[change] {
			continue
		}

		outcome := audit.Succeeded()
		if change.State == status.SettingFailed || change.State == status.SettingHeld || change.State == status.SettingConflict {
			outcome = audit.Failed(reasonOf(change.Reason))
		}

		events = append(events, audit.GitHubOrgSettingChanged(org, change.Kind, change.Name, change.Action, out.Mode, outcome))
	}

	return events
}

func reasonOf(text string) string {
	if len(text) > maxAuditReason {
		return text[:maxAuditReason] + "..."
	}

	return text
}
