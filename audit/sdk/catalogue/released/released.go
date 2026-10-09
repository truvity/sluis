// Package released holds a catalogue document to the versions it has shipped.
//
// An audit archive stores a catalogue at catalogue/<source>/<version> exactly
// as it was registered, byte for byte, and refuses other bytes under a version
// it already holds: the writer does not start, and an application's
// registration is refused. So a released version is frozen, comments and
// spacing included, and any change to the document needs a new version.
//
// The record of what shipped sits beside each document, in
// testdata/released/: <stem>-<version>.yaml holds the exact bytes of each
// version, where <stem> is the document's file name without .yaml, and
// SHA256SUMS pins those bytes. An installation registers the .json schemas
// beside the document with it and compares them too, so SHA256SUMS also pins
// each one as <stem>-<version>/<file>. [Check] fails when the document or a
// schema differs from the record of its version, when that record is missing,
// or when a record was rewritten or removed. A test calls [CheckTree] on the repository, so a
// catalogue added anywhere is held the same way.
package released

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// Dir is where a document's released versions are kept, relative to the
// directory that holds the document.
const Dir = "testdata/released"

// Sums is the file in [Dir] that pins each released version's bytes.
const Sums = "SHA256SUMS"

type header struct {
	Source  string `json:"source"`
	Version string `json:"version"`
}

func readHeader(path string) ([]byte, header, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, header{}, err
	}
	var h header
	if err := yaml.Unmarshal(raw, &h); err != nil {
		return nil, header{}, fmt.Errorf("%s: %w", path, err)
	}
	if h.Source == "" || h.Version == "" {
		return nil, header{}, fmt.Errorf("%s: a catalogue needs `source` and `version`", path)
	}
	return raw, h, nil
}

// Check holds the catalogue document at doc to the released versions in the
// testdata/released directory beside it. It returns every problem found,
// joined, or nil.
func Check(doc string) error {
	raw, cur, err := readHeader(doc)
	if err != nil {
		return err
	}
	stem := strings.TrimSuffix(filepath.Base(doc), filepath.Ext(doc))
	dir := filepath.Join(filepath.Dir(doc), filepath.FromSlash(Dir))
	fixture := func(version string) string { return stem + "-" + version + ".yaml" }
	bump := fmt.Sprintf("set a new `version` in %s, copy it to %s and add its line to %s (sha256sum %s >> %s)",
		filepath.Base(doc), filepath.Join(dir, fixture("<version>")), Sums, fixture("<version>"), Sums)

	var errs []error
	paths, err := filepath.Glob(filepath.Join(dir, stem+"-*.yaml"))
	if err != nil {
		return err
	}
	found := false
	for _, path := range paths {
		body, h, err := readHeader(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if name := fixture(h.Version); filepath.Base(path) != name {
			errs = append(errs, fmt.Errorf("%s declares version %s: it should be named %s", path, h.Version, name))
		}
		if h.Source != cur.Source {
			errs = append(errs, fmt.Errorf("%s is a catalogue of source %q, not %q", path, h.Source, cur.Source))
		}
		if h.Version != cur.Version {
			continue
		}
		found = true
		if !bytes.Equal(body, raw) {
			errs = append(errs, fmt.Errorf("catalogue %s %s was released as %s (%d bytes, sha256 %s) and %s now differs "+
				"(%d bytes, sha256 %s): an archive holding %s refuses it. Restore %s, or %s",
				cur.Source, cur.Version, path, len(body), sum(body), doc, len(raw), sum(raw),
				cur.Version, doc, bump))
		}
	}
	if !found {
		errs = append(errs, fmt.Errorf("catalogue %s %s (%s) has no released record %s: "+
			"copy the document there and add its line to %s, so that the version cannot change once it ships",
			cur.Source, cur.Version, doc, filepath.Join(dir, fixture(cur.Version)), Sums))
	}
	errs = append(errs, checkSums(dir, stem, paths)...)
	errs = append(errs, checkSchemas(doc, dir, stem+"-"+cur.Version+"/")...)
	return errors.Join(errs...)
}

// checkSchemas holds the .json schemas beside doc to the lines of SHA256SUMS
// under prefix (<stem>-<version>/): the same files, with the same bytes. Only
// the current version's schemas are in the tree, so only they are checked; the
// lines of older versions stay as the record of what they shipped.
func checkSchemas(doc, dir, prefix string) []error {
	listing := filepath.Join(dir, Sums)
	pinned := map[string]string{}
	if raw, err := os.ReadFile(listing); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if digest, name, ok := strings.Cut(line, "  "); ok && strings.HasPrefix(name, prefix) {
				pinned[strings.TrimPrefix(name, prefix)] = digest
			}
		}
	}
	schemas, err := filepath.Glob(filepath.Join(filepath.Dir(doc), "*.json"))
	if err != nil {
		return []error{err}
	}
	var errs []error
	for _, path := range schemas {
		name := filepath.Base(path)
		body, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		want, ok := pinned[name]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("schema %s is not pinned for this version: add `%s  %s%s` to %s; "+
				"if the version has shipped, the schema is a change and needs a new version", path, sum(body), prefix, name, listing))
		case sum(body) != want:
			errs = append(errs, fmt.Errorf("schema %s differs from the one released under %s (sha256 %s, pinned %s): "+
				"an installation refuses it. Restore it, or give the change a new version", path, strings.TrimSuffix(prefix, "/"), sum(body), want))
		}
		delete(pinned, name)
	}
	gone := make([]string, 0, len(pinned))
	for name := range pinned {
		gone = append(gone, name)
	}
	sort.Strings(gone)
	for _, name := range gone {
		errs = append(errs, fmt.Errorf("%s pins schema %s%s, which is no longer beside %s: an installation holds it under this version",
			listing, prefix, name, doc))
	}
	return errs
}

