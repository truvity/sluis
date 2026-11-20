package githubapp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"golang.org/x/crypto/nacl/box"
)

// The organisation settings the GitHub controller reconciles beside
// membership: Actions variables and secrets, and code security
// configurations (ADR 0080). Every call takes the installation token, as the
// membership calls do.

// Repo is a repository of the organisation, by the id the settings APIs take.
type Repo struct {
	ID   int64
	Name string
	// Archived is set for a repository GitHub has archived; only
	// [Org.Repositories] says it.
	Archived bool
}

// Variable is one organisation Actions variable.
type Variable struct {
	Name       string
	Value      string
	Visibility string
}

// Secret is one organisation Actions secret. GitHub never returns the value.
type Secret struct {
	Name       string
	Visibility string
}

// SecurityConfiguration is one code security configuration of the
// organisation.
type SecurityConfiguration struct {
	ID          int64
	Name        string
	Description string
	// Settings are the string-valued settings GitHub returned, by name.
	Settings map[string]string
}

// ConfigurationRepo is a repository a configuration is attached to, and how
// far the attachment is.
type ConfigurationRepo struct {
	Repo
	// Status is GitHub's own word: `attached`, `attaching`, `updating`,
	// `removed`, `failed` ...
	Status string
}

// Repositories reads every repository of the organisation.
func (o Org) Repositories(ctx context.Context, token string) ([]Repo, error) {
	var out []Repo
	err := pages(ctx, o.HTTP, o.url("/repos?per_page=100&type=all"), token, func(page *[]struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Archived bool   `json:"archived"`
	}) {
		for _, repo := range *page {
			out = append(out, Repo{ID: repo.ID, Name: repo.Name, Archived: repo.Archived})
		}
	})
	if err != nil {
		return nil, fmt.Errorf("github: read %s's repositories: %w", o.Login, err)
	}
	return out, nil
}

func (o Org) url(suffix string) string {
	return APIBase + "/orgs/" + url.PathEscape(o.Login) + suffix
}

// Variables reads every Actions variable of the organisation, with its value.
func (o Org) Variables(ctx context.Context, token string) ([]Variable, error) {
	var out []Variable
	err := pages(ctx, o.HTTP, o.url("/actions/variables?per_page=30"), token, func(page *struct {
		Variables []struct {
			Name       string `json:"name"`
			Value      string `json:"value"`
			Visibility string `json:"visibility"`
		} `json:"variables"`
	}) {
		for _, v := range page.Variables {
			out = append(out, Variable{Name: v.Name, Value: v.Value, Visibility: v.Visibility})
		}
	})
	if err != nil {
		return nil, fmt.Errorf("github: read %s's variables: %w", o.Login, err)
	}
	return out, nil
}

// VariableRepositories reads the repositories a `selected` variable is
// visible to.
func (o Org) VariableRepositories(ctx context.Context, token, name string) ([]Repo, error) {
	return o.selectedRepositories(ctx, token, "/actions/variables", "/"+url.PathEscape(name)+"/repositories?per_page=100")
}

// SecretRepositories reads the repositories a `selected` secret is visible to.
func (o Org) SecretRepositories(ctx context.Context, token, name string) ([]Repo, error) {
	return o.selectedRepositories(ctx, token, "/actions/secrets", "/"+url.PathEscape(name)+"/repositories?per_page=100")
}

