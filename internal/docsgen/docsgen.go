// Package docsgen writes the generated regions of the documentation from the
// files that are the source of truth: the JSON schemas, the audit catalogue,
// the chart's golden alert render and the ADRs.
//
// A region is the text between `<!-- generated: name -->` and
// `<!-- /generated -->` in a Markdown page. Only the inside of a region is
// ever rewritten, and the output is a pure function of the sources, so the
// same tree always yields the same bytes. A marker no generator knows is an
// error, so a region cannot silently go unchecked.
//
// Not generated, on purpose: the sluisctl command reference (the CLI is
// stdlib `flag` per command with hand-written usage, so there is no command
// tree to read) and the telemetry metric catalogue (instruments are created
// inline across the packages, and their labels exist only at the call sites).
package docsgen

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	openPrefix = "<!-- generated: "
	openSuffix = " -->"
	closeMark  = "<!-- /generated -->"
)

// generator renders the body of one region. root is the repository root.
type generator func(root string) (string, error)

// regions maps a region name to the page it lives in and its generator.
var regions = map[string]struct {
	page string
	gen  generator
}{
	"config-keys":             {"docs/reference/configuration.md", configKeys},
	"config-keys-controllers": {"docs/reference/configuration.md", configKeysControllers},
	"chart-values":            {"docs/reference/chart-values.md", chartValues},
	"policy-keys":             {"docs/reference/policy.md", policyKeys},
	"audit-actions":           {"docs/reference/audit-actions.md", auditActions},
	"telemetry-alerts":        {"docs/reference/telemetry.md", telemetryAlerts},
	"adr-index":               {"docs/decisions/README.md", adrIndex},
}

// Run regenerates every region. With write it rewrites the pages that differ;
// without it nothing is written. It returns "page: region" for each region
// that was not what the generator writes.
func Run(root string, write bool) ([]string, error) {
	if err := checkMarkers(root); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(regions))
	for n := range regions {
		names = append(names, n)
	}
	slices.Sort(names)

	pages := map[string]string{}
	var stale []string
	for _, name := range names {
		r := regions[name]
		text, ok := pages[r.page]
		if !ok {
			b, err := os.ReadFile(filepath.Join(root, r.page))
			if err != nil {
				return nil, err
			}
			text = string(b)
		}
		body, err := r.gen(root)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		next, err := replaceRegion(text, name, body)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.page, err)
		}
		if next != text {
			stale = append(stale, r.page+": "+name)
		}
		pages[r.page] = next
	}
	if write {
		for page, text := range pages {
			if err := os.WriteFile(filepath.Join(root, page), []byte(text), 0o644); err != nil { //nolint:gosec // committed, world-readable
				return nil, err
			}
		}
	}
	return stale, nil
}

// replaceRegion swaps the inside of the one region called name for body.
func replaceRegion(text, name, body string) (string, error) {
	open := openPrefix + name + openSuffix
	if strings.Count(text, open) != 1 {
		return "", fmt.Errorf("want exactly one %q marker, found %d", open, strings.Count(text, open))
	}
	start := strings.Index(text, open) + len(open)
	end := strings.Index(text[start:], closeMark)
	if end < 0 {
		return "", fmt.Errorf("region %q is not closed by %q", name, closeMark)
	}
	if next := strings.Index(text[start:], openPrefix); next >= 0 && next < end {
		return "", fmt.Errorf("region %q is not closed before the next region opens", name)
	}
	return text[:start] + "\n\n" + strings.TrimRight(body, "\n") + "\n" + text[start+end:], nil
}

// checkMarkers fails on a marker in the docs that has no generator, so that a
// region nobody regenerates cannot sit in the tree looking checked.
func checkMarkers(root string) error {
	return filepath.WalkDir(filepath.Join(root, "docs"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, openPrefix) || !strings.HasSuffix(line, openSuffix) {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(line, openPrefix), openSuffix)
			if r, ok := regions[name]; !ok {
				return fmt.Errorf("%s: region %q has no generator", rel, name)
			} else if r.page != rel {
				return fmt.Errorf("%s: region %q belongs in %s", rel, name, r.page)
			}
		}
		return nil
	})
}

// cell makes text safe for one Markdown table cell.
func cell(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.ReplaceAll(s, "|", `\|`)
}
