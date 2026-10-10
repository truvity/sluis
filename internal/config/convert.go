package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	yaml "go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/internal/githubapp/catalogue"
	slackcatalogue "github.com/truvity/sluis/internal/slackapp/catalogue"
	"github.com/truvity/sluis/policy"
)

// legacyPolicy is what a v1 service document said that v2 keeps in the policy
// document: the files it named and the values it held. A converted document
// keeps it, and [PolicyOf] builds the policy document from it, so a v1
// deployment runs on this build unchanged until it renders its policy.
type legacyPolicy struct {
	policyDir           string
	clustersFile        string
	awsFile             string
	githubCatalogueFile string
	slackCatalogueFile  string
	owners              []string
	runnerTiers         []string
	enabledOrgs         []string
	enabledWorkspaces   []string

	// secrets is where each secret v1 named by a variable or a file is, by
	// the name v2 gives it; clientDir is v1's clientSecretsDir.
	secrets   map[string]SecretLocation
	clientDir string
}

// SecretLocation is where a converted v1 document said one secret was: a
// variable or a file.
type SecretLocation struct {
	Env  string
	File string
}

// The names v2 gives the secrets v1 named by a variable or a file.
const (
	secretValkeyPassword   = "valkey/password"
	secretRecoveryPassword = "recovery/password"
	secretStateSecret      = "issuer/state-secret"
	legacyProvider         = "default"
)