func (o Org) selectedRepositories(ctx context.Context, token, collection, rest string) ([]Repo, error) {
	var out []Repo
	err := pages(ctx, o.HTTP, o.url(collection+rest), token, func(page *struct {
		Repositories []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"repositories"`
	}) {
		for _, repo := range page.Repositories {
			out = append(out, Repo{ID: repo.ID, Name: repo.Name})
		}
	})
	if err != nil {
		return nil, fmt.Errorf("github: read %s's selected repositories: %w", o.Login, err)
	}
	return out, nil
}

// CreateVariable creates an organisation variable.
func (o Org) CreateVariable(ctx context.Context, token string, v Variable, repositories []int64) error {
	body := map[string]any{"name": v.Name, "value": v.Value, "visibility": v.Visibility}
	if v.Visibility == "selected" {
		body["selected_repository_ids"] = repositories
	}
	if err := send(ctx, o.HTTP, http.MethodPost, o.url("/actions/variables"), token, body, http.StatusCreated, nil); err != nil {
		return fmt.Errorf("github: create %s's variable %s: %w", o.Login, v.Name, err)
	}
	return nil
}

// UpdateVariable changes an organisation variable's value, visibility and
// selection.
func (o Org) UpdateVariable(ctx context.Context, token string, v Variable, repositories []int64) error {
	body := map[string]any{"value": v.Value, "visibility": v.Visibility}
	if v.Visibility == "selected" {
		body["selected_repository_ids"] = repositories
	}
	if err := send(ctx, o.HTTP, http.MethodPatch, o.url("/actions/variables/"+url.PathEscape(v.Name)), token, body, http.StatusNoContent, nil); err != nil {
		return fmt.Errorf("github: update %s's variable %s: %w", o.Login, v.Name, err)
	}
	return nil
}

// DeleteVariable deletes an organisation variable.
func (o Org) DeleteVariable(ctx context.Context, token, name string) error {
	if err := call(ctx, o.HTTP, http.MethodDelete, o.url("/actions/variables/"+url.PathEscape(name)), token, http.StatusNoContent, nil); err != nil {
		return fmt.Errorf("github: delete %s's variable %s: %w", o.Login, name, err)
	}
	return nil
}

// Secrets reads the organisation's Actions secrets: their names and
// visibility, never a value.
func (o Org) Secrets(ctx context.Context, token string) ([]Secret, error) {
	var out []Secret
	err := pages(ctx, o.HTTP, o.url("/actions/secrets?per_page=100"), token, func(page *struct {
		Secrets []struct {
			Name       string `json:"name"`
			Visibility string `json:"visibility"`
		} `json:"secrets"`
	}) {
		for _, s := range page.Secrets {
			out = append(out, Secret{Name: s.Name, Visibility: s.Visibility})
		}
	})
	if err != nil {
		return nil, fmt.Errorf("github: read %s's secrets: %w", o.Login, err)
	}
	return out, nil
}

// SecretsKey is the organisation's public key for Actions secrets.
type SecretsKey struct {
	ID  string
	Key [32]byte
}

// SecretsPublicKey reads the key a secret is sealed with.
func (o Org) SecretsPublicKey(ctx context.Context, token string) (SecretsKey, error) {
	var body struct {
		KeyID string `json:"key_id"`
		Key   string `json:"key"`
	}
	if err := call(ctx, o.HTTP, http.MethodGet, o.url("/actions/secrets")+"/public-key", token, http.StatusOK, &body); err != nil {
		return SecretsKey{}, fmt.Errorf("github: read %s's secrets key: %w", o.Login, err)
	}
	raw, err := base64.StdEncoding.DecodeString(body.Key)
	if err != nil || len(raw) != len(SecretsKey{}.Key) || body.KeyID == "" {
		return SecretsKey{}, fmt.Errorf("github: %s's secrets key is not a 32-byte key with an id", o.Login)
	}
	key := SecretsKey{ID: body.KeyID}
	copy(key.Key[:], raw)
	return key, nil
}

// Seal encrypts a secret's value for GitHub: a libsodium sealed box
// (crypto_box_seal) to the organisation's key, base64-encoded. x/crypto's
// box.SealAnonymous is the same construction.
func (k SecretsKey) Seal(value []byte) (string, error) {
	sealed, err := box.SealAnonymous(nil, value, &k.Key, rand.Reader)
	if err != nil {
		return "", errors.New("github: seal the secret: " + err.Error())
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// PutSecret creates or replaces an organisation secret from a value already
// sealed with [SecretsKey.Seal].
func (o Org) PutSecret(ctx context.Context, token, name, sealed string, key SecretsKey, visibility string, repositories []int64) error {
	body := map[string]any{"encrypted_value": sealed, "key_id": key.ID, "visibility": visibility}
	if visibility == "selected" {
		body["selected_repository_ids"] = repositories
	}
	response, err := do(ctx, o.HTTP, http.MethodPut, o.url("/actions/secrets")+"/"+url.PathEscape(name), token, body)
	if err != nil {
		return fmt.Errorf("github: write %s's secret %s: %w", o.Login, name, err)
	}
	defer response.Body.Close() //nolint:errcheck // a read body's close has nothing to report
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("github: write %s's secret %s: %w", o.Login, name, statusError(response))
	}
	return nil
}

// DeleteSecret deletes an organisation secret.
func (o Org) DeleteSecret(ctx context.Context, token, name string) error {
	if err := call(ctx, o.HTTP, http.MethodDelete, o.url("/actions/secrets")+"/"+url.PathEscape(name), token, http.StatusNoContent, nil); err != nil {
		return fmt.Errorf("github: delete %s's secret %s: %w", o.Login, name, err)
	}
	return nil
}

// SecurityConfigurations reads the organisation's own code security
// configurations, leaving out the ones GitHub provides.
func (o Org) SecurityConfigurations(ctx context.Context, token string) ([]SecurityConfiguration, error) {
	var out []SecurityConfiguration
	err := pages(ctx, o.HTTP, o.url("/code-security/configurations?per_page=100"), token, func(page *[]map[string]any) {
		for _, raw := range *page {
			if target, _ := raw["target_type"].(string); target != "organization" {
				continue
			}
			id, _ := raw["id"].(float64)
			cfg := SecurityConfiguration{ID: int64(id), Settings: map[string]string{}}
			cfg.Name, _ = raw["name"].(string)
			cfg.Description, _ = raw["description"].(string)
			for key, value := range raw {
				if text, ok := value.(string); ok {
					cfg.Settings[key] = text
				}
			}
			out = append(out, cfg)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("github: read %s's code security configurations: %w", o.Login, err)
	}
	return out, nil
}

// configurationBody is a configuration as the create and update calls take
// it: its name and description beside its settings.
func configurationBody(name, description string, settings map[string]string) map[string]any {
	body := map[string]any{"name": name, "description": description}
	for key, value := range settings {
		body[key] = value
	}
	return body
}

// CreateSecurityConfiguration creates a configuration and returns its id.
func (o Org) CreateSecurityConfiguration(ctx context.Context, token, name, description string, settings map[string]string) (int64, error) {
	var created struct {
		ID int64 `json:"id"`
	}
	err := send(ctx, o.HTTP, http.MethodPost, o.url("/code-security/configurations"), token,
		configurationBody(name, description, settings), http.StatusCreated, &created)
	if err != nil {
		return 0, fmt.Errorf("github: create %s's code security configuration %s: %w", o.Login, name, err)
	}
	return created.ID, nil
}

// UpdateSecurityConfiguration changes a configuration's description and
// settings. The name is the configuration's key and stays.
func (o Org) UpdateSecurityConfiguration(ctx context.Context, token string, id int64, name, description string, settings map[string]string) error {
	response, err := do(ctx, o.HTTP, http.MethodPatch, o.url("/code-security/configurations/"+strconv.FormatInt(id, 10)), token,
		configurationBody(name, description, settings))
	if err != nil {
		return fmt.Errorf("github: update %s's code security configuration %s: %w", o.Login, name, err)
	}
	defer response.Body.Close() //nolint:errcheck // a read body's close has nothing to report
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("github: update %s's code security configuration %s: %w", o.Login, name, statusError(response))
	}
	return nil
}

// DeleteSecurityConfiguration deletes a configuration. Repositories it was
// attached to are detached by GitHub.
func (o Org) DeleteSecurityConfiguration(ctx context.Context, token string, id int64) error {
	endpoint := o.url("/code-security/configurations/" + strconv.FormatInt(id, 10))
	if err := call(ctx, o.HTTP, http.MethodDelete, endpoint, token, http.StatusNoContent, nil); err != nil {
		return fmt.Errorf("github: delete %s's code security configuration %d: %w", o.Login, id, err)
	}
	return nil
}

// ConfigurationRepositories reads the repositories a configuration is
// attached to, or being attached to.
func (o Org) ConfigurationRepositories(ctx context.Context, token string, id int64) ([]ConfigurationRepo, error) {
	var out []ConfigurationRepo
	endpoint := o.url("/code-security/configurations/" + strconv.FormatInt(id, 10) + "/repositories?per_page=100")
	err := pages(ctx, o.HTTP, endpoint, token, func(page *[]struct {
		Status     string `json:"status"`
		Repository struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"repository"`
	}) {
		for _, row := range *page {
			out = append(out, ConfigurationRepo{Repo: Repo{ID: row.Repository.ID, Name: row.Repository.Name}, Status: row.Status})
		}
	})
	if err != nil {
		return nil, fmt.Errorf("github: read %s's configuration %d repositories: %w", o.Login, id, err)
	}
	return out, nil
}

