package docsgen

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// auditActions renders the audit catalogue's actions, in document order.
func auditActions(root string) (string, error) {
	const src = "internal/audit/catalogue/roster.yaml"
	b, err := os.ReadFile(filepath.Join(root, src))
	if err != nil {
		return "", err
	}
	var doc struct {
		Version string    `yaml:"version"`
		Actions yaml.Node `yaml:"actions"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return "", fmt.Errorf("%s: %w", src, err)
	}
	type action struct {
		Summary   string   `yaml:"summary"`
		Operation string   `yaml:"operation"`
		Targets   []string `yaml:"target_types"`
		Delivery  string   `yaml:"delivery"`
	}
	var rows strings.Builder
	n := 0
	for i := 0; i+1 < len(doc.Actions.Content); i += 2 {
		var a action
		if err := doc.Actions.Content[i+1].Decode(&a); err != nil {
			return "", err
		}
		targets := "—"
		if len(a.Targets) > 0 {
			targets = strings.Join(a.Targets, ", ")
		}
		fmt.Fprintf(&rows, "| `%s` | %s | %s | %s | %s |\n", doc.Actions.Content[i].Value, cell(a.Operation), cell(targets), cell(a.Delivery), cell(a.Summary))
		n++
	}
	head := fmt.Sprintf("Catalogue version %s, %d actions.\n\n| Action | Operation | Targets | Delivery | Summary |\n|---|---|---|---|---|\n", doc.Version, n)
	return head + rows.String(), nil
}

var firstSentence = regexp.MustCompile(`^.*?[.!?](\s|$)`)

// telemetryAlerts renders the chart's alert rules from the golden render of
// templates/alerts.yaml, which `just chart-lint` holds to the template.
func telemetryAlerts(root string) (string, error) {
	const src = "tests/golden/sluis/alerts.yaml"
	b, err := os.ReadFile(filepath.Join(root, src))
	if err != nil {
		return "", err
	}
	var doc struct {
		Spec struct {
			Groups []struct {
				Rules []struct {
					Alert       string            `yaml:"alert"`
					Expr        string            `yaml:"expr"`
					For         string            `yaml:"for"`
					Labels      map[string]string `yaml:"labels"`
					Annotations map[string]string `yaml:"annotations"`
				} `yaml:"rules"`
			} `yaml:"groups"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return "", fmt.Errorf("%s: %w", src, err)
	}
	var rows strings.Builder
	n := 0
	for _, g := range doc.Spec.Groups {
		for _, r := range g.Rules {
			desc := strings.Join(strings.Fields(r.Annotations["description"]), " ")
			if m := firstSentence.FindString(desc); m != "" {
				desc = strings.TrimSpace(m)
			}
			expr := strings.ReplaceAll(strings.Join(strings.Fields(r.Expr), " "), "|", `\|`)
			fmt.Fprintf(&rows, "| `%s` | %s | %s | %s | `%s` |\n", r.Alert, cell(r.Labels["severity"]), cell(r.For), cell(desc), expr)
			n++
		}
	}
	head := fmt.Sprintf("Source: `%s`, the render of `charts/sluis/templates/alerts.yaml` with default values (%d rules). The expressions carry the default thresholds; every one is a value under `alerts.rules`.\n\n| Alert | Severity | For | What it says | Default expression |\n|---|---|---|---|---|\n", src, n)
	return head + rows.String(), nil
}

var (
	adrFile   = regexp.MustCompile(`^(\d{4})-.*\.md$`)
	adrTitle  = regexp.MustCompile(`^# \d{4} — (.+)$`)
	adrStatus = regexp.MustCompile(`^\*\*Status:\*\*\s*(.*)$`)
)

// adrIndex renders the ADR index from each record's title and Status line. A
// Status may wrap over several lines, up to the Date line or a blank one.
func adrIndex(root string) (string, error) {
	dir := filepath.Join(root, "docs", "decisions")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var rows strings.Builder
	rows.WriteString("| ADR | Decision | Status |\n|---|---|---|\n")
	for _, e := range ents {
		m := adrFile.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return "", err
		}
		var title, status string
		inStatus := false
		for _, line := range strings.Split(string(b), "\n") {
			switch {
			case title == "" && adrTitle.MatchString(line):
				title = adrTitle.FindStringSubmatch(line)[1]
			case adrStatus.MatchString(line):
				status = adrStatus.FindStringSubmatch(line)[1]
				inStatus = true
			case inStatus && strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "**"):
				status += " " + strings.TrimSpace(line)
			default:
				if inStatus {
					inStatus = false
				}
			}
			if title != "" && status != "" && !inStatus {
				break
			}
		}
		if title == "" || status == "" {
			return "", fmt.Errorf("%s: want a `# NNNN — Title` line and a `**Status:**` line", e.Name())
		}
		fmt.Fprintf(&rows, "| [%s](%s) | %s | %s |\n", m[1], e.Name(), cell(title), cell(status))
	}
	return rows.String(), nil
}
