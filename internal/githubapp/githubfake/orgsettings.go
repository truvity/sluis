package githubfake

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/crypto/nacl/box"
)

// The organisation's Actions variables, Actions secrets and code security
// configurations (ADR 0080), as GitHub's REST API serves them. A write is
// logged in [Org.Actions] as "create-variable NAME", "update-variable NAME",
// "delete-variable NAME", "put-secret NAME", "delete-secret NAME",
// "create-config NAME", "update-config NAME", "delete-config NAME",
// "attach-config NAME scope", "detach-config ID,ID" and "set-default NAME value".

// Variable is an Actions variable.
type Variable struct {
	Value      string
	Visibility string
	// Selected are the repository ids it is visible to, when selected.
	Selected []int64
}

// Secret is an Actions secret as GitHub keeps it: sealed, never readable.
type Secret struct {
	Visibility string
	Selected   []int64
	// KeyID is the key the value was sealed with, and Sealed the base64
	// ciphertext as written. [Org.OpenSecret] opens it.
	KeyID  string
	Sealed string
	// Writes counts how many times the secret was written.
	Writes int
}

// SecurityConfiguration is a code security configuration.
type SecurityConfiguration struct {
	ID          int64
	Name        string
	TargetType  string
	Description string
	Settings    map[string]string
	// Attached are the repositories it is attached to, by id, with their status.
	Attached map[int64]string
	// DefaultFor are the kinds of repository, `public` and
	// `private_and_internal`, that a repository created from now on gets this
	// configuration for; empty is nobody's default.
	DefaultFor map[string]bool
}

// Default is the configuration's default for new repositories in the words of
// the API: `all`, `public`, `private_and_internal` or `none`.
func (c *SecurityConfiguration) Default() string {
	switch {
	case c.DefaultFor["public"] && c.DefaultFor["private_and_internal"]:
		return "all"
	case c.DefaultFor["public"]:
		return "public"
	case c.DefaultFor["private_and_internal"]:
		return "private_and_internal"
	}

	return "none"
}

// defaultKinds are the kinds of repository a value of default_for_new_repos
// covers.
func defaultKinds(value string) []string {
	switch value {
	case "all":
		return []string{"public", "private_and_internal"}
	case "public", "private_and_internal":
		return []string{value}
	}

	return nil
}

// Repository is a repository of the organisation.
type Repository struct {
	ID       int64
	Name     string
	Archived bool
}

type settingsState struct {
	Repositories []Repository
	Variables    map[string]*Variable
	Secrets      map[string]*Secret
	Configs      map[int64]*SecurityConfiguration

	secretsKeyID string
	secretsPub   *[32]byte
	secretsPriv  *[32]byte
}

func (o *Org) initSettings() {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}

	o.settings = settingsState{
		Variables: map[string]*Variable{}, Secrets: map[string]*Secret{}, Configs: map[int64]*SecurityConfiguration{},
		secretsKeyID: "568250167242549743", secretsPub: pub, secretsPriv: priv,
	}
}

// AddRepository adds a repository to the organisation and returns its id.
func (o *Org) AddRepository(name string) int64 {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.nextID++
	o.settings.Repositories = append(o.settings.Repositories, Repository{ID: o.nextID, Name: name})

	return o.nextID
}

// Archive archives a repository.
func (o *Org) Archive(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	for i := range o.settings.Repositories {
		if o.settings.Repositories[i].Name == name {
			o.settings.Repositories[i].Archived = true
		}
	}
}

// RepositoryID is a repository's id, or 0.
func (o *Org) RepositoryID(name string) int64 {
	o.mu.Lock()
	defer o.mu.Unlock()

	for _, r := range o.settings.Repositories {
		if r.Name == name {
			return r.ID
		}
	}

	return 0
}

// SetVariable puts a variable in the organisation, as another writer would.
func (o *Org) SetVariable(name string, v Variable) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.settings.Variables[strings.ToUpper(name)] = &v
}

// RemoveVariable removes a variable, as an operator would on GitHub.
func (o *Org) RemoveVariable(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	delete(o.settings.Variables, strings.ToUpper(name))
}

