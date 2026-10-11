package releasecheck

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/config"
)

// TestTheReleaseMatrixIsTheModuleRegistry holds every artifact list to
// config.ZipModules (the module registry): the zips of hack/release-zips.txt,
// the archives and builds of .goreleaser.yaml (named by config.ZipName, the
// constant the Pulumi library's default package names come from), the module
// images, and the entries of cmd/. Nothing is missing and nothing is left over
// (`github-mint`, `accessctl`).
func TestTheReleaseMatrixIsTheModuleRegistry(t *testing.T) {
	want := slices.Clone(config.ZipModules())
	slices.Sort(want)

	var listed []string
	for _, z := range releaseZips(t) {
		if z.kind != "module" {
			t.Errorf("release-zips.txt lists a %s zip %s: every zip is a pinned module's", z.kind, z.name)
		}
		listed = append(listed, z.name)
	}
	slices.Sort(listed)
	if !slices.Equal(listed, want) {
		t.Errorf("hack/release-zips.txt lists %v, the module registry has %v", listed, want)
	}

	raw, err := os.ReadFile(filepath.Join(root, ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Builds   []buildCfg   `yaml:"builds"`
		Archives []zipArchive `yaml:"archives"`
	}
	if err = yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	var names, pins []string
	for _, a := range cfg.Archives {
		if len(a.Formats) == 1 && a.Formats[0] == "zip" && len(a.IDs) == 1 && strings.HasPrefix(a.Name, "sluis-") {
			names = append(names, a.Name)
		}
	}
	slices.Sort(names)
	var wantNames []string
	for _, m := range want {
		// config.ZipName with goreleaser's template fields for the version.
		n := strings.TrimSuffix(config.ZipName(m, "{{ .Version }}"), "_linux_arm64.zip") + "_{{ .Os }}_{{ .Arch }}"
		wantNames = append(wantNames, n)
	}
	slices.Sort(wantNames)
	if !slices.Equal(names, wantNames) {
		t.Errorf(".goreleaser.yaml zip archives are %v, config.ZipName gives %v", names, wantNames)
	}
	for _, b := range cfg.Builds {
		if b.Binary != "bootstrap" {
			continue
		}
		for _, l := range b.Ldflags {
			if m := pinRE.FindStringSubmatch(l); m != nil {
				pins = append(pins, strings.TrimPrefix(m[1], "sluis-module="))
			}
		}
		if len(pinRE.FindAllString(strings.Join(b.Ldflags, " "), -1)) != 1 {
			t.Errorf("build %s has not exactly one module pin", b.ID)
		}
	}
	slices.Sort(pins)
	if !slices.Equal(pins, want) {
		t.Errorf("the Lambda builds pin modules %v, the registry has %v: a zip would run as another module or be missing", pins, want)
	}

	// Module images: one per registry module, and no other sluis-* image.
	imgs := imageRefs(t)
	var gotImgs []string
	imgRE := regexp.MustCompile(`^ghcr\.io/truvity/sluis/sluis-(.+)$`)
	for _, i := range imgs {
		if m := imgRE.FindStringSubmatch(i); m != nil {
			gotImgs = append(gotImgs, m[1])
		}
	}
	slices.Sort(gotImgs)
	if !slices.Equal(gotImgs, want) {
		t.Errorf("hack/release-images.txt has module images %v, the registry has %v", gotImgs, want)
	}

	// cmd/: one entry point per zip, and no orphan.
	for _, m := range want {
		if _, err := os.Stat(filepath.Join(root, "cmd", "sluis-"+m)); err != nil {
			t.Errorf("module %s has no cmd/sluis-%s", m, m)
		}
	}
	for _, orphan := range []string{"accessctl", "github-mint", "sluis-github-mint", "sluis-accessctl"} {
		if _, err := os.Stat(filepath.Join(root, "cmd", orphan)); err == nil {
			t.Errorf("cmd/%s exists: an orphan artifact", orphan)
		}
		if strings.Contains(string(raw), orphan) {
			t.Errorf(".goreleaser.yaml names the orphan %s", orphan)
		}
	}
}

// imageRefs are the references of hack/release-images.txt.
func imageRefs(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "hack", "release-images.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && f[0] == "image" {
			out = append(out, f[1])
		}
	}
	return out
}

// TestOnlySluisctlIsAFlake: the Nix flakes the release publishes are
// sluisctl's, the one thing run on a laptop.
func TestOnlySluisctlIsAFlake(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, l := range strings.Split(string(raw), "\n") {
		if i := strings.Index(l, "nix-flakes:"); i >= 0 && !strings.HasPrefix(strings.TrimSpace(l), "#") {
			found = append(found, strings.TrimSpace(l[i+len("nix-flakes:"):]))
		}
	}
	if len(found) != 1 || strings.Trim(found[0], "'\" ") != `["sluisctl"]` {
		t.Errorf("release.yaml nix-flakes = %v, want exactly [\"sluisctl\"]", found)
	}
}
