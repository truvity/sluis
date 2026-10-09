package openbao

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/truvity/sluis/internal/port"
)

// This file is the [port.Secrets] adapter over an OpenBao (or Vault) KV
// version 2 mount, laid out exactly as the ssm adapter lays out SSM (layout v3,
// docs/decisions/0036): the same Client, the same login and the same
// TLS-verified connection as the Export adapter, a different shape of key.
//
// # Layout
//
// A port path `p` is the KV key `<root>/private/<p>`, except a path under
// `export/`, which is `<root>/export/<rest>`. The root is the installation's,
// `sluis` (in a namespace of its own) or `sluis/<instance>`, and has no default: two installations share a mount by
// their roots. The split lets a consumer be granted `<mount>/data/<root>/export/*`
// and nothing else.
//
// # Values
//
// A value is stored as a KV secret of ONE field, `value` (text), or `value_b64`
// (base64, for bytes that are not UTF-8), so a human seeds one with
// `bao kv put kv/<root>/private/config/<name> value=...`.
//
// A secret under `export/` whose value is the JSON object the secrets export
// writes (text properties, keys sorted, see secretsexport) is stored as the
// properties themselves, one field each, so a consumer's External Secrets
// reads `property: botToken` of the key, exactly as it reads a copy made by the
// openbao Export adapter. Get puts the object back together, byte for byte. A
// value that is not such an object is stored in the one field above.
//
// # Operations
//
//   - Get is GET `data/<key>`; the version is KV's own, a counter that grows on
//     every write.
//   - Put is POST `data/<key>`.
//   - PutIfVersion is a POST with `options.cas`: atomic on the server, unlike
//     ssm's. An empty version is cas 0, "only if absent". The mount must not
//     require cas on every write (`cas_required`), or Put is refused.
//   - Delete is DELETE `metadata/<key>`: every version, as the Export adapter
//     does. An absent key is not an error.
//   - List is `GET metadata/<prefix>?list=true`, walked to every depth.
//
// The policy the service needs is in docs/reference/sluis/adapters.md.
//
// Nothing here logs or returns a value: an error names the operation, the path
// and the status and the server's own error text.

// SecretsConfig is what NewSecrets needs: a [Config] (whose Namespace is the one
// the secrets live in) and the root.
type SecretsConfig struct {
	Config
	// Root is the installation's key hierarchy under the mount,
	// `sluis` when the installation has an OpenBao namespace of its own (the
	// namespace names the instance), `sluis/<instance>` in a shared one: no
	// leading or trailing slash. Required.
	Root string
}

