package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/truvity/sluis/config"
)

// The two files `sluisctl render` writes, in the directory it is given.
const (
	renderedService = "sluis.yaml"
	renderedPolicy  = "policy.yaml"
)

const renderUsage = `Usage: sluisctl render --installation <file> --out <dir> [--check]

Renders an installation (apiVersion sluis.truvity.github.io/installation/v1,
schemas/config/installation.schema.json) into the two documents sluis reads:
sluis.yaml, the service document (sluis/v3), and policy.yaml, the policy
document (policy/v2), both written to <dir>. The output is deterministic (sorted
keys, a fixed layout) and holds both documents to the loader the service runs at
start, so what this writes is what the service accepts. It touches nothing but
the files it is given: no session, no network.

  --installation  the installation document (required)
  --out           the directory the two documents are written to (required)
  --check         write nothing: compare what would be written with the files
                  in <dir>, print the difference and exit 1 if there is any.
                  For a CI job that holds committed documents to their source.
`

// renderCommand is `sluisctl render`: what an estate's tooling runs to turn the
// installation it keeps into the documents its processes are started with.
func renderCommand(args []string) error {
	flags := flag.NewFlagSet("render", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(os.Stderr, renderUsage) }
	file := flags.String("installation", "", "the installation document")
	out := flags.String("out", "", "the directory the documents are written to")
	check := flags.Bool("check", false, "compare with the files in --out instead of writing them")
	if err := flags.Parse(args); err != nil {
		return usageError{err}
	}
	if flags.NArg() > 0 {
		return badUsage("sluisctl render takes no arguments: only --installation, --out and --check")
	}
	if *file == "" || *out == "" {
		return badUsage("sluisctl render needs --installation <file> and --out <dir>")
	}
	in, err := config.LoadInstallation(*file)
	if err != nil {
		return err
	}
	service, policy, err := config.Render(in)
	if err != nil {
		return err
	}
	docs := []struct {
		name string
		body []byte
	}{{renderedService, service}, {renderedPolicy, policy}}
	if *check {
		return checkRendered(*out, docs)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil { //nolint:gosec // the documents hold no secret and are reviewed in git
		return err
	}
	for _, d := range docs {
		if err := os.WriteFile(filepath.Join(*out, d.name), d.body, 0o644); err != nil { //nolint:gosec // as above
			return err
		}
	}
	return nil
}

// errRenderedDiffers is the exit of `render --check` with a difference: the
// diff has been printed, and the process exits 1.
var errRenderedDiffers = errors.New("the rendered documents differ from the files in --out: run sluisctl render without --check and commit the result")

func checkRendered(dir string, docs []struct {
	name string
	body []byte
}) error {
	differs := false
	for _, d := range docs {
		path := filepath.Join(dir, d.name)
		have, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			_, _ = fmt.Fprintf(stdout, "%s: missing\n", path)
			differs = true
			continue
		case err != nil:
			return err
		}
		if diff := lineDiff(path, string(have), "rendered", string(d.body)); diff != "" {
			_, _ = fmt.Fprint(stdout, diff)
			differs = true
		}
	}
	if differs {
		return errRenderedDiffers
	}
	return nil
}

// lineDiff is a unified-style difference of two texts, or "" when they are the
// same. It is the longest common subsequence of the lines, which is plenty for
// documents of hundreds of lines; it has no context lines, so the whole of a
// changed region is shown and nothing else.
func lineDiff(aName, a, bName, b string) string {
	if a == b {
		return ""
	}
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", aName, bName)
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			i, j = i+1, j+1
		case j < len(y) && (i == len(x) || lcs[i][j+1] >= lcs[i+1][j]):
			fmt.Fprintf(&sb, "+%s\n", y[j])
			j++
		default:
			fmt.Fprintf(&sb, "-%s\n", x[i])
			i++
		}
	}
	return sb.String()
}
