package keys

import (
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
)

// ToJOSE converts an ASN.1 DER ECDSA signature (what Sign returns, and what
// KMS returns) to the fixed-width r||s form a JWS carries. size is the
// curve's byte length: 48 for ES384.
func ToJOSE(der []byte, size int) ([]byte, error) {
	var sig struct{ R, S *big.Int }
	rest, err := asn1.Unmarshal(der, &sig)
	if err != nil {
		return nil, fmt.Errorf("keys: not an ASN.1 ECDSA signature: %w", err)
	}
	if len(rest) != 0 || sig.R == nil || sig.S == nil || sig.R.Sign() <= 0 || sig.S.Sign() <= 0 {
		return nil, errors.New("keys: not an ASN.1 ECDSA signature")
	}
	if sig.R.BitLen() > size*8 || sig.S.BitLen() > size*8 {
		return nil, fmt.Errorf("keys: signature does not fit %d bytes per integer", size)
	}
	out := make([]byte, 2*size)
	sig.R.FillBytes(out[:size])
	sig.S.FillBytes(out[size:])
	return out, nil
}