// SecretsSettings is `adapters.secrets.settings`, as a document spells it.
type SecretsSettings struct {
	Address   string `json:"address"`
	CAFile    string `json:"caFile,omitempty"`
	Mount     string `json:"mount,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Root      string `json:"root"`
	Auth      struct {
		Method    string `json:"method"`
		Mount     string `json:"mount,omitempty"`
		Role      string `json:"role"`
		TokenFile string `json:"tokenFile,omitempty"`
	} `json:"auth"`
}

// Config is the settings as a [SecretsConfig].
func (s SecretsSettings) Config() SecretsConfig {
	return SecretsConfig{
		Config: Config{
			Address: s.Address, CAFile: s.CAFile, Mount: s.Mount, Namespace: s.Namespace,
			Auth: Auth{Method: s.Auth.Method, Mount: s.Auth.Mount, Role: s.Auth.Role, TokenFile: s.Auth.TokenFile},
		},
		Root: s.Root,
	}
}

const (
	privateDir = "private"
	exportDir  = "export"

	fieldText   = "value"
	fieldBinary = "value_b64"
)

// Secrets is the adapter.
type Secrets struct {
	c         *Client
	root      string
	namespace string
}

var (
	_ port.Secrets = (*Secrets)(nil)
)

// NewSecrets validates the configuration and returns the adapter. It does not
// connect: an OpenBao that is down at start must not stop the service.
func NewSecrets(cfg SecretsConfig) (*Secrets, error) {
	root, err := secretsRoot(cfg.Root)
	if err != nil {
		return nil, err
	}
	c, err := NewClient(cfg.Config)
	if err != nil {
		return nil, err
	}
	return &Secrets{c: c, root: root, namespace: cfg.Namespace}, nil
}

func secretsRoot(root string) (string, error) {
	if root == "" {
		return "", errors.New("openbao: root is required: the installation's root: sluis in its own OpenBao namespace, or sluis/<instance> in a shared one")
	}
	if err := port.CheckSecretPath(root); err != nil {
		return "", fmt.Errorf("openbao: root %q must be sluis or sluis/<instance>: no leading or trailing slash, segments of letters, digits, '.', '_' and '-'", root)
	}
	// An instance named private or export would nest its tree under another's.
	for _, seg := range strings.Split(root, "/") {
		if seg == privateDir || seg == exportDir {
			return "", fmt.Errorf("openbao: root %q has a segment %q: an instance may not be named private or export", root, seg)
		}
	}
	return root, nil
}

// key maps a port path to the KV key under the mount.
func (s *Secrets) key(path string) string {
	if rest, ok := strings.CutPrefix(path, port.ExportPrefix); ok {
		return s.root + "/" + exportDir + "/" + rest
	}
	return s.root + "/" + privateDir + "/" + path
}

func (s *Secrets) dataPath(path string) string { return s.c.mount + "/data/" + s.key(path) }

func isExport(path string) bool { return strings.HasPrefix(path, port.ExportPrefix) }

// Get implements [port.Secrets].
func (s *Secrets) Get(ctx context.Context, path string) (port.Secret, error) {
	if err := port.CheckSecretPath(path); err != nil {
		return port.Secret{}, err
	}
	secret, found, err := s.read(ctx, path)
	if err != nil {
		return port.Secret{}, err
	}
	if !found {
		return port.Secret{}, port.ErrNotFound
	}
	return secret, nil
}

func (s *Secrets) read(ctx context.Context, path string) (port.Secret, bool, error) {
	status, body, err := s.c.call(ctx, s.namespace, http.MethodGet, s.dataPath(path), "", nil)
	if err != nil {
		return port.Secret{}, false, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		return port.Secret{}, false, nil
	default:
		return port.Secret{}, false, s.refusal("get", path, status, body)
	}
	var out struct {
		Data struct {
			Data     map[string]any `json:"data"`
			Metadata struct {
				Version json.Number `json:"version"`
			} `json:"metadata"`
		} `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err = dec.Decode(&out); err != nil || out.Data.Data == nil {
		// A key whose current version is deleted answers 404; a body with no data is not a secret this adapter wrote.
		return port.Secret{}, false, fmt.Errorf("%w: the answer to a read of %s is not a KV version", port.ErrUnavailable, path)
	}
	value, err := decodeFields(out.Data.Data)
	if err != nil {
		return port.Secret{}, false, fmt.Errorf("openbao: %s: %w", path, err)
	}
	return port.Secret{Value: value, Version: out.Data.Metadata.Version.String()}, true, nil
}

// Put implements [port.Secrets].
func (s *Secrets) Put(ctx context.Context, path string, value []byte) (string, error) {
	payload, err := s.prepare(path, value, nil)
	if err != nil {
		return "", err
	}
	return s.write(ctx, path, payload, false)
}