// Variable is a variable, or nil.
func (o *Org) Variable(name string) *Variable {
	o.mu.Lock()
	defer o.mu.Unlock()

	if v := o.settings.Variables[strings.ToUpper(name)]; v != nil {
		c := *v

		return &c
	}

	return nil
}

// SetSecret puts a secret in the organisation, as another writer would.
func (o *Org) SetSecret(name, visibility string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.settings.Secrets[strings.ToUpper(name)] = &Secret{Visibility: visibility}
}

// Secret is a secret, or nil.
func (o *Org) Secret(name string) *Secret {
	o.mu.Lock()
	defer o.mu.Unlock()

	if s := o.settings.Secrets[strings.ToUpper(name)]; s != nil {
		c := *s

		return &c
	}

	return nil
}

// OpenSecret decrypts what was written for a secret with the organisation's
// private key, as only GitHub could.
func (o *Org) OpenSecret(name string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	s := o.settings.Secrets[strings.ToUpper(name)]
	if s == nil || s.Sealed == "" {
		return "", errors.New("no such secret, or never written")
	}

	raw, err := base64.StdEncoding.DecodeString(s.Sealed)
	if err != nil {
		return "", err //nolint:wrapcheck // a test double
	}

	plain, ok := box.OpenAnonymous(nil, raw, o.settings.secretsPub, o.settings.secretsPriv)
	if !ok {
		return "", errors.New("the value was not sealed to the organisation's key")
	}

	return string(plain), nil
}

// AddConfiguration puts an organisation configuration in place, as another
// writer would.
func (o *Org) AddConfiguration(name string, settings map[string]string) int64 {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.nextID++
	o.settings.Configs[o.nextID] = &SecurityConfiguration{
		ID: o.nextID, Name: name, TargetType: "organization", Description: "made elsewhere",
		Settings: settings, Attached: map[int64]string{},
	}

	return o.nextID
}

// AddGlobalConfiguration puts a configuration GitHub provides in place; the
// controller must neither list it as the organisation's nor touch it.
func (o *Org) AddGlobalConfiguration(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.nextID++
	o.settings.Configs[o.nextID] = &SecurityConfiguration{
		ID: o.nextID, Name: name, TargetType: "global", Description: "GitHub's", Settings: map[string]string{}, Attached: map[int64]string{},
	}
}

// Configuration is a configuration by name, or nil.
func (o *Org) Configuration(name string) *SecurityConfiguration {
	o.mu.Lock()
	defer o.mu.Unlock()

	for _, c := range o.settings.Configs {
		if c.Name == name && c.TargetType == "organization" {
			return cloneConfig(c)
		}
	}

	return nil
}

func cloneConfig(c *SecurityConfiguration) *SecurityConfiguration {
	out := *c
	out.Attached = map[int64]string{}
	out.DefaultFor = maps.Clone(c.DefaultFor)

	for k, v := range c.Attached {
		out.Attached[k] = v
	}

	return &out
}

// SetDefault makes a configuration the default for new repositories, as another
// writer would: `all`, `public`, `private_and_internal` or `none`.
func (o *Org) SetDefault(config, value string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	for _, c := range o.settings.Configs {
		if c.Name == config {
			o.makeDefault(c, value)
		}
	}
}

// makeDefault takes the kinds of repository value covers from whichever
// configuration held them, and gives them to c; none gives c up.
func (o *Org) makeDefault(c *SecurityConfiguration, value string) {
	kinds := defaultKinds(value)

	for _, other := range o.settings.Configs {
		for _, kind := range kinds {
			delete(other.DefaultFor, kind)
		}
	}

	c.DefaultFor = map[string]bool{}
	for _, kind := range kinds {
		c.DefaultFor[kind] = true
	}
}

// Attach attaches a repository to a configuration directly, as another
// writer would.
func (o *Org) Attach(config string, repository int64) {
	o.mu.Lock()
	defer o.mu.Unlock()

	for _, c := range o.settings.Configs {
		if c.Name == config {
			c.Attached[repository] = "attached"
		}
	}
}

// secretsRoute is the collection of the organisation's Actions secrets. The
// routes name it as a base and a rest because the repository's leak canary
// reads a literal secrets path as a parameter path.
const secretsRoute = "/orgs/{org}/actions/secrets"