// ConfigurationDefaults reads which configurations are the organisation's
// default for repositories created from now on, by configuration id: `all`,
// `public` or `private_and_internal`. A configuration that is nobody's default
// is not in the map. GitHub answers with one row per kind of repository, and a
// configuration that is both rows' is `all`.
func (o Org) ConfigurationDefaults(ctx context.Context, token string) (map[int64]string, error) {
	kinds := map[int64]map[string]bool{}
	err := pages(ctx, o.HTTP, o.url("/code-security/configurations/defaults"), token, func(page *[]struct {
		Default       string `json:"default_for_new_repos"`
		Configuration struct {
			ID int64 `json:"id"`
		} `json:"configuration"`
	}) {
		for _, row := range *page {
			if kinds[row.Configuration.ID] == nil {
				kinds[row.Configuration.ID] = map[string]bool{}
			}
			switch row.Default {
			case "all":
				kinds[row.Configuration.ID]["public"], kinds[row.Configuration.ID]["private_and_internal"] = true, true
			case "public", "private_and_internal":
				kinds[row.Configuration.ID][row.Default] = true
			}
		}
	})
	if err != nil {
		return nil, fmt.Errorf("github: read %s's code security defaults: %w", o.Login, err)
	}
	out := map[int64]string{}
	for id, held := range kinds {
		switch {
		case held["public"] && held["private_and_internal"]:
			out[id] = "all"
		case held["public"]:
			out[id] = "public"
		case held["private_and_internal"]:
			out[id] = "private_and_internal"
		}
	}
	return out, nil
}