// checkSums holds each record of stem to its line in SHA256SUMS. Comparing the
// document with its record proves nothing when both were edited together (a
// rename once rewrote every catalogue document and its records alike, and
// every installation refused the "same" version), so the records are pinned
// too: a new version adds a line, and an existing line never changes.
func checkSums(dir, stem string, paths []string) []error {
	listing := filepath.Join(dir, Sums)
	raw, err := os.ReadFile(listing)
	if errors.Is(err, fs.ErrNotExist) && len(paths) == 0 {
		return nil // reported as the missing record of the current version
	}
	if err != nil {
		return []error{err}
	}
	var errs []error
	pinned := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		digest, name, ok := strings.Cut(line, "  ")
		if !ok {
			errs = append(errs, fmt.Errorf("%s: malformed line %q", listing, line))
			continue
		}
		if strings.HasPrefix(name, stem+"-") && !strings.Contains(name, "/") {
			pinned[name] = digest
		}
	}
	for _, path := range paths {
		name := filepath.Base(path)
		body, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		want, ok := pinned[name]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("%s is not in %s: add its line (sha256sum %s >> %s)", path, listing, name, Sums))
		case sum(body) != want:
			errs = append(errs, fmt.Errorf("%s was rewritten after release: an archive holds the old bytes. "+
				"Restore it, and give the change a new version", path))
		}
		delete(pinned, name)
	}
	gone := make([]string, 0, len(pinned))
	for name := range pinned {
		gone = append(gone, name)
	}
	sort.Strings(gone)
	for _, name := range gone {
		errs = append(errs, fmt.Errorf("%s lists %s, which is gone: a released record is never removed", listing, name))
	}
	return errs
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

var (
	sourceLine  = regexp.MustCompile(`(?m)^source:\s*\S`)
	versionLine = regexp.MustCompile(`(?m)^version:\s*\S`)
	actionsLine = regexp.MustCompile(`(?m)^actions:\s*$`)
)

// skipped are the directories [Find] does not enter: the released records
// themselves, examples, which ship nothing, and what is not source.
var skipped = map[string]bool{
	"testdata": true, "examples": true, "node_modules": true, "vendor": true, "dist": true,
}

// Find returns every catalogue document below root: a .yaml file with a
// top-level `source`, `version` and `actions`, outside testdata, examples and
// hidden directories.
func Find(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (skipped[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".yaml" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if sourceLine.Match(raw) && versionLine.Match(raw) && actionsLine.Match(raw) {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

// CheckTree runs [Check] on every catalogue document [Find] returns below root.
// It refuses a tree with none, since a check that found nothing proves nothing.
func CheckTree(root string) ([]string, error) {
	docs, err := Find(root)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("no catalogue document found below %s: the check would pass on nothing", root)
	}
	var errs []error
	for _, doc := range docs {
		errs = append(errs, Check(doc))
	}
	return docs, errors.Join(errs...)
}

// RepositoryRoot returns the nearest directory at or above dir that holds a
// .git entry (a directory, or the file of a worktree), and false outside a
// checkout, as in a module downloaded from a proxy.
func RepositoryRoot(dir string) (string, bool) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
