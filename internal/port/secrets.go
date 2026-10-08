package port

import (
	"context"
	"fmt"
	"strings"
)

// MaxSecret is the largest secret value: the smallest limit among the stores
// the adapters stand for (an SSM advanced parameter holds 8 KiB), with
// headroom for none. Larger content is a blob.
const MaxSecret = 8 << 10

// ExportPrefix is where layout v3 kept the copies of secrets made out of the
// service (ADR 0034, retired by ADR 0041): `export/<path>`. Nothing writes it
// any more; the v3 adapters still address it, so a migration can read and
// delete what an earlier release left.
const ExportPrefix = "export/"

// Secret is a secret as read.
type Secret struct {
	Value []byte
	// Version is opaque and changes on every write. Callers compare it for
	// equality and never order it.
	Version string
}

// Secrets is the store of dynamic secrets: whole values
// under slash-separated paths, each with a version. It is NOT State: a value
// here is a credential, so a listing returns names and never values, and an
// adapter is a store that protects what it holds (see [Descriptor.SecretStore]).
//
// Paths are `a/b/c`, each segment of letters, digits, `.`, `_` and `-`
// ([CheckSecretPath]). The errors are the package's: [ErrNotFound],
// [ErrConflict], [ErrTooLarge], [ErrUnavailable], [ErrUnsupported].
type Secrets interface {
	// Get returns the secret: ErrNotFound if absent.
	Get(ctx context.Context, path string) (Secret, error)
	// Put writes unconditionally and returns the new version.
	Put(ctx context.Context, path string, value []byte) (string, error)
	// PutIfVersion writes only if the version is still version: ErrConflict
	// if it moved, ErrNotFound if the secret is gone. An empty version means
	// "only if absent" and is ErrConflict over an existing secret.
	PutIfVersion(ctx context.Context, path string, value []byte, version string) (string, error)
	// Delete removes the secret; an absent one is not an error.
	Delete(ctx context.Context, path string) error
	// List returns the paths under a prefix, sorted, at every depth. The
	// prefix is a whole number of segments (`export` and `export/` are the
	// same); the empty prefix lists everything.
	List(ctx context.Context, prefix string) ([]string, error)
}

// CheckSecretPath refuses a path that is empty, begins or ends with a slash,
// holds an empty or relative segment or a character outside letters, digits,
// `.`, `_` and `-`.
func CheckSecretPath(path string) error {
	if path == "" || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return fmt.Errorf("%w: secret path %q", ErrUnsupported, path)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("%w: secret path %q", ErrUnsupported, path)
		}
		for _, r := range seg {
			ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
			if !ok {
				return fmt.Errorf("%w: secret path %q", ErrUnsupported, path)
			}
		}
	}
	return nil
}

// CheckSecretWrite is the refusal every adapter shares before a write.
func CheckSecretWrite(path string, value []byte) error {
	if err := CheckSecretPath(path); err != nil {
		return err
	}
	if len(value) > MaxSecret {
		return ErrTooLarge
	}
	return nil
}

// SecretPrefix normalises a listing prefix to "" or one ending in a slash,
// refusing one that is not whole segments.
func SecretPrefix(prefix string) (string, error) {
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "" {
		return "", nil
	}
	if err := CheckSecretPath(prefix); err != nil {
		return "", err
	}
	return prefix + "/", nil
}
