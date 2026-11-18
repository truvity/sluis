package orgsettings

import (
	"context"
	"errors"
	"regexp"
	"slices"
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
	// Enabled is whether the controller acts in this organisation
	// (`controllers.github.enabledOrgs`). A disabled organisation is read as
	// `report` whatever its mode says: that list is the kill switch.
	Enabled bool
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

	if mode == ModeEnforce && !in.Enabled {
		mode = ModeReport
		out.Mode, out.NotEnabled = string(mode), true
	}

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

	// An empty document is more likely a parameter that lost its content than
	// a request to remove everything the controller wrote.
	if section.IsEmpty() {
		return notApplied(status.DocumentUnreadable, "the document's org section is empty: it is read as a document that could not be read, and nothing changes")
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
		Ceiling:  in.Settings.Ceiling(),
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

// atLine finds the line numbers a YAML decoder names.
var atLine = regexp.MustCompile(`line ([0-9]+)`)

// decode reads the document and refuses one that carries more than the org
// section: a rung or a gate in this document would be a row nobody merges.
// A decoder's error can quote a scalar of the document, so a refusal names the
// lines and not the words.
func decode(raw []byte) (tenant.OrgSection, error) {
	fragment, err := tenant.DecodeFragment(raw)
	if err != nil {
		var lines []string

		for _, m := range atLine.FindAllStringSubmatch(err.Error(), -1) {
			if !slices.Contains(lines, m[1]) {
				lines = append(lines, "line "+m[1])
			}
		}

		where := ""
		if len(lines) > 0 {
			where = " (" + strings.Join(lines, ", ") + ")"
		}

		return tenant.OrgSection{}, errors.New("the document does not decode as a fragment: a key or a value is not one the fragment has" + where)
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
// Names are folded, as GitHub reads them.
func desired(s orgconfig.Config, section tenant.OrgSection) Desired {
	d := Desired{
		Variables: map[string]tenant.OrgVariable{},
		Secrets:   section.Secrets,
		Configs:   section.SecurityConfigurations,
		Platform:  map[string]bool{},
	}

	own := map[string]tenant.OrgVariable{}

	for name, v := range section.Variables {
		d.Variables[tenant.FoldName(name)] = v
		own[tenant.FoldName(name)] = v
	}

	for name, platform := range s.Variables {
		folded := tenant.FoldName(name)
		v := tenant.OrgVariable{Value: platform.Value, Visibility: orDefault(platform.Visibility, tenant.VisibilityAll), Repositories: platform.Repositories}

		if declared, ok := own[folded]; ok && s.Ceiling().ValueOnly(name) && declared.Visibility != "" {
			v.Visibility, v.Repositories = declared.Visibility, declared.Repositories
		}

		d.Variables[folded] = v
		d.Platform[folded] = true
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