// PutIfVersion implements [port.Secrets]. Atomic on the server (KV's
// check-and-set): a writer that lands in between makes this one ErrConflict.
func (s *Secrets) PutIfVersion(ctx context.Context, path string, value []byte, version string) (string, error) {
	cas := uint64(0)
	if version != "" {
		n, err := strconv.ParseUint(version, 10, 64)
		if err != nil || n == 0 {
			if err := port.CheckSecretWrite(path, value); err != nil {
				return "", err
			}
			return "", port.ErrConflict // no version of this adapter looks like that
		}
		cas = n
	}
	payload, err := s.prepare(path, value, &cas)
	if err != nil {
		return "", err
	}
	if version != "" {
		// Tell a secret that is gone from one that moved on.
		cur, found, err := s.read(ctx, path)
		if err != nil {
			return "", err
		}
		if !found {
			return "", port.ErrNotFound
		}
		if cur.Version != version {
			return "", port.ErrConflict
		}
	}
	return s.write(ctx, path, payload, true)
}

func (s *Secrets) prepare(path string, value []byte, cas *uint64) ([]byte, error) {
	if err := port.CheckSecretWrite(path, value); err != nil {
		return nil, err
	}
	data := encodeFields(path, value)
	req := map[string]any{"data": data}
	if cas != nil {
		req["options"] = map[string]any{"cas": *cas}
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("openbao: encode %s: %w", path, err)
	}
	return raw, nil
}

