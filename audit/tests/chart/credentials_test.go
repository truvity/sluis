package chart_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/truvity/sluis/audit/internal/cli"
	"github.com/truvity/sluis/audit/internal/config"
	"github.com/truvity/sluis/audit/profile"
)

// The contract between the chart and the binaries, for each way a preset's
// credentials are given: every configuration the chart renders is loaded by
// the loader its binary starts with (schema, decoding and the checks after
// them), and every process that opens the archive plans its preset with the
// credentials the values meant. A chart rendering a field a binary refuses is
// caught here, before a pod fails at start.
//
// archiveOpeners are the components whose binaries open the archive; the
// rest (the receiver, migrate, purge, clock-sync) take no `archive` block.
var archiveOpeners = []string{"notary", "observe", "query", "verify", "writer"}

// startup loads a rendered configuration file as its binary does at start,
// and returns the archive block it opens the archive with, if it opens one.
func startup(schema, file string) (*config.Archive, error) {
	switch schema {
	case "audit-writer":
		c, err := config.LoadWriter(file)
		if err != nil || c.Mode != "writer" {
			return nil, err
		}
		return c.Archive, nil
	case "audit-query":
		c, err := config.LoadQuery(file)
		if err != nil {
			return nil, err
		}
		return c.Archive, nil
	case "audit-observe":
		c, err := config.LoadObserve(file)
		if err != nil {
			return nil, err
		}
		return &c.Archive, nil
	case "audit-notary":
		c, err := config.LoadNotary(file)
		if err != nil {
			return nil, err
		}
		return &c.Archive, nil
	case "audit-verify":
		c, err := config.LoadVerify(file)
		if err != nil {
			return nil, err
		}
		return &c.Archive, nil
	case "audit-purge":
		_, err := config.LoadPurge(file)
		return nil, err
	case "audit-clock-sync":
		_, err := config.LoadClockSync(file)
		return nil, err
	case "audit-migrate":
		_, err := config.LoadMigrate(file)
		return nil, err
	}
	panic("no loader for " + schema)
}

// r2Store is a preset's store on an S3-compatible endpoint, before its
// credentials.
const r2Store = "bucket: audit-standard\n    region: auto\n    endpoint: https://s3.example.test\n    path_style: true\n"

// credentialModes are the ways a preset's store is reached, each with the
// preset's fields, the `archive` block every opener is given, the Secret
// mounts the opener is given, and what the plan must say.
var credentialModes = []struct {
	name    string
	preset  string
	archive string
	mounts  string
	want    func(p cli.PresetPlan) bool
}{
	{
		name:    "aws",
		preset:  "bucket: audit-standard\n    region: eu-example-1",
		archive: "{}",
		want:    func(p cli.PresetPlan) bool { return p.Stored == nil && p.Minted == nil && p.Credentials == nil },
	},
	{
		name:    "credentials",
		preset:  r2Store + "    credentials: internal/audit/r2",
		archive: "{stateRoot: /audit/main}",
		want: func(p cli.PresetPlan) bool {
			return p.Credentials != nil && p.Credentials.Root == "/audit/main" && p.Stored == nil
		},
	},
	{
		name: "credentials_preset",
		preset: r2Store +
			"    credentials_preset: {account: example-account, minter: internal/cloudflare/minter, prototype: example-prototype, lifetime: 15m}",
		archive: "{stateRoot: /audit/main}",
		want: func(p cli.PresetPlan) bool {
			return p.Minted != nil && p.Minted.Root == "/audit/main" && p.Stored == nil
		},
	},
	{
		// In a cluster: the secrets operator projects the document sluis
		// rotates into a Secret, mounted as a directory.
		name:    "credentials_ref-sluisDir",
		preset:  r2Store + "    credentials_ref: external/cloudflare/audit-r2",
		archive: "{sluisDir: /etc/audit/sluis}",
		mounts:  "[{secretName: audit-r2, mountPath: /etc/audit/sluis/external/cloudflare}]",
		want: func(p cli.PresetPlan) bool {
			return p.Stored != nil && p.Stored.Dir == "/etc/audit/sluis" && p.Stored.Root == "" &&
				p.Stored.Ref == "external/cloudflare/audit-r2" && p.Stored.Endpoint == "https://s3.example.test"
		},
	},
	{
		// With an identity that reads SSM: the document is read below the
		// sluis installation's root.
		name:    "credentials_ref-sluisRoot",
		preset:  r2Store + "    credentials_ref: external/cloudflare/audit-r2",
		archive: "{sluisRoot: /sluis/main}",
		want: func(p cli.PresetPlan) bool {
			return p.Stored != nil && p.Stored.Root == "/sluis/main" && p.Stored.Dir == "" &&
				p.Stored.Ref == "external/cloudflare/audit-r2"
		},
	},
}

