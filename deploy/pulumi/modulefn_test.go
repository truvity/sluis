package sluispulumi_test

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// backupShape is every resource NewBackup declares on the mocks, by type and
// logical name, the physical names that identify the replaceable ones, and
// nothing else. A preview creates exactly what is here and not in the state,
// and replaces a resource whose logical name or physical name moves.
const backupShape = "testdata/backup-shape.txt"

func shapeOf(rec *recorder) string {
	var lines []string
	for _, n := range rec.names() {
		lines = append(lines, n)
	}
	for _, typ := range []string{
		"aws:lambda/function:Function", "aws:iam/role:Role", "aws:cloudwatch/logGroup:LogGroup",
		"aws:lambda/layerVersion:LayerVersion", "aws:lambda/alias:Alias",
	} {
		for _, d := range rec.ofType(typ) {
			for _, k := range []string{"name", "layerName", "functionName", "role"} {
				if v := prop(d, k); v.HasValue() && v.IsString() {
					lines = append(lines, "name "+d.Type+"/"+d.Name+" "+k+"="+v.StringValue())
				}
			}
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

// Backup moved onto the generic module-function builder with no resource
// change: a preview of an installed backup creates 0 and replaces 0, because
// every logical name and every physical name is the one it had.
func TestBackupOnTheModuleFunctionBuilderChangesNoResource(t *testing.T) {
	rec, _ := mustBackup(t, nil)
	got := shapeOf(rec)
	if os.Getenv("SLUIS_UPDATE_SHAPE") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(backupShape, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(backupShape)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("the backup's resources moved (a create or a replace in a preview):\n--- want\n%s--- got\n%s", want, got)
	}
}