func (s *Secrets) write(ctx context.Context, path string, payload []byte, conditional bool) (string, error) {
	status, body, err := s.c.call(ctx, s.namespace, http.MethodPost, s.dataPath(path), "application/json", payload)
	if err != nil {
		return "", err
	}
	if status == http.StatusBadRequest && strings.Contains(string(body), "check-and-set") {
		if conditional {
			return "", port.ErrConflict
		}
		// Not a lost race: nothing was conditional. The mount refuses writes that carry no cas.
		return "", fmt.Errorf("openbao: write %s: the mount requires check-and-set on every write (cas_required): "+
			"unset cas_required on the mount, which this adapter's unconditional Put cannot satisfy", path)
	}
	if status < 200 || status >= 300 {
		return "", s.refusal("write", path, status, body)
	}
	var out struct {
		Data struct {
			Version json.Number `json:"version"`
		} `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err = dec.Decode(&out); err != nil || out.Data.Version.String() == "" {
		return "", fmt.Errorf("%w: the answer to a write of %s names no version", port.ErrUnavailable, path)
	}
	return out.Data.Version.String(), nil
}

// Delete implements [port.Secrets].
func (s *Secrets) Delete(ctx context.Context, path string) error {
	if err := port.CheckSecretPath(path); err != nil {
		return err
	}
	status, body, err := s.c.call(ctx, s.namespace, http.MethodDelete, s.c.mount+"/metadata/"+s.key(path), "", nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return s.requireMount(ctx)
	}
	if status < 200 || status >= 300 {
		return s.refusal("delete", path, status, body)
	}
	return nil
}

// maxListDepth bounds the walk of a listing: a tree deeper than any layout
// holds is a loop or an abuse, and is refused rather than followed.
const maxListDepth = 16

// List implements [port.Secrets].
func (s *Secrets) List(ctx context.Context, prefix string) ([]string, error) {
	p, err := port.SecretPrefix(prefix)
	if err != nil {
		return nil, err
	}
	type root struct{ key, port string }
	var roots []root
	switch {
	case p == "":
		roots = []root{{s.root + "/" + privateDir + "/", ""}, {s.root + "/" + exportDir + "/", port.ExportPrefix}}
	case strings.HasPrefix(p, port.ExportPrefix):
		rest := strings.TrimPrefix(p, port.ExportPrefix)
		roots = []root{{s.root + "/" + exportDir + "/" + rest, port.ExportPrefix + rest}}
	default:
		roots = []root{{s.root + "/" + privateDir + "/" + p, p}}
	}
	out := []string{}
	for _, r := range roots {
		found, err := s.walk(ctx, r.key, r.port, 0)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	if len(out) == 0 {
		if err = s.requireMount(ctx); err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// walk lists a directory of keys (key ends in a slash, so does as) and every
// directory below it.
func (s *Secrets) walk(ctx context.Context, key, as string, depth int) ([]string, error) {
	if depth > maxListDepth {
		return nil, fmt.Errorf("openbao: list %s: the tree is deeper than %d levels", as, maxListDepth)
	}
	status, body, err := s.c.call(ctx, s.namespace, http.MethodGet, s.c.mount+"/metadata/"+key+"?list=true", "", nil)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, nil
	default:
		return nil, s.refusal("list", strings.TrimSuffix(as, "/"), status, body)
	}
	var out struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	if err = json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%w: the answer to a list of %s is not JSON", port.ErrUnavailable, as)
	}
	var paths []string
	for _, k := range out.Data.Keys {
		if dir, ok := strings.CutSuffix(k, "/"); ok {
			sub, err := s.walk(ctx, key+dir+"/", as+dir+"/", depth+1)
			if err != nil {
				return nil, err
			}
			paths = append(paths, sub...)
			continue
		}
		paths = append(paths, as+k)
	}
	return paths, nil
}

// requireMount runs after a 404 that reads as "nothing there" (an empty
// listing, a delete of an absent key), which is also what a wrong mount or a
// wrong namespace answers. It asks the server whether the mount is there: if it
// says it is not, the configuration is wrong and the error says so. An answer
// that is anything else (a policy without access to the endpoint, a server
// error) proves nothing, and the 404 is taken as it reads.
func (s *Secrets) requireMount(ctx context.Context) error {
	status, body, err := s.c.call(ctx, s.namespace, http.MethodGet, "sys/internal/ui/mounts/"+s.c.mount, "", nil)
	if err != nil || status != http.StatusNotFound {
		return nil //nolint:nilerr // the probe is advisory
	}
	_ = body
	return fmt.Errorf("openbao: there is no mount %q in namespace %q: check adapters.secrets.settings.mount and .namespace", s.c.mount, s.namespace)
}

func (s *Secrets) refusal(what, path string, status int, body []byte) error {
	return answer(what, target{Namespace: s.namespace, Path: path}, status, body)
}

// encodeFields is the KV data of a value.
func encodeFields(path string, value []byte) map[string]string {
	if isExport(path) {
		if props, ok := exportProperties(value); ok {
			return props
		}
	}
	if utf8.Valid(value) {
		return map[string]string{fieldText: string(value)}
	}
	return map[string]string{fieldBinary: base64.StdEncoding.EncodeToString(value)}
}

// exportProperties reports whether value is exactly what the secrets export
// writes for some properties (see [canonical]) and says them. An object that
// would read back as a one-field secret of this adapter is not.
func exportProperties(value []byte) (map[string]string, bool) {
	var props map[string]string
	if len(value) == 0 || value[0] != '{' || json.Unmarshal(value, &props) != nil || len(props) == 0 {
		return nil, false
	}
	if len(props) == 1 {
		for k := range props {
			if k == fieldText || k == fieldBinary {
				return nil, false
			}
		}
	}
	if !bytes.Equal(canonical(props), value) {
		return nil, false
	}
	return props, true
}

// canonical is the JSON the secrets export writes: keys sorted, no HTML
// escaping. Reading back what was written depends on it being the same.
func canonical(props map[string]string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(props) // strings only; map keys are sorted
	return bytes.TrimRight(b.Bytes(), "\n")
}

// decodeFields is the inverse of [encodeFields].
func decodeFields(data map[string]any) ([]byte, error) {
	if len(data) == 1 {
		if v, ok := data[fieldText].(string); ok {
			return []byte(v), nil
		}
		if v, ok := data[fieldBinary].(string); ok {
			b, err := base64.StdEncoding.DecodeString(v)
			if err != nil {
				return nil, fmt.Errorf("the field %s is not base64", fieldBinary)
			}
			return b, nil
		}
	}
	props := make(map[string]string, len(data))
	for k, v := range data {
		text, ok := v.(string)
		if !ok {
			text = fmt.Sprint(v)
		}
		props[k] = text
	}
	return canonical(props), nil
}
