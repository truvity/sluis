package secretsexport_test

import (
	"context"
	"errors"
	"testing"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/port/secretsexport"
)

var ctx = context.Background()

func TestReplacePatchAndIdempotence(t *testing.T) {
	sec := memory.NewSecrets()
	e := secretsexport.New(sec)
	target := port.ExportTarget{Path: "slack-apps/alerts"}

	if err := e.Put(ctx, target, map[string]string{"b": "2", "a": "<1>"}, port.ExportPatch); err != nil {
		t.Fatal(err)
	}
	got, err := sec.Get(ctx, "export/slack-apps/alerts")
	if err != nil || string(got.Value) != `{"a":"<1>","b":"2"}` {
		t.Fatalf("export = %s, %v", got.Value, err)
	}
	// Identical: no new version. A patch keeps the other properties.
	if err = e.Put(ctx, target, map[string]string{"a": "<1>"}, port.ExportPatch); err != nil {
		t.Fatal(err)
	}
	same, _ := sec.Get(ctx, "export/slack-apps/alerts")
	if same.Version != got.Version {
		t.Errorf("an identical patch made version %s -> %s", got.Version, same.Version)
	}
	if err = e.Put(ctx, target, map[string]string{"c": "3"}, port.ExportPatch); err != nil {
		t.Fatal(err)
	}
	if v, _ := sec.Get(ctx, "export/slack-apps/alerts"); string(v.Value) != `{"a":"<1>","b":"2","c":"3"}` {
		t.Errorf("after a patch: %s", v.Value)
	}
	// A replace is exactly the properties.
	if err = e.Put(ctx, target, map[string]string{"z": "9"}, port.ExportReplace); err != nil {
		t.Fatal(err)
	}
	if v, _ := sec.Get(ctx, "export/slack-apps/alerts"); string(v.Value) != `{"z":"9"}` {
		t.Errorf("after a replace: %s", v.Value)
	}
	if err = e.Delete(ctx, target); err != nil {
		t.Fatal(err)
	}
	if _, err = sec.Get(ctx, "export/slack-apps/alerts"); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("after Delete: %v", err)
	}
}

func TestRefusals(t *testing.T) {
	e := secretsexport.New(memory.NewSecrets())
	if err := e.Put(ctx, port.ExportTarget{Path: "a"}, nil, port.ExportPatch); !errors.Is(err, port.ErrNoProperties) {
		t.Errorf("no properties: %v", err)
	}
	if err := e.Put(ctx, port.ExportTarget{Namespace: "kernel", Path: "a"}, map[string]string{"a": "b"}, port.ExportPatch); !errors.Is(err, port.ErrUnsupported) {
		t.Errorf("a namespace: %v", err)
	}
	if err := e.Put(ctx, port.ExportTarget{Path: "a b"}, map[string]string{"a": "b"}, port.ExportPatch); !errors.Is(err, port.ErrUnsupported) {
		t.Errorf("a path with a space: %v", err)
	}
}
