package transit

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/truvity/sluis/storage/keys"
)

// macDomain starts every MAC input, so the HMAC of this scheme cannot equal
// an HMAC the same key made for any other purpose.
const macDomain = "keys/mac/v1"

var errNoTenant = errors.New("transit: MAC needs a tenant")

// macInput is the unambiguous encoding of (purpose, tenant, data): each
// variable part carries its length, so ("ab", "c") and ("a", "bc") differ.
func macInput(purpose keys.Purpose, tenant string, data []byte) []byte {
	out := []byte(macDomain)
	out = binary.AppendUvarint(out, uint64(len(purpose)))
	out = append(out, purpose...)
	out = binary.AppendUvarint(out, uint64(len(tenant)))
	out = append(out, tenant...)
	return append(out, data...)
}

// MAC returns HMAC-SHA-256, computed by transit's hmac/<key>. For the
// pseudonym purpose that is hmac on the tenant's own key (see the package
// documentation, "Per-tenant keys"); for any other purpose it is hmac on the
// purpose's key, of data prefixed with the purpose and the tenant, and the
// documentation says what that does and does not separate.
func (b *Backend) MAC(ctx context.Context, key string, purpose keys.Purpose, tenant string, data []byte) ([]byte, error) {
	if tenant == "" {
		return nil, errNoTenant
	}
	if perTenant(purpose) {
		return b.tenantMAC(ctx, key, tenant, data)
	}
	resp, err := b.c.Request(ctx, http.MethodPost, b.path("hmac", key), map[string]any{
		"input":       base64.StdEncoding.EncodeToString(macInput(purpose, tenant, data)),
		"algorithm":   "sha2-256",
		"key_version": b.macVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("transit: hmac with %s: %w", key, err)
	}
	var out struct {
		HMAC string `json:"hmac"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		return nil, fmt.Errorf("transit: hmac with %s: %w", key, err)
	}
	raw, ver, err := payload(out.HMAC)
	if err != nil {
		return nil, fmt.Errorf("transit: hmac with %s: %w", key, err)
	}
	if ver != b.macVersion || len(raw) != sha256.Size {
		return nil, fmt.Errorf("transit: hmac with %s: got version %d, %d bytes; want version %d, %d bytes",
			key, ver, len(raw), b.macVersion, sha256.Size)
	}
	return raw, nil
}