func (o *Org) settingsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /orgs/{org}/repos", o.listRepos)
	mux.HandleFunc("GET /orgs/{org}/actions/variables", o.listVariables)
	mux.HandleFunc("POST /orgs/{org}/actions/variables", o.createVariable)
	mux.HandleFunc("PATCH /orgs/{org}/actions/variables/{name}", o.updateVariable)
	mux.HandleFunc("DELETE /orgs/{org}/actions/variables/{name}", o.deleteVariable)
	mux.HandleFunc("GET /orgs/{org}/actions/variables/{name}/repositories", o.variableRepos)
	mux.HandleFunc("GET "+secretsRoute, o.listSecrets)
	mux.HandleFunc("GET "+secretsRoute+"/public-key", o.secretsKey)
	mux.HandleFunc("PUT "+secretsRoute+"/{name}", o.putSecret)
	mux.HandleFunc("DELETE "+secretsRoute+"/{name}", o.deleteSecret)
	mux.HandleFunc("GET "+secretsRoute+"/{name}/repositories", o.secretRepos)
	mux.HandleFunc("GET /orgs/{org}/code-security/configurations", o.listConfigs)
	mux.HandleFunc("GET /orgs/{org}/code-security/configurations/defaults", o.listDefaults)
	mux.HandleFunc("PUT /orgs/{org}/code-security/configurations/{id}/defaults", o.putDefault)
	mux.HandleFunc("POST /orgs/{org}/code-security/configurations", o.createConfig)
	mux.HandleFunc("DELETE /orgs/{org}/code-security/configurations/detach", o.detachConfigs)
	mux.HandleFunc("PATCH /orgs/{org}/code-security/configurations/{id}", o.updateConfig)
	mux.HandleFunc("DELETE /orgs/{org}/code-security/configurations/{id}", o.deleteConfig)
	mux.HandleFunc("POST /orgs/{org}/code-security/configurations/{id}/attach", o.attachConfig)
	mux.HandleFunc("GET /orgs/{org}/code-security/configurations/{id}/repositories", o.configRepos)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func notFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
}

func decodeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(raw, into); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Problems parsing JSON"})

		return false
	}

	return true
}

func (o *Org) repoRows(ids []int64) []map[string]any {
	rows := []map[string]any{}

	for _, id := range ids {
		for _, r := range o.settings.Repositories {
			if r.ID == id {
				rows = append(rows, map[string]any{"id": r.ID, "name": r.Name})
			}
		}
	}

	return rows
}

func (o *Org) listRepos(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	rows := []map[string]any{}
	for _, repo := range o.settings.Repositories {
		rows = append(rows, map[string]any{"id": repo.ID, "name": repo.Name, "archived": repo.Archived})
	}

	writeJSON(w, http.StatusOK, rows)
}

func (o *Org) listVariables(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	rows := []map[string]any{}
	for _, name := range sortedKeys(o.settings.Variables) {
		v := o.settings.Variables[name]
		rows = append(rows, map[string]any{"name": name, "value": v.Value, "visibility": v.Visibility})
	}

	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(rows), "variables": rows})
}

type variableBody struct {
	Name       string  `json:"name"`
	Value      *string `json:"value"`
	Visibility string  `json:"visibility"`
	Selected   []int64 `json:"selected_repository_ids"`
}

func (o *Org) createVariable(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	var body variableBody
	if !decodeBody(w, r, &body) {
		return
	}

	body.Name = strings.ToUpper(body.Name)

	if _, exists := o.settings.Variables[body.Name]; exists {
		writeJSON(w, http.StatusConflict, map[string]string{"message": "Variable already exists"})

		return
	}

	if !o.act(w, "create-variable "+body.Name) {
		return
	}

	v := &Variable{Visibility: body.Visibility, Selected: body.Selected}
	if body.Value != nil {
		v.Value = *body.Value
	}

	o.settings.Variables[body.Name] = v

	writeJSON(w, http.StatusCreated, map[string]string{"message": "Created"})
}

func (o *Org) updateVariable(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	name := strings.ToUpper(r.PathValue("name"))

	v, exists := o.settings.Variables[name]
	if !exists {
		notFound(w)

		return
	}

	var body variableBody
	if !decodeBody(w, r, &body) {
		return
	}

	if !o.act(w, "update-variable "+name) {
		return
	}

	if body.Value != nil {
		v.Value = *body.Value
	}

	if body.Visibility != "" {
		v.Visibility = body.Visibility
	}

	v.Selected = body.Selected
	w.WriteHeader(http.StatusNoContent)
}