// convertV1 turns a v1 service document, already held to its v1 schema, into
// the v2 shape in place, and returns what moved to the policy document.
func convertV1(name string, doc map[string]any) (*legacyPolicy, error) {
	l := &legacyPolicy{}
	pop := func(m map[string]any, key string) any {
		v := m[key]
		delete(m, key)
		return v
	}
	str := func(v any) string { s, _ := v.(string); return s }
	strs := func(v any) []string {
		list, _ := v.([]any)
		out := make([]string, 0, len(list))
		for _, x := range list {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	l.policyDir = str(pop(doc, "policyDir"))
	l.secrets = map[string]SecretLocation{}
	switch name {
	case "serve":
		convertV1Secrets(doc, l)
		overlay := str(pop(doc, "overlayFile"))
		// The directory API listener is not served by sluis serve: its guard
		// configured nothing that runs.
		delete(doc, "api")
		if g, ok := pop(doc, "github").(map[string]any); ok {
			l.owners, l.runnerTiers = strs(g["owners"]), strs(g["runnerTiers"])
			l.githubCatalogueFile = str(g["catalogueFile"])
		}
		if s, ok := pop(doc, "slack").(map[string]any); ok {
			l.slackCatalogueFile = str(s["catalogueFile"])
		}
		if e, ok := pop(doc, "exports").([]any); ok && len(e) > 0 {
			return nil, fmt.Errorf("exports: %s", retiredExports)
		}
		if ports, ok := doc["ports"].(map[string]any); ok {
			if _, has := ports["export"]; has {
				return nil, fmt.Errorf("ports.export: %s", retiredExports)
			}
		}
		if x, ok := doc["exchange"].(map[string]any); ok {
			l.clustersFile, l.awsFile = str(pop(x, "clustersFile")), str(pop(x, "awsFile"))
			if len(x) == 0 {
				delete(doc, "exchange")
			}
		}
		workspaces, err := readOverlay(overlay)
		if err != nil {
			return nil, err
		}
		for i, w := range workspaces {
			ws, _ := w.(map[string]any)
			key := str(pop(ws, "keyFile"))
			seg := str(ws["id"])
			if seg == "" {
				seg = fmt.Sprintf("declared-%d", i)
			}
			name := "directory/" + seg + "/key"
			ws["keySecret"] = name
			l.secrets[name] = SecretLocation{File: key}
		}
		if len(workspaces) > 0 {
			doc["directory"] = map[string]any{"workspaces": workspaces}
		}
	case "controller-github":
		l.githubCatalogueFile = str(pop(doc, "catalogueFile"))
		l.enabledOrgs = strs(pop(doc, "enabledOrgs"))
	case "controller-slack":
		l.enabledWorkspaces = strs(pop(doc, "enabledWorkspaces"))
	}
	return l, nil
}

// convertV1Secrets moves every secret a v1 serve document named by a variable
// or a file to the name v2 gives it, and remembers where v1 said it was.
func convertV1Secrets(doc map[string]any, l *legacyPolicy) {
	str := func(v any) string { s, _ := v.(string); return s }
	if v, ok := doc["valkey"].(map[string]any); ok {
		if env := str(v["passwordEnv"]); env != "" {
			delete(v, "passwordEnv")
			v["passwordSecret"] = secretValkeyPassword
			l.secrets[secretValkeyPassword] = SecretLocation{Env: env}
		}
	}
	if o, ok := doc["oauthClient"].(map[string]any); ok {
		idFile, secretFile, secretEnv := str(o["idFile"]), str(o["secretFile"]), str(o["secretEnv"])
		delete(o, "idFile")
		delete(o, "secretFile")
		delete(o, "secretEnv")
		if idFile != "" || secretFile != "" || secretEnv != "" {
			o["provider"] = legacyProvider
		}
		if idFile != "" {
			l.secrets["providers/google/"+legacyProvider+"/client-id"] = SecretLocation{File: idFile}
		}
		switch {
		case secretFile != "":
			l.secrets["providers/google/"+legacyProvider+"/client-secret"] = SecretLocation{File: secretFile}
		case secretEnv != "":
			l.secrets["providers/google/"+legacyProvider+"/client-secret"] = SecretLocation{Env: secretEnv}
		}
	}
	adminEnv := str(doc["adminPasswordEnv"])
	delete(doc, "adminPasswordEnv")
	r, _ := doc["recovery"].(map[string]any)
	passwordFile := ""
	if r != nil {
		passwordFile = str(r["passwordFile"])
		delete(r, "passwordFile")
	}
	if passwordFile != "" || adminEnv != "" {
		if r == nil {
			r = map[string]any{}
			doc["recovery"] = r
		}
		r["passwordSecret"] = secretRecoveryPassword
		if passwordFile != "" {
			l.secrets[secretRecoveryPassword] = SecretLocation{File: passwordFile}
		} else {
			l.secrets[secretRecoveryPassword] = SecretLocation{Env: adminEnv}
		}
	}
	if k, ok := doc["signingKey"].(map[string]any); ok {
		for _, key := range []string{"kms", "kmsWrapped"} {
			if b, ok := k[key].(map[string]any); ok {
				if file := str(b["stateSecretFile"]); file != "" {
					delete(b, "stateSecretFile")
					b["stateSecret"] = secretStateSecret
					l.secrets[secretStateSecret] = SecretLocation{File: file}
				}
			}
		}
	}
	if a, ok := doc["adapters"].(map[string]any); ok {
		if signing, ok := a["signing"].(map[string]any); ok {
			if settings, ok := signing["settings"].(map[string]any); ok {
				if file := str(settings["stateSecretFile"]); file != "" {
					delete(settings, "stateSecretFile")
					settings["stateSecret"] = secretStateSecret
					l.secrets[secretStateSecret] = SecretLocation{File: file}
				}
			}
		}
	}
	if dir := str(doc["clientSecretsDir"]); dir != "" {
		delete(doc, "clientSecretsDir")
		l.clientDir = dir
	}
}

// readOverlay reads a v1 overlay file's workspaces in the v2 `directory`
// shape. A path that does not exist declares nothing, as in v1.
func readOverlay(file string) ([]any, error) {
	if file == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(file) //nolint:gosec // the path is the deployment's own configuration
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the workspace overlay: %w", err)
	}
	var overlay struct {
		Workspaces []map[string]any `yaml:"workspaces"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err = dec.Decode(&overlay); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse the workspace overlay: %w", err)
	}
	out := make([]any, 0, len(overlay.Workspaces))
	for _, ws := range overlay.Workspaces {
		raw, _ := json.Marshal(ws)
		var normalised any
		_ = json.Unmarshal(raw, &normalised)
		out = append(out, normalised)
	}
	return out, nil
}

// document builds the policy document a v1 deployment's files make: the policy
// directory (or tables to fall back on), and beside it every section read from
// the file or the key v1 kept it in.
func (l *legacyPolicy) document(fallback *policy.Policy) (*PolicyDocument, error) {
	var d *PolicyDocument
	switch {
	case l.policyDir != "":
		tables, err := policy.LoadDeclared(l.policyDir)
		if err != nil {
			return nil, err
		}
		d = NewPolicyDocument(tables)
	case fallback != nil:
		d = NewPolicyDocument(*fallback)
	default:
		d = NewPolicyDocument(policy.Policy{})
	}
	var x PolicyExchange
	if l.clustersFile != "" {
		var f struct {
			Clusters []FederatedCluster `yaml:"clusters"`
		}
		if err := readYAML(l.clustersFile, &f, false); err != nil {
			return nil, fmt.Errorf("exchange.clustersFile: %w", err)
		}
		x.Clusters = f.Clusters
	}
	if l.awsFile != "" {
		var f AWSFederation
		if err := readYAML(l.awsFile, &f, true); err != nil {
			return nil, fmt.Errorf("exchange.awsFile: %w", err)
		}
		x.AWS = &f
	}
	if len(l.owners) > 0 {
		x.GitHub = &ExchangeGitHub{Owners: l.owners}
	}
	if x.Clusters != nil || x.AWS != nil || x.GitHub != nil {
		d.Exchange = &x
	}
	var apps PolicyApps
	if l.githubCatalogueFile != "" || len(l.runnerTiers) > 0 {
		c, err := catalogue.Load(l.githubCatalogueFile)
		if err != nil {
			return nil, fmt.Errorf("github.catalogueFile: %w", err)
		}
		apps.GitHub = &AppsGitHub{RunnerTiers: l.runnerTiers, Catalogue: c.Apps}
	}
	if l.slackCatalogueFile != "" {
		c, err := slackcatalogue.Load(l.slackCatalogueFile)
		if err != nil {
			return nil, fmt.Errorf("slack.catalogueFile: %w", err)
		}
		apps.Slack = &AppsSlack{Catalogue: c.Apps}
	}
	if apps.GitHub != nil || apps.Slack != nil {
		apps.FoldLegacy()
		d.Apps = &apps
	}
	var controllers PolicyControllers
	if len(l.enabledOrgs) > 0 {
		controllers.GitHub = &ControllersGitHub{EnabledOrgs: l.enabledOrgs}
	}
	if len(l.enabledWorkspaces) > 0 {
		controllers.Slack = &ControllersSlack{EnabledWorkspaces: l.enabledWorkspaces}
	}
	if controllers.GitHub != nil || controllers.Slack != nil {
		d.Controllers = &controllers
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return d, nil
}

func readYAML(file string, into any, strict bool) error {
	raw, err := os.ReadFile(file) //nolint:gosec // the path is the deployment's own configuration
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(strict)
	if err = dec.Decode(into); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("parse %s: %w", file, err)
	}
	return nil
}
