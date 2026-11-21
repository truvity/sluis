package main

import (
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// The three registries and the uid list, as far as the generator reads them. The headers of
// docs/architecture/{actors,kinds,keys}.yaml say what every field means.
const (
	actorsFile = "docs/architecture/actors.yaml"
	kindsFile  = "docs/architecture/kinds.yaml"
	keysFile   = "docs/architecture/keys.yaml"
	uidsFile   = "tools/gen/registry/uids.yaml"
)

type keyPurposeUse struct {
	Purpose string   `yaml:"purpose"`
	Ops     []string `yaml:"ops"`
}

type actorDoc struct {
	Name string `yaml:"name"`
	// Executable is the name of the program, binary, unit and uid when it differs from the name of the actor in the
	// docs: `signin` runs as `session` until the planned step that renames the executable. The generators use it
	// wherever they name the actor.
	Executable  string          `yaml:"executable"`
	Family      string          `yaml:"family"`
	Holder      bool            `yaml:"holder"`
	Language    string          `yaml:"language"`
	Contracts   []string        `yaml:"contracts"`
	Ports       []string        `yaml:"ports"`
	Calls       []string        `yaml:"calls"`
	Egress      []string        `yaml:"egress"`
	KeyPurposes []keyPurposeUse `yaml:"key_purposes"`
}

type actorsDoc struct {
	Schema int        `yaml:"schema"`
	Actors []actorDoc `yaml:"actors"`
	// AuditPieces are audit's pieces that still run from this repository (the writer, witness, indexer and reader).
	// They are read like actors, after them.
	AuditPieces []actorDoc `yaml:"audit_pieces"`
	Platform    []actorDoc `yaml:"platform"`
	Externals   []actorDoc `yaml:"external_callers"`
}

type ttlDoc struct {
	Default string `yaml:"default"`
	Max     string `yaml:"max"`
}

type kindDoc struct {
	Name       string   `yaml:"name"`
	Scope      string   `yaml:"scope"`
	Class      string   `yaml:"class"`
	Treatment  string   `yaml:"treatment"`
	Profile    string   `yaml:"profile"`
	Writer     string   `yaml:"writer"`
	Readers    []string `yaml:"readers"`
	WatchedBy  []string `yaml:"watched_by"`
	Exportable string   `yaml:"exportable"`
	BackedUp   string   `yaml:"backed_up"`
	TTL        *ttlDoc  `yaml:"ttl"`
	Indexes    []string `yaml:"indexes"`
	SelfScoped bool     `yaml:"self_scoped"`
	CreateOnly bool     `yaml:"create_only"`
	Personal   bool     `yaml:"personal"`
	Links      []string `yaml:"links"`
	Status     string   `yaml:"status"`
}

type kindsDoc struct {
	Schema int       `yaml:"schema"`
	Kinds  []kindDoc `yaml:"kinds"`
}

type purposeDoc struct {
	Name  string   `yaml:"name"`
	Ops   []string `yaml:"ops"`
	Users []string `yaml:"users"`
}

type dataKeyDoc struct {
	Ops   []string `yaml:"ops"`
	Users []string `yaml:"users"`
}

type keysDoc struct {
	Schema   int          `yaml:"schema"`
	Purposes []purposeDoc `yaml:"purposes"`
	DataKeys struct {
		Tenant   map[string]dataKeyDoc `yaml:"tenant"`
		Boundary map[string]dataKeyDoc `yaml:"boundary"`
	} `yaml:"data_keys"`
}

type uidsDoc struct {
	Names []string `yaml:"names"`
}

// inputs is what the generator reads.
type inputs struct {
	actors actorsDoc
	kinds  kindsDoc
	keys   keysDoc
	uids   uidsDoc
}

func readYAML(root, rel string, into any) error {
	b, err := os.ReadFile(filepath.Join(root, rel)) // #nosec G304 -- the generator reads its own repository
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(b, into); err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	return nil
}

func load(root string) (*inputs, error) {
	in := &inputs{}
	for _, f := range []struct {
		rel  string
		into any
	}{
		{actorsFile, &in.actors},
		{kindsFile, &in.kinds},
		{keysFile, &in.keys},
		{uidsFile, &in.uids},
	} {
		if err := readYAML(root, f.rel, f.into); err != nil {
			return nil, err
		}
	}
	for _, c := range []struct {
		file   string
		schema int
	}{{actorsFile, in.actors.Schema}, {kindsFile, in.kinds.Schema}, {keysFile, in.keys.Schema}} {
		if c.schema != 1 {
			return nil, fmt.Errorf("%s: schema %d is not the schema 1 this generator reads", c.file, c.schema)
		}
	}
	in.toExecutables()
	return in, nil
}

// toExecutables makes the inputs speak in executable names: the audit pieces join the actors, and an actor whose row says
// `executable:` is named so everywhere (its own row, `calls`, the writer, readers and watchers of a kind, and the users of a key
// purpose). The generated files therefore name an actor as its program, its unit and its uid do.
func (in *inputs) toExecutables() {
	in.actors.Actors = append(in.actors.Actors, in.actors.AuditPieces...)
	in.actors.AuditPieces = nil
	exe := map[string]string{}
	for _, list := range [][]actorDoc{in.actors.Actors, in.actors.Platform} {
		for _, a := range list {
			if a.Executable != "" && a.Executable != a.Name {
				exe[a.Name] = a.Executable
			}
		}
	}
	if len(exe) == 0 {
		return
	}
	for _, list := range [][]actorDoc{in.actors.Actors, in.actors.Platform, in.actors.Externals} {
		for i := range list {
			list[i].Name = mapName(list[i].Name, exe)
			list[i].Calls = mapNames(list[i].Calls, exe)
		}
	}
	for i := range in.kinds.Kinds {
		k := &in.kinds.Kinds[i]
		k.Writer = mapToken(k.Writer, exe)
		k.Readers = mapTokens(k.Readers, exe)
		k.WatchedBy = mapTokens(k.WatchedBy, exe)
	}
	for i := range in.keys.Purposes {
		in.keys.Purposes[i].Users = mapNames(in.keys.Purposes[i].Users, exe)
	}
	for _, m := range []map[string]dataKeyDoc{in.keys.DataKeys.Tenant, in.keys.DataKeys.Boundary} {
		for n, d := range m {
			d.Users = mapNames(d.Users, exe)
			m[n] = d
		}
	}
}

func mapName(n string, exe map[string]string) string {
	if to, ok := exe[n]; ok {
		return to
	}
	return n
}

func mapNames(ns []string, exe map[string]string) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = mapName(n, exe)
	}
	return out
}

// mapToken maps the actor name that starts a writer or reader token: `signin`, `signin@` and `signin (own id)` all name it.
func mapToken(tok string, exe map[string]string) string {
	i := 0
	for i < len(tok) && tok[i] == ' ' {
		i++
	}
	j := i
	for j < len(tok) && (tok[j] >= 'a' && tok[j] <= 'z' || tok[j] >= '0' && tok[j] <= '9' || tok[j] == '-') {
		j++
	}
	if to, ok := exe[tok[i:j]]; ok {
		return tok[:i] + to + tok[j:]
	}
	return tok
}

func mapTokens(ts []string, exe map[string]string) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = mapToken(t, exe)
	}
	return out
}