// everyComponent is an install that renders every component that has a
// configuration: the writer, the query service, the indexer and its schema
// migration, and the four jobs. PRESET, ARCHIVE and MOUNTS are filled per mode.
const everyComponent = `mode: direct
externalIdentifiersAreOpaque: true
presets:
  standard:
    PRESET
profiles:
  security:
    frameworks: [security]
writer:
  config:
    apiVersion: audit.truvity.github.io/audit-writer/v2
    deployment: /etc/audit/deployment.yaml
    anonymousWrites: true
    archive: ARCHIVE
  secretMounts: MOUNTS
query:
  enabled: true
  replicas: 1
  grants:
    issuers:
      - url: https://id.example.test
        audience: audit
  config:
    apiVersion: audit.truvity.github.io/audit-query/v2
    searcher: s3scan
    deployment: /etc/audit/deployment.yaml
    grants: /etc/audit/grants.yaml
    archive: ARCHIVE
    sink:
      url: http://audit:8080
  secretMounts: MOUNTS
observe:
  enabled: true
  config:
    apiVersion: audit.truvity.github.io/audit-observe/v2
    deployment: /etc/audit/deployment.yaml
    archive: ARCHIVE
    database:
      url: postgres://audit_observe@postgres.example.test:5432/audit?sslmode=require
      passwordSecret: database-password
    secrets: {source: file, root: /etc/audit/secrets}
  secretFiles:
    - {name: database-password, secretName: audit-pg-observe, key: password}
  secretMounts: MOUNTS
migrate:
  enabled: true
  config:
    apiVersion: audit.truvity.github.io/audit-migrate/v2
    database:
      url: postgres://audit@postgres.example.test:5432/audit?sslmode=require
      passwordSecret: database-password
    secrets: {source: file, root: /etc/audit/secrets}
    observe: audit_observe
    reader: audit_query
  secretFiles:
    - {name: database-password, secretName: audit-pg-owner, key: password}
jobs:
  notary:
    enabled: true
    config:
      apiVersion: audit.truvity.github.io/audit-notary/v2
      deployment: /etc/audit/deployment.yaml
      archive: ARCHIVE
      keys:
        adapter: kms
        instance: audit
        seal: {key: alias/audit-seal}
      sink:
        url: http://audit:8080
    secretMounts: MOUNTS
  verify:
    config:
      apiVersion: audit.truvity.github.io/audit-verify/v2
      deployment: /etc/audit/deployment.yaml
      archive: ARCHIVE
    secretMounts: MOUNTS
  purge:
    config:
      apiVersion: audit.truvity.github.io/audit-purge/v2
      deployment: /etc/audit/deployment.yaml
      database:
        url: postgres://audit_purge@postgres.example.test:5432/audit?sslmode=require
        passwordSecret: database-password
      secrets: {source: file, root: /etc/audit/secrets}
    secretFiles:
      - {name: database-password, secretName: audit-pg-purge, key: password}
  clockSync:
    config:
      apiVersion: audit.truvity.github.io/audit-clock-sync/v2
      ntp: [time.example.test]
`

func TestEveryRenderedConfigurationIsOneItsBinaryStartsWith(t *testing.T) {
	for _, mode := range credentialModes {
		t.Run(mode.name, func(t *testing.T) {
			mounts := mode.mounts
			if mounts == "" {
				mounts = "[]"
			}
			values := strings.NewReplacer("PRESET", mode.preset, "ARCHIVE", mode.archive, "MOUNTS", mounts).Replace(everyComponent)
			if mode.name == "credentials_preset" {
				// The mode puts the minter in every process: the chart renders it
				// only when the installation says so.
				values = "acknowledgeMinterCustody: true\n" + values
			}
			out, stderr, err := helmOutput(t, values)
			if err != nil {
				t.Fatalf("helm template: %v\n%s", err, stderr)
			}
			var docs []map[string]any
			for _, part := range strings.Split(out, "\n---\n") {
				var doc map[string]any
				if err := yaml.Unmarshal([]byte(part), &doc); err != nil {
					t.Fatal(err)
				}
				if doc != nil {
					docs = append(docs, doc)
				}
			}

			var deployment *profile.Deployment
			for _, doc := range docs {
				if name, _ := dig(doc, "metadata", "name"); doc["kind"] == "ConfigMap" && name == "audit-deployment" {
					raw, _ := dig(doc, "data", "deployment.yaml")
					if deployment, err = profile.ParseDeployment([]byte(raw.(string))); err != nil {
						t.Fatalf("the rendered deployment document: %v", err)
					}
				}
			}
			if deployment == nil {
				t.Fatal("no deployment document was rendered")
			}

			dir := t.TempDir()
			loaded := map[string]bool{}
			opened := map[string]bool{}
			for _, doc := range docs {
				if doc["kind"] != "ConfigMap" {
					continue
				}
				name, _ := dig(doc, "metadata", "name")
				component, ok := dig(doc, "metadata", "labels", "app.kubernetes.io/component")
				data, hasData := dig(doc, "data", "config.yaml")
				if !ok || !hasData || !strings.HasSuffix(name.(string), "-config") {
					continue
				}
				c, known := components[component.(string)]
				if !known {
					t.Errorf("%s: a configuration for a component this test does not know", name)
					continue
				}
				file := filepath.Join(dir, name.(string)+".yaml")
				if err := os.WriteFile(file, []byte(data.(string)), 0o600); err != nil {
					t.Fatal(err)
				}
				archive, err := startup(c.schema, file)
				if err != nil {
					t.Errorf("%s: %s refuses the file the chart renders for it: %v", name, c.schema, err)
					continue
				}
				loaded[component.(string)] = true
				if archive == nil {
					continue
				}
				opened[component.(string)] = true
				// The archive the binary opens: its preset is planned with the
				// credentials the values gave, not merely accepted.
				plans, err := cli.PlanPresets(deployment, *archive)
				if err != nil {
					t.Errorf("%s: the archive cannot be opened: %v", name, err)
					continue
				}
				if p, ok := plans[profile.Standard]; !ok || !mode.want(p) {
					t.Errorf("%s: the standard preset is not planned with the %s credentials: %+v", name, mode.name, plans[profile.Standard])
				}
			}

			// A sweep that loaded nothing proved nothing: every component this
			// install configures was rendered and loaded, and every one that
			// opens the archive planned it.
			for component := range components {
				if component == "receiver" {
					continue // stream mode only
				}
				if !loaded[component] {
					t.Errorf("%s: no configuration was rendered and loaded", component)
				}
			}
			var got []string
			for component := range opened {
				got = append(got, component)
			}
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(archiveOpeners, ",") {
				t.Errorf("the components that opened the archive are %v, want %v", got, archiveOpeners)
			}

			if mode.mounts != "" {
				checkSluisMounts(t, docs)
			}
		})
	}
}

