// Command gen writes docs/reference/adapters.md. Run it through
// `just adapters-doc`; the matrixdoc test fails when the committed file is
// not what it writes.
package main

import (
	"fmt"
	"os"

	"github.com/truvity/sluis/internal/port/matrixdoc"
)

func main() {
	path := matrixdoc.File
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	if err := os.WriteFile(path, []byte(matrixdoc.Render()), 0o644); err != nil { //nolint:gosec // committed, world-readable
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