func (o *Org) deleteVariable(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	name := strings.ToUpper(r.PathValue("name"))
	if _, exists := o.settings.Variables[name]; !exists {
		notFound(w)

		return
	}

	if !o.act(w, "delete-variable "+name) {
		return
	}

	delete(o.settings.Variables, name)
	w.WriteHeader(http.StatusNoContent)
}

func (o *Org) variableRepos(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	v, exists := o.settings.Variables[strings.ToUpper(r.PathValue("name"))]
	if !exists {
		notFound(w)

		return
	}

	rows := o.repoRows(v.Selected)
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(rows), "repositories": rows})
}

func (o *Org) listSecrets(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	rows := []map[string]any{}
	for _, name := range sortedKeys(o.settings.Secrets) {
		rows = append(rows, map[string]any{"name": name, "visibility": o.settings.Secrets[name].Visibility})
	}

	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(rows), "secrets": rows})
}

func (o *Org) secretsKey(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"key_id": o.settings.secretsKeyID, "key": base64.StdEncoding.EncodeToString(o.settings.secretsPub[:]),
	})
}

func (o *Org) putSecret(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	name := strings.ToUpper(r.PathValue("name"))

	var body struct {
		Encrypted  string  `json:"encrypted_value"`
		KeyID      string  `json:"key_id"`
		Visibility string  `json:"visibility"`
		Selected   []int64 `json:"selected_repository_ids"`
	}
	if !decodeBody(w, r, &body) {
		return
	}

	if body.KeyID != o.settings.secretsKeyID {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "key_id does not match the organisation's key"})

		return
	}

	if !o.act(w, "put-secret "+name) {
		return
	}

	existing, exists := o.settings.Secrets[name]
	if !exists {
		existing = &Secret{}
		o.settings.Secrets[name] = existing
	}

	existing.Visibility, existing.Selected, existing.KeyID, existing.Sealed = body.Visibility, body.Selected, body.KeyID, body.Encrypted
	existing.Writes++

	if exists {
		w.WriteHeader(http.StatusNoContent)

		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"message": "Created"})
}

func (o *Org) deleteSecret(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	name := strings.ToUpper(r.PathValue("name"))
	if _, exists := o.settings.Secrets[name]; !exists {
		notFound(w)

		return
	}

	if !o.act(w, "delete-secret "+name) {
		return
	}

	delete(o.settings.Secrets, name)
	w.WriteHeader(http.StatusNoContent)
}

func (o *Org) secretRepos(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	s, exists := o.settings.Secrets[strings.ToUpper(r.PathValue("name"))]
	if !exists {
		notFound(w)

		return
	}

	rows := o.repoRows(s.Selected)
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(rows), "repositories": rows})
}

func (c *SecurityConfiguration) view() map[string]any {
	row := map[string]any{"id": c.ID, "name": c.Name, "target_type": c.TargetType, "description": c.Description}
	for k, v := range c.Settings {
		row[k] = v
	}

	return row
}

func (o *Org) listConfigs(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	rows := []map[string]any{}

	ids := make([]int64, 0, len(o.settings.Configs))
	for id := range o.settings.Configs {
		ids = append(ids, id)
	}

	slices.Sort(ids)

	for _, id := range ids {
		rows = append(rows, o.settings.Configs[id].view())
	}

	writeJSON(w, http.StatusOK, rows)
}

func (o *Org) configBody(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	var body map[string]any

	return body, decodeBody(w, r, &body)
}

func (o *Org) createConfig(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	body, ok := o.configBody(w, r)
	if !ok {
		return
	}

	name, _ := body["name"].(string)
	for _, c := range o.settings.Configs {
		if c.Name == name && c.TargetType == "organization" {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Name has already been taken"})

			return
		}
	}

	if !o.act(w, "create-config "+name) {
		return
	}

	o.nextID++
	c := &SecurityConfiguration{ID: o.nextID, Name: name, TargetType: "organization", Settings: map[string]string{}, Attached: map[int64]string{}}
	applyConfigBody(c, body)
	o.settings.Configs[c.ID] = c

	writeJSON(w, http.StatusCreated, c.view())
}