// SetConfigurationDefault makes a configuration the organisation's default for
// repositories created from now on: `all`, `public` or `private_and_internal`,
// or, with `none`, the default for none. GitHub takes the kinds of repository it
// now covers from the configuration that held them.
func (o Org) SetConfigurationDefault(ctx context.Context, token string, id int64, value string) error {
	endpoint := o.url("/code-security/configurations/" + strconv.FormatInt(id, 10) + "/defaults")
	if err := send(ctx, o.HTTP, http.MethodPut, endpoint, token, map[string]any{"default_for_new_repos": value}, http.StatusOK, nil); err != nil {
		return fmt.Errorf("github: set %s's code security configuration %d as the default: %w", o.Login, id, err)
	}
	return nil
}

// AttachSecurityConfiguration attaches a configuration to every repository
// (scope `all`) or to the given ones (scope `selected`). GitHub does it
// asynchronously and answers 202. Scope `all` includes archived repositories,
// which hold no configuration of use: the controller attaches the live
// repositories as a selected set instead.
func (o Org) AttachSecurityConfiguration(ctx context.Context, token string, id int64, scope string, repositories []int64) error {
	body := map[string]any{"scope": scope}
	if scope == "selected" {
		body["selected_repository_ids"] = repositories
	}
	endpoint := o.url("/code-security/configurations/" + strconv.FormatInt(id, 10) + "/attach")
	if err := send(ctx, o.HTTP, http.MethodPost, endpoint, token, body, http.StatusAccepted, nil); err != nil {
		return fmt.Errorf("github: attach %s's code security configuration %d: %w", o.Login, id, err)
	}
	return nil
}

// DetachSecurityConfigurations detaches whatever configuration the given
// repositories hold.
func (o Org) DetachSecurityConfigurations(ctx context.Context, token string, repositories []int64) error {
	body := map[string]any{"selected_repository_ids": repositories}
	if err := send(ctx, o.HTTP, http.MethodDelete, o.url("/code-security/configurations/detach"), token, body, http.StatusNoContent, nil); err != nil {
		return fmt.Errorf("github: detach %s's code security configurations: %w", o.Login, err)
	}
	return nil
}
