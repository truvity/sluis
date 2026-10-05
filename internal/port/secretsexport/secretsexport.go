// Package secretsexport is the [port.Export] over the Secrets port: a copy of a
// secret OUT of the service is the Secrets path `export/<path>`, which the ssm
// adapter keeps at `/sluis/export/<path>` (docs/reference/storage-layout.md).
// It is the export target whenever a Secrets adapter is configured and
// `ports.export` is not (ADR 0034, as amended by the Secrets port).
//
// # Value
//
// One secret per target: a JSON object of the written properties, text values
// only, keys in sorted order and no HTML escaping, for example
//
//	{"botToken":"xoxb-…","signingSecret":"…"}
//
// An export entry's `namespace` is honoured by a Secrets adapter that has
// namespaces ([port.NamespacedSecrets], the openbao one) and refused by one that
// has none (ssm).
//
// A consumer's External Secrets Operator reads a property with
// `remoteRef: {key: /sluis/export/<path>, property: botToken}`. A replace writes
// exactly the properties; a patch reads the object, sets the given properties
// and keeps the others. Putting what the secret already holds writes nothing,
// so SSM makes no new version.
package secretsexport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/truvity/sluis/internal/port"
)

// Export is the adapter.
type Export struct{ secrets port.Secrets }

var _ port.Export = (*Export)(nil)

// New returns the export over a Secrets port.
func New(secrets port.Secrets) *Export { return &Export{secrets: secrets} }

// Path is the Secrets path of an export target.
func Path(target port.ExportTarget) string { return port.ExportPrefix + target.Path }

// path checks the target and says where it is written: the Secrets path, and
// the Secrets to write it with. An export entry's `namespace` is the store's
// namespace: an adapter that has them (openbao) writes the same `export/<path>`
// of the same installation in that namespace; one that has none refuses it.
func (e *Export) path(target port.ExportTarget, check func() error) (string, port.Secrets, error) {
	if err := check(); err != nil {
		return "", nil, err
	}
	secrets := e.secrets
	if target.Namespace != "" {
		ns, ok := e.secrets.(port.NamespacedSecrets)
		if !ok {
			return "", nil, fmt.Errorf("%w: the secrets adapter has no namespaces (%q): use ports.export.openbao or the openbao secrets adapter", port.ErrUnsupported, target.Namespace)
		}
		var err error
		if secrets, err = ns.In(target.Namespace); err != nil {
			return "", nil, err
		}
	}
	p := Path(target)
	if err := port.CheckSecretPath(p); err != nil {
		return "", nil, err
	}
	return p, secrets, nil
}

// Encode is the value of an export: the properties as one JSON object.
func Encode(properties map[string]string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(properties) // strings only; map keys are sorted
	return bytes.TrimRight(b.Bytes(), "\n")
}

// Put implements [port.Export].
func (e *Export) Put(ctx context.Context, target port.ExportTarget, properties map[string]string, mode port.ExportMode) error {
	p, secrets, err := e.path(target, func() error { return port.CheckExport(target, properties, mode) })
	if err != nil {
		return err
	}
	cur, err := secrets.Get(ctx, p)
	found := err == nil
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		return err
	}
	next := properties
	if mode == port.ExportPatch && found {
		var have map[string]string
		if json.Unmarshal(cur.Value, &have) == nil {
			next = maps.Clone(have)
			maps.Copy(next, properties)
		}
	}
	value := Encode(next)
	if found && bytes.Equal(cur.Value, value) {
		return nil
	}
	_, err = secrets.Put(ctx, p, value)
	return err
}

// Delete implements [port.Export].
func (e *Export) Delete(ctx context.Context, target port.ExportTarget) error {
	p, secrets, err := e.path(target, func() error { return port.CheckExportPath(target.Path) })
	if err != nil {
		return err
	}
	return secrets.Delete(ctx, p)
}
