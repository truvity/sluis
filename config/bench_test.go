package config_test

import (
	"path/filepath"
	"testing"

	"github.com/truvity/sluis/config"
)

// BenchmarkRender is what a stack does on each NewLambda with the same
// installation: render the two documents and the module documents, each held to
// its schema and loaded back as its function loads it. The same bytes recur
// from call to call, so every iteration after the first is a repeat.
func BenchmarkRender(b *testing.B) {
	in, err := config.LoadInstallation(filepath.Join("testdata", "modules-full.installation.yaml"))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := config.Render(in); err != nil {
			b.Fatal(err)
		}
		docs, err := config.RenderModules(in, config.ModuleOptions{})
		if err != nil {
			b.Fatal(err)
		}
		for _, m := range config.Modules() {
			if doc, ok := docs.Modules[m]; ok {
				if _, err := config.ModuleSecrets(m, doc, docs.Policy); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
}
