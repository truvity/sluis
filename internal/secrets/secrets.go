// Package secrets resolves the secrets a service document declares, by name.
//
// A document never holds a secret: it names one (`valkey.passwordSecret:
// valkey/password`), and `secrets.source` says how a name is delivered
// (truvity/policy docs/contracts/config.md, section 5):
//
//	env   the variable SLUIS_SECRET_<NAME>, the name upper-cased with every
//	      character that is not a letter or a digit made an underscore:
//	      `valkey/password` is SLUIS_SECRET_VALKEY_PASSWORD. For a local run.
//	file  the file <root>/<name>, read on every use, so a rotated Secret
//	      mounted by the platform takes effect without a restart.
//	ssm   the SecureString <root>/private/config/<name> of AWS SSM Parameter
//	      Store (layout v3, root `/sluis/<instance>`): every parameter under
//	      the prefix is read at once, decrypted, and read again when it is
//	      older than the refresh (five minutes), so a rotation reaches a
//	      function within that.
//
// The names are the layout's (docs/decisions/0036): `clients/<id>/secret`,
// `providers/google/<id>/client-id` and `client-secret`,
// `issuer/state-secret`, `recovery/password`, `directory/<id>/key`,
// `valkey/password`. Nothing here logs or returns a value in an error.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrNotFound is a name the source does not deliver.
var ErrNotFound = errors.New("secrets: not delivered")

// Source delivers secrets by name.
type Source interface {
	// Get returns the secret, or an error wrapping ErrNotFound when the
	// source has none by that name. The value is never in an error.
	Get(ctx context.Context, name string) (string, error)
	// Describe says where a name is read from, for a log line: never a
	// value.
	Describe(name string) string
}

// The kinds of source, as `secrets.source` spells them.
const (
	KindEnv  = "env"
	KindFile = "file"
	KindSSM  = "ssm"
)

// EnvPrefix begins every variable the env source reads.
const EnvPrefix = "SLUIS_SECRET_"

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*$`)

// Check refuses a name that is not one: segments of letters, digits, '.', '_'
// and '-', separated by '/', none empty and none a dot or two.
func Check(name string) error {
	if !namePattern.MatchString(name) {
		return errors.New("secrets: not a secret name: segments of letters, digits, '.', '_' and '-', separated by '/'")
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "." || seg == ".." {
			return errors.New("secrets: not a secret name: no segment may be a dot or two")
		}
	}
	return nil
}

// The names of the layout, built in one place.

// ClientSecret is a confidential client's secret.
func ClientSecret(clientID string) string { return "clients/" + clientID + "/secret" }

// ProviderClientID and ProviderClientSecret are a Google OAuth client's halves.
func ProviderClientID(provider string) string {
	return "providers/google/" + provider + "/client-id"
}

// ProviderClientSecret is a Google OAuth client's secret.
func ProviderClientSecret(provider string) string {
	return "providers/google/" + provider + "/client-secret"
}

// CheckEnvNames refuses names two of which the env source would read from one
// variable (`a/b` and `a-b` are both SLUIS_SECRET_A_B): one would silently be
// the other's secret.
func CheckEnvNames(names []string) error {
	seen := map[string]string{}
	for _, n := range names {
		if n == "" {
			continue
		}
		v := EnvName(n)
		if other, clash := seen[v]; clash && other != n {
			return fmt.Errorf("secrets: %s and %s are both read from %s: rename one, or use another source", other, n, v)
		}
		seen[v] = n
	}
	return nil
}

// EnvName is the variable the env source reads a name from.
func EnvName(name string) string {
	var b strings.Builder
	b.WriteString(EnvPrefix)
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Env reads SLUIS_SECRET_<NAME> through getenv, on every use.
type Env struct{ Getenv func(string) string }

// Get implements [Source].
func (e Env) Get(_ context.Context, name string) (string, error) {
	if err := Check(name); err != nil {
		return "", err
	}
	getenv := e.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	v := getenv(EnvName(name))
	if v == "" {
		return "", fmt.Errorf("%w: %s (the variable %s is unset or empty)", ErrNotFound, name, EnvName(name))
	}
	return v, nil
}

// Describe implements [Source].
func (e Env) Describe(name string) string { return "env " + EnvName(name) }

// File reads <Root>/<name> on every use, so a rotated mount is seen at once.
type File struct{ Root string }

// Get implements [Source].
func (f File) Get(_ context.Context, name string) (string, error) {
	if err := Check(name); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(f.Root, filepath.FromSlash(name))) //nolint:gosec // the name is checked and the root is configuration
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: %s (no file under %s)", ErrNotFound, name, f.Root)
	}
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			err = pathErr.Err
		}
		return "", fmt.Errorf("secrets: %s could not be read: %w", name, err)
	}
	v := strings.TrimSpace(string(raw))
	if v == "" {
		return "", fmt.Errorf("%w: %s (the file is empty)", ErrNotFound, name)
	}
	return v, nil
}

// Describe implements [Source].
func (f File) Describe(name string) string {
	return "file " + filepath.Join(f.Root, filepath.FromSlash(name))
}

// Location is where a converted v1 document said one secret was: a variable or
// a file.
type Location struct {
	Env  string
	File string
}

// Legacy is what a v1 document named, secret by secret, in front of another
// source: a v1 document keeps reading the variables and the files it named
// until it is moved to v2. Dir, when set, holds one file per client (v1's
// `clientSecretsDir`).
type Legacy struct {
	Locations map[string]Location
	ClientDir string
	Next      Source
}

// Get implements [Source].
func (l Legacy) Get(ctx context.Context, name string) (string, error) {
	if loc, ok := l.Locations[name]; ok {
		switch {
		case loc.Env != "":
			if v := os.Getenv(loc.Env); v != "" {
				return v, nil
			}
			return "", fmt.Errorf("%w: %s (the variable %s is unset or empty)", ErrNotFound, name, loc.Env)
		case loc.File != "":
			return File{Root: filepath.Dir(loc.File)}.Get(ctx, filepath.Base(loc.File))
		}
	}
	if l.ClientDir != "" {
		if id, ok := strings.CutPrefix(name, "clients/"); ok {
			if id, ok = strings.CutSuffix(id, "/secret"); ok && !strings.Contains(id, "/") {
				return File{Root: l.ClientDir}.Get(ctx, id)
			}
		}
	}
	if l.Next == nil {
		return "", fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return l.Next.Get(ctx, name)
}

// Describe implements [Source].
func (l Legacy) Describe(name string) string {
	if loc, ok := l.Locations[name]; ok {
		if loc.Env != "" {
			return "env " + loc.Env
		}
		return "file " + loc.File
	}
	if l.Next == nil {
		return "nowhere"
	}
	return l.Next.Describe(name)
}
