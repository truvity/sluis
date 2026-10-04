package matrixdoc

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTheMatrixIsNotStale fails when docs/reference/adapters.md is not what
// the registry says. Fix it with `just adapters-doc`.
func TestTheMatrixIsNotStale(t *testing.T) {
	want := Render()
	got, err := os.ReadFile(filepath.Join("..", "..", "..", File))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s is stale: run `just adapters-doc` and commit the result", File)
	}
}
