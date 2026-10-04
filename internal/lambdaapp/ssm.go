package lambdaapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// SSMPrefix marks an environment variable whose value is not the value but where
// to read it: `OAUTH_CLIENT_SECRET=ssm:/sluis/private/config/oauth/client-secret`. The
// configuration file names the variable that holds a secret (`secretEnv`), and
// on Lambda the deployment fills that variable at cold start from SSM Parameter
// Store, so the file, which is in the zip, never holds the secret and the
// function's environment holds only a path.
const SSMPrefix = "ssm:"

// SSMRoots are the only places a configuration secret may be read from: the
// service's own under /sluis/private and what it exports under /sluis/export
// (decision D1a). A path outside them is refused, so a function's environment
// cannot be pointed at another service's parameters.
var SSMRoots = []string{"/sluis/private/", "/sluis/export/"}

// ParameterAPI is the part of the SSM client the reader uses.
type ParameterAPI interface {
	GetParameters(ctx context.Context, in *ssm.GetParametersInput, opts ...func(*ssm.Options)) (*ssm.GetParametersOutput, error)
}

// OpenSSM connects with the function's role.
func OpenSSM(ctx context.Context) (ParameterAPI, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("ssm: %w", err)
	}
	return ssm.NewFromConfig(cfg), nil
}

// ResolveEnv replaces every `ssm:<path>` value in environ with the parameter's
// decrypted value, through set, and returns the names it set. Nothing it logs or
// returns contains a value. open is called only if there is something to read,
// so a function with no such variable needs no SSM permission.
//
// It is a small reader of paths and not the `ssm` Secrets adapter of the
// secrets concern, which stores the service's dynamic secrets and has other
// semantics (versions, exports): this reads configuration, once.
func ResolveEnv(ctx context.Context, environ []string, set func(name, value string) error, open func(context.Context) (ParameterAPI, error)) ([]string, error) {
	paths := map[string]string{} // variable -> path
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(value, SSMPrefix) {
			continue
		}
		path := strings.TrimPrefix(value, SSMPrefix)
		if err := checkRoot(path); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		paths[name] = path
	}
	if len(paths) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(paths))
	list := make([]string, 0, len(paths))
	for name, path := range paths {
		names = append(names, name)
		list = append(list, path)
	}
	slices.Sort(names)
	values, err := readParameters(ctx, open, list)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, name := range names {
		value, ok := values[paths[name]]
		if !ok {
			errs = append(errs, fmt.Errorf("ssm: %s: %s was not returned", name, paths[name]))
			continue
		}
		if err := set(name, value); err != nil {
			errs = append(errs, err)
		}
	}
	return names, errors.Join(errs...)
}

func checkRoot(path string) error {
	if strings.Contains(path, "..") || !slices.ContainsFunc(SSMRoots, func(root string) bool { return strings.HasPrefix(path, root) && len(path) > len(root) }) {
		return fmt.Errorf("%q is not under %s", path, strings.Join(SSMRoots, " or "))
	}
	return nil
}

// readParameters reads decrypted parameters, ten to a call, and returns them by
// path. A parameter that does not exist is an error naming its path.
func readParameters(ctx context.Context, open func(context.Context) (ParameterAPI, error), paths []string) (map[string]string, error) {
	api, err := open(ctx)
	if err != nil {
		return nil, err
	}
	paths = slices.Clone(paths)
	slices.Sort(paths)
	paths = slices.Compact(paths)
	values := map[string]string{}
	for chunk := range slices.Chunk(paths, 10) {
		out, err := api.GetParameters(ctx, &ssm.GetParametersInput{Names: chunk, WithDecryption: aws.Bool(true)})
		if err != nil {
			return nil, fmt.Errorf("ssm: %w", err)
		}
		if len(out.InvalidParameters) > 0 {
			return nil, fmt.Errorf("ssm: no such parameter: %s", strings.Join(out.InvalidParameters, ", "))
		}
		for _, p := range out.Parameters {
			values[aws.ToString(p.Name)] = aws.ToString(p.Value)
		}
	}
	return values, nil
}

// EnvSecretFiles names the variable that lists the files to write: a JSON array
// of {"parameter":"/sluis/private/...","path":"/tmp/..."}.
const EnvSecretFiles = "SLUIS_SECRET_FILES"

// SecretFile is one parameter written to one file.
type SecretFile struct {
	Parameter string `json:"parameter"`
	Path      string `json:"path"`
}

// secretDir is the only place a secret file may be: /tmp is the function's
// writable, per-environment, never-shipped disk.
const secretDir = "/tmp/"

// WriteSecretFiles writes each parameter's decrypted value to its path, mode
// 0600 in directories of mode 0700, and returns the paths. Every `*File`
// setting of the configuration (the GitHub App key files, the Slack secrets,
// `signingKey.kms.stateSecretFile`) then works as on Kubernetes by naming one of
// them. Values are written exactly as stored, with no newline added.
func WriteSecretFiles(ctx context.Context, spec string, open func(context.Context) (ParameterAPI, error)) ([]string, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, nil
	}
	var files []SecretFile
	if err := json.Unmarshal([]byte(spec), &files); err != nil {
		return nil, fmt.Errorf("%s is not a JSON array of {parameter, path}: %w", EnvSecretFiles, err)
	}
	if len(files) == 0 {
		return nil, nil
	}
	params := make([]string, 0, len(files))
	for _, f := range files {
		if err := checkRoot(f.Parameter); err != nil {
			return nil, fmt.Errorf("%s: %w", EnvSecretFiles, err)
		}
		if clean := filepath.Clean(f.Path); clean != f.Path || !strings.HasPrefix(f.Path, secretDir) || strings.HasSuffix(f.Path, "/") {
			return nil, fmt.Errorf("%s: path %q is not a clean file path under %s", EnvSecretFiles, f.Path, secretDir)
		}
		params = append(params, f.Parameter)
	}
	values, err := readParameters(ctx, open, params)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(f.Path, []byte(values[f.Parameter]), 0o600); err != nil {
			return nil, fmt.Errorf("%s: %w", f.Path, err)
		}
		paths = append(paths, f.Path)
	}
	return paths, nil
}
