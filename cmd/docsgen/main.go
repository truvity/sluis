// Command docsgen rewrites the generated regions of the documentation:
// the text between `<!-- generated: name -->` and `<!-- /generated -->`.
// Run it through `just docs-generate`; `just docs-check` runs it with -check
// and fails when a region differs from what it would write.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/truvity/sluis/internal/docsgen"
)

func main() {
	root := flag.String("root", ".", "the repository root")
	check := flag.Bool("check", false, "write nothing; exit 1 when a region is not what the generator writes")
	flag.Parse()

	stale, err := docsgen.Run(*root, !*check)
	if err != nil {
		fmt.Fprintln(os.Stderr, "docsgen:", err)
		os.Exit(1)
	}
	if *check && len(stale) > 0 {
		for _, s := range stale {
			fmt.Fprintln(os.Stderr, "docsgen: stale:", s)
		}
		fmt.Fprintln(os.Stderr, "docsgen: run `just docs-generate` and commit the result")
		os.Exit(1)
	}
}
