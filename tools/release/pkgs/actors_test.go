package pkgs

import (
	"os"
	"path/filepath"
	"testing"
)

// LoadActors names an actor by its executable, and reads audit's pieces after the actors.
func TestLoadActorsReadsTheExecutableAndTheAuditPieces(t *testing.T) {
	p := filepath.Join(t.TempDir(), "actors.yaml")
	const doc = `
schema: 1
actors:
  - {name: entry, family: work}
  - {name: signin, executable: session, family: work}
  - {name: store, family: data}
audit_pieces:
  - {name: writer, family: audit}
`
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadActors(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []Actor{
		{Name: "entry", Family: "work"},
		{Name: "session", Executable: "session", Family: "work"},
		{Name: "store", Family: "data"},
		{Name: "writer", Family: "audit"},
	}
	if len(got) != len(want) {
		t.Fatalf("LoadActors = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("actor %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
