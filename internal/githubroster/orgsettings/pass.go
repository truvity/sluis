package orgsettings

import (
	"context"
	"errors"
	"maps"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/githubroster/orgconfig"
	"github.com/truvity/sluis/internal/githubroster/status"
	"github.com/truvity/sluis/policy/tenant"
)

// Source is where one organisation's document and secrets are read from.
type Source interface {
	// Document is the organisation's typed document, read now.
	Document(ctx context.Context) ([]byte, error)
	Secrets
}

// PassInput is one pass over one organisation's settings.
type PassInput struct {
	Org      string
	Settings orgconfig.Config
	// Source is nil when the organisation has no way to read its document;
	// the pass then reports it unreadable.
	Source Source
	Target Target
	// Previous is the last pass's record, or nil for the first.
	Previous *status.Settings
	Now      time.Time
}

// maxError bounds an error text kept in the report.
const maxError = 2000

// Pass reads the organisation's document and, when it holds to its checks,
// reconciles GitHub with it. A document that cannot be read or fails a check
// is not applied: GitHub keeps what the last good document made it, and the
// report says why. The result never holds a value.
func Pass(ctx context.Context, in PassInput) status.Settings {
	mode := Mode(in.Settings.EffectiveMode())
	out := status.Settings{Mode: string(mode), At: in.Now.UTC()}

	if in.Previous != nil {
		out.Managed = in.Previous.Managed
	}

	notApplied := func(outcome status.DocumentOutcome, reason string) status.Settings {
		out.Document = status.Document{Outcome: outcome, Error: truncate(reason)}

		return out
	}

	if in.Source == nil {
		return notApplied(status.DocumentUnreadable, "no read access is configured for this organisation's document")
	}

	raw, err := in.Source.Document(ctx)
	if err != nil {
		return notApplied(status.DocumentUnreadable, err.Error())
	}

	section, err := decode(raw)
	if err != nil {
		return notApplied(status.DocumentRefused, err.Error())
	}

	if vs := tenant.CheckOrg(tenant.Owner{Name: in.Org, Ceiling: tenant.Ceiling{Org: in.Settings.Ceiling()}}, section); len(vs) > 0 {
		lines := make([]string, 0, len(vs))
		for _, v := range vs {
			lines = append(lines, v.Code+" "+v.Path+": "+v.Detail)
		}

		return notApplied(status.DocumentRefused, strings.Join(lines, "\n"))
	}

	result, err := Reconcile(ctx, Input{
		Mode:     mode,
		Target:   in.Target,
		Desired:  desired(in.Settings, section),
		Previous: out.Managed,
		Secrets:  in.Source,
	})
	if err != nil {
		return notApplied(status.DocumentFailed, err.Error())
	}

	out.Document = status.Document{Outcome: status.DocumentApplied}
	out.Changes = result.Changes
	out.Managed = result.Managed

	return out
}

// decode reads the document and refuses one that carries more than the org
// section: a rung or a gate in this document would be a row nobody merges.
func decode(raw []byte) (tenant.OrgSection, error) {
	fragment, err := tenant.DecodeFragment(raw)
	if err != nil {
		return tenant.OrgSection{}, err //nolint:wrapcheck // the decoder names the line
	}

	org := fragment.Org
	fragment.Org = tenant.OrgSection{}

	if !fragment.IsEmpty() {
		return tenant.OrgSection{}, errors.New("the document carries a section other than org; the GitHub controller reads only org")
	}

	return org, nil
}

// desired is what the organisation should hold: the document's variables,
// secrets and configurations, and the installation's variables over them.
func desired(s orgconfig.Config, section tenant.OrgSection) Desired {
	d := Desired{
		Variables: maps.Clone(section.Variables),
		Secrets:   section.Secrets,
		Configs:   section.SecurityConfigurations,
	}
	if d.Variables == nil {
		d.Variables = map[string]tenant.OrgVariable{}
	}

	for name, platform := range s.Variables {
		v := tenant.OrgVariable{Value: platform.Value, Visibility: orDefault(platform.Visibility, tenant.VisibilityAll), Repositories: platform.Repositories}

		if own, declared := section.Variables[name]; declared && s.Ceiling().ValueOnly(name) && own.Visibility != "" {
			v.Visibility, v.Repositories = own.Visibility, own.Repositories
		}

		d.Variables[name] = v
	}

	return d
}

func truncate(text string) string {
	if len(text) > maxError {
		return text[:maxError] + "..."
	}

	return text
}

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}

	return fallback
}
