package sluispulumi

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/curve25519"
)

// The age identity and recipient encodings: Bech32 (BIP 173) of the raw X25519
// scalar and public key, with the human-readable parts age uses. age has no
// exported constructor from raw bytes, so the encoding is made here; the tests
// hold it to age's own parser.
const (
	ageIdentityHRP  = "age-secret-key-"
	ageRecipientHRP = "age"
	bech32Charset   = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
)

// AgeKeypairFromSeed derives the age X25519 identity (`AGE-SECRET-KEY-1...`) and
// its recipient (`age1...`) from 32 secret bytes. The same seed always gives the
// same pair: the Pulumi program keeps the seed in a RandomBytes resource.
func AgeKeypairFromSeed(seed []byte) (identity, recipient string, err error) {
	if len(seed) != curve25519.ScalarSize {
		return "", "", fmt.Errorf("sluispulumi: an age identity is made from %d bytes, got %d", curve25519.ScalarSize, len(seed))
	}
	pub, err := curve25519.X25519(seed, curve25519.Basepoint)
	if err != nil {
		return "", "", fmt.Errorf("sluispulumi: deriving the age recipient: %w", err)
	}
	id, err := bech32Encode(ageIdentityHRP, seed)
	if err != nil {
		return "", "", err
	}
	rcpt, err := bech32Encode(ageRecipientHRP, pub)
	if err != nil {
		return "", "", err
	}
	return strings.ToUpper(id), rcpt, nil
}

func bech32Polymod(values []byte) uint32 {
	gen := [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := uint32(1)
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := range gen {
			if (top>>uint(i))&1 == 1 {
				chk ^= gen[i]
			}
		}
	}
	return chk
}

func bech32Encode(hrp string, data []byte) (string, error) {
	if hrp == "" || strings.ToLower(hrp) != hrp {
		return "", errors.New("sluispulumi: bech32: the human-readable part is lower case and not empty")
	}
	// 8-bit groups to 5-bit groups, padded.
	var values []byte
	var acc, bits uint32
	for _, b := range data {
		acc = acc<<8 | uint32(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			values = append(values, byte(acc>>bits)&31)
		}
	}
	if bits > 0 {
		values = append(values, byte(acc<<(5-bits))&31)
	}
	expand := make([]byte, 0, len(hrp)*2+1+len(values)+6)
	for i := 0; i < len(hrp); i++ {
		expand = append(expand, hrp[i]>>5)
	}
	expand = append(expand, 0)
	for i := 0; i < len(hrp); i++ {
		expand = append(expand, hrp[i]&31)
	}
	expand = append(expand, values...)
	expand = append(expand, 0, 0, 0, 0, 0, 0)
	mod := bech32Polymod(expand) ^ 1
	var sb strings.Builder
	sb.WriteString(hrp)
	sb.WriteByte('1')
	for _, v := range values {
		sb.WriteByte(bech32Charset[v])
	}
	for i := 0; i < 6; i++ {
		sb.WriteByte(bech32Charset[(mod>>uint(5*(5-i)))&31])
	}
	return sb.String(), nil
}