func applyConfigBody(c *SecurityConfiguration, body map[string]any) {
	for key, value := range body {
		switch text, _ := value.(string); key {
		case "name":
			c.Name = text
		case "description":
			c.Description = text
		default:
			c.Settings[key] = text
		}
	}
}

func (o *Org) configByID(w http.ResponseWriter, r *http.Request) *SecurityConfiguration {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)

	c := o.settings.Configs[id]
	if c == nil {
		notFound(w)
	}

	return c
}

func (o *Org) updateConfig(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	c := o.configByID(w, r)
	if c == nil {
		return
	}

	body, ok := o.configBody(w, r)
	if !ok || !o.act(w, "update-config "+c.Name) {
		return
	}

	applyConfigBody(c, body)
	writeJSON(w, http.StatusOK, c.view())
}

func (o *Org) deleteConfig(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	c := o.configByID(w, r)
	if c == nil || !o.act(w, "delete-config "+c.Name) {
		return
	}

	delete(o.settings.Configs, c.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (o *Org) attachConfig(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	c := o.configByID(w, r)
	if c == nil {
		return
	}

	var body struct {
		Scope    string  `json:"scope"`
		Selected []int64 `json:"selected_repository_ids"`
	}
	if !decodeBody(w, r, &body) || !o.act(w, "attach-config "+c.Name+" "+body.Scope) {
		return
	}

	targets := body.Selected

	if body.Scope == "all" {
		// As GitHub does: every repository, archived ones too. A caller that
		// wants the live ones alone names them as a selected set.
		targets = nil

		for _, repo := range o.settings.Repositories {
			targets = append(targets, repo.ID)
		}
	}

	for _, id := range targets {
		// A repository holds one configuration: attaching takes it from
		// whichever held it.
		for _, other := range o.settings.Configs {
			delete(other.Attached, id)
		}

		c.Attached[id] = "attached"
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"message": "Accepted"})
}

func (o *Org) listDefaults(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	rows := []map[string]any{}

	for _, id := range slices.Sorted(maps.Keys(o.settings.Configs)) {
		c := o.settings.Configs[id]
		if c.Default() != "none" {
			rows = append(rows, map[string]any{"default_for_new_repos": c.Default(), "configuration": c.view()})
		}
	}

	writeJSON(w, http.StatusOK, rows)
}

func (o *Org) putDefault(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	c := o.configByID(w, r)
	if c == nil {
		return
	}

	var body struct {
		Default string `json:"default_for_new_repos"`
	}

	if !decodeBody(w, r, &body) {
		return
	}

	if !slices.Contains([]string{"all", "public", "private_and_internal", "none"}, body.Default) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Invalid default_for_new_repos"})

		return
	}

	if !o.act(w, "set-default "+c.Name+" "+body.Default) {
		return
	}

	o.makeDefault(c, body.Default)
	writeJSON(w, http.StatusOK, map[string]any{"default_for_new_repos": body.Default, "configuration": c.view()})
}

func (o *Org) detachConfigs(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	var body struct {
		Selected []int64 `json:"selected_repository_ids"`
	}
	if !decodeBody(w, r, &body) {
		return
	}

	ids := make([]string, 0, len(body.Selected))
	for _, id := range body.Selected {
		ids = append(ids, strconv.FormatInt(id, 10))
	}

	if !o.act(w, fmt.Sprintf("detach-config %s", joinComma(ids))) {
		return
	}

	for _, id := range body.Selected {
		for _, c := range o.settings.Configs {
			delete(c.Attached, id)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func joinComma(parts []string) string {
	out := ""

	for i, p := range parts {
		if i > 0 {
			out += ","
		}

		out += p
	}

	return out
}

func (o *Org) configRepos(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.authorised(w, r) {
		return
	}

	c := o.configByID(w, r)
	if c == nil {
		return
	}

	rows := []map[string]any{}

	for _, repo := range o.settings.Repositories {
		if state, attached := c.Attached[repo.ID]; attached {
			rows = append(rows, map[string]any{"status": state, "repository": map[string]any{"id": repo.ID, "name": repo.Name}})
		}
	}

	writeJSON(w, http.StatusOK, rows)
}