// checkSluisMounts holds the mount of the rotated credential to what lets the
// file follow the rotation: on every component that opens the archive, mounted
// as a directory (a subPath mount is a copy that never changes), and on no
// other.
func checkSluisMounts(t *testing.T, docs []map[string]any) {
	t.Helper()
	opener := map[string]bool{}
	for _, c := range archiveOpeners {
		opener[c] = true
	}
	mounted := map[string]bool{}
	for _, doc := range docs {
		pod, ok := dig(doc, "spec", "template")
		if !ok {
			pod, ok = dig(doc, "spec", "jobTemplate", "spec", "template")
		}
		if !ok {
			continue
		}
		component, _ := dig(pod, "metadata", "labels", "app.kubernetes.io/component")
		name, _ := dig(doc, "metadata", "name")
		volumes := map[string]bool{}
		vs, _ := dig(pod, "spec", "volumes")
		for _, v := range asList(vs) {
			if secret, ok := dig(v, "secret", "secretName"); ok && secret == "audit-r2" {
				n, _ := dig(v, "name")
				volumes[n.(string)] = true
			}
		}
		cs, _ := dig(pod, "spec", "containers")
		for _, c := range asList(cs) {
			ms, _ := dig(c, "volumeMounts")
			for _, m := range asList(ms) {
				n, _ := dig(m, "name")
				if !volumes[n.(string)] {
					continue
				}
				comp, _ := component.(string)
				if comp == "" {
					// A job's pods carry the release's labels only; the
					// CronJob is named for the job.
					comp = strings.TrimPrefix(name.(string), "audit-")
				}
				if comp == "consumer" {
					comp = "writer"
				}
				mounted[comp] = true
				if !opener[comp] {
					t.Errorf("%s (%s) mounts the archive credential and opens no archive", name, comp)
				}
				if _, sub := dig(m, "subPath"); sub {
					t.Errorf("%s mounts the archive credential with subPath: the file would never follow the rotation", name)
				}
				if p, _ := dig(m, "mountPath"); p != "/etc/audit/sluis/external/cloudflare" {
					t.Errorf("%s mounts the archive credential at %v, not the directory <sluisDir>/external/cloudflare", name, p)
				}
			}
		}
	}
	for _, c := range archiveOpeners {
		if !mounted[c] {
			t.Errorf("%s opens the archive and does not mount the credential", c)
		}
	}
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// credentials_preset is refused unless acknowledgeMinterCustody is true; the
// refusal names the preset and points at credentials_ref, and the same values
// render once acknowledged.
func TestCredentialsPresetIsRefusedWithoutAcknowledgeMinterCustody(t *testing.T) {
	var preset string
	for _, mode := range credentialModes {
		if mode.name == "credentials_preset" {
			preset = mode.preset
		}
	}
	values := strings.NewReplacer("PRESET", preset, "ARCHIVE", "{stateRoot: /audit/main}", "MOUNTS", "[]").Replace(everyComponent)
	_, stderr, err := helmOutput(t, values)
	if err == nil {
		t.Fatal("credentials_preset rendered without acknowledgeMinterCustody")
	}
	for _, want := range []string{"presets.standard", "credentials_preset", "credentials_ref", "acknowledgeMinterCustody"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not mention %q: %s", want, stderr)
		}
	}
	if _, stderr, err := helmOutput(t, "acknowledgeMinterCustody: true\n"+values); err != nil {
		t.Errorf("the acknowledged install was refused: %s", stderr)
	}
}
