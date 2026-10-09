package docsgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// goreleaserFile is the part of a .goreleaser.yaml the artifact page reads.
type goreleaserFile struct {
	Archives []struct {
		ID           string   `yaml:"id"`
		IDs          []string `yaml:"ids"`
		Formats      []string `yaml:"formats"`
		NameTemplate string   `yaml:"name_template"`
	} `yaml:"archives"`
	Kos []struct {
		ID           string   `yaml:"id"`
		Repositories []string `yaml:"repositories"`
	} `yaml:"kos"`
}

func readGoreleaser(root, rel string) (goreleaserFile, error) {
	var g goreleaserFile
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return g, err
	}
	if err := yaml.Unmarshal(b, &g); err != nil {
		return g, fmt.Errorf("%s: %w", rel, err)
	}
	return g, nil
}

var workflowList = regexp.MustCompile(`(?m)^\s*(charts|nix-flakes|flakes|ko-docker-repo):\s*'?([^'\n]+)'?\s*$`)

// artifacts renders every deliverable a release publishes, from the two
// goreleaser files, the release workflow and the module list.
func artifacts(root string) (string, error) {
	sl, err := readGoreleaser(root, ".goreleaser.yaml")
	if err != nil {
		return "", err
	}
	au, err := readGoreleaser(root, "audit/.goreleaser.yaml")
	if err != nil {
		return "", err
	}
	wf, err := os.ReadFile(filepath.Join(root, ".github/workflows/release.yaml"))
	if err != nil {
		return "", err
	}
	var kodocker string
	var charts, flakes []string
	for _, m := range workflowList.FindAllStringSubmatch(string(wf), -1) {
		val := strings.TrimSpace(m[2])
		switch m[1] {
		case "ko-docker-repo":
			kodocker = val
		case "charts":
			charts = append(charts, jsonStrings(val)...)
		default:
			flakes = append(flakes, jsonStrings(val)...)
		}
	}
	mods, err := exec.Command("python3", filepath.Join(root, "hack/modules.py"), "list").Output() //nolint:gosec // fixed repository script
	if err != nil {
		return "", fmt.Errorf("hack/modules.py list: %w", err)
	}

	var b strings.Builder
	b.WriteString("Release assets (the GitHub release of tag `vX.Y.Z`):\n\n| Product | Asset | Contents |\n|---|---|---|\n")
	for _, a := range sl.Archives {
		fmt.Fprintf(&b, "| sluis | `%s` | %s |\n", cell(archiveName("sluis", a.NameTemplate, a.Formats)), cell(contents(a.ID, a.IDs)))
	}
	for _, a := range au.Archives {
		fmt.Fprintf(&b, "| audit | `%s` | %s |\n", cell(archiveName("audit", a.NameTemplate, a.Formats)), cell(contents(a.ID, a.IDs)))
	}
	b.WriteString("\nContainer images:\n\n| Product | Image |\n|---|---|\n")
	for _, k := range sl.Kos {
		repo := kodocker
		fmt.Fprintf(&b, "| sluis | `%s/%s` |\n", repo, k.ID)
	}
	for _, k := range au.Kos {
		fmt.Fprintf(&b, "| audit | `%s/%s` |\n", strings.Join(k.Repositories, ","), k.ID)
	}
	b.WriteString("\nHelm charts and Nix flakes:\n\n| Kind | Published at |\n|---|---|\n")
	for _, c := range charts {
		fmt.Fprintf(&b, "| chart | `oci://ghcr.io/truvity/charts/%s` |\n", c)
	}
	for _, f := range flakes {
		fmt.Fprintf(&b, "| Nix flake | `%s_<version>_nix-flake.tar.gz` |\n", f)
	}
	b.WriteString("\nGo modules, each tagged `<dir>/vX.Y.Z` beside the root tag `vX.Y.Z`:\n\n| Directory | Import path |\n|---|---|\n")
	b.WriteString("| `.` | `github.com/truvity/sluis` |\n")
	for _, dir := range strings.Fields(string(mods)) {
		fmt.Fprintf(&b, "| `%s` | `github.com/truvity/sluis/%s` |\n", dir, dir)
	}
	b.WriteString("\nPackages and actions published by the release workflow:\n\n| Kind | Name | Where |\n|---|---|---|\n")
	b.WriteString("| npm package | `@truvity/sluis` | GitHub Packages |\n")
	b.WriteString("| npm package | `@truvity/audit` | GitHub Packages |\n")
	b.WriteString("| npm package | `@truvity/audit-react` | GitHub Packages |\n")
	b.WriteString("| GitHub Action | `truvity/sluis@<commit>` | the repository root, `action.yml` |\n")
	return b.String(), nil
}

func archiveName(project, tmpl string, formats []string) string {
	n := strings.NewReplacer("{{ .ProjectName }}", project, "{{ .Version }}", "<version>", "{{ .Os }}", "<os>", "{{ .Arch }}", "<arch>").Replace(tmpl)
	ext := "tar.gz"
	if len(formats) > 0 {
		ext = formats[0]
	}
	return n + "." + ext
}

func jsonStrings(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(`[]", `, r) })
}

// contents names what an archive holds: its builds, or, for a bundle of
// files with no build, its own id.
func contents(id string, ids []string) string {
	if len(ids) == 0 {
		return id + " (bundled files)"
	}
	return strings.Join(ids, ", ")
}
