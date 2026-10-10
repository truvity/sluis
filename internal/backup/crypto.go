package backup

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"context"
	"github.com/truvity/sluis/storage/keys"
)

// Key is what the archive needs from the configured key: a fresh wrapped data
// key to write with, and the unwrapping of one to read with. *keys.Key is
// one; it carries the encryption context, which this package neither adds to
// nor changes.
type Key interface {
	GenerateDataKey(ctx context.Context) (keys.DataKey, error)
	UnwrapDataKey(ctx context.Context, wrapped []byte, opts ...keys.DecryptOption) ([]byte, error)
}

var _ Key = (*keys.Key)(nil)

const (
	dataKeyLen = 32
	nonceLen   = 12
	chunkMagic = "SLBC"
	// maxRecord bounds one record, so a corrupt length cannot ask for a huge
	// allocation.
	maxRecord = 64 << 20
)

// derive returns a key from the data key for one use, bound by its label.
func derive(dk []byte, label string) ([]byte, error) {
	return hkdf.Key(sha256.New, dk, nil, "sluis-backup/"+label, 32)
}

func gcm(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func chunkAAD(format int, name string) []byte {
	return []byte(fmt.Sprintf("sluis-backup/chunk\x00%d\x00%s", format, name))
}

// sealChunk seals plaintext as the chunk called name.
func sealChunk(dk []byte, format int, name string, plaintext []byte) ([]byte, error) {
	k, err := derive(dk, "chunk/"+name)
	if err != nil {
		return nil, err
	}
	a, err := gcm(k)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(chunkMagic)+1+nonceLen+len(plaintext)+a.Overhead())
	out = append(out, chunkMagic...)
	out = append(out, byte(format))
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out = append(out, nonce...)
	return a.Seal(out, nonce, plaintext, chunkAAD(format, name)), nil
}

// openChunk opens a sealed chunk called name.
func openChunk(dk []byte, format int, name string, sealed []byte) ([]byte, error) {
	hdr := len(chunkMagic) + 1 + nonceLen
	if len(sealed) < hdr+16 || string(sealed[:len(chunkMagic)]) != chunkMagic || int(sealed[len(chunkMagic)]) != format {
		return nil, fmt.Errorf("%w: chunk %s has no valid header", ErrIntegrity, name)
	}
	k, err := derive(dk, "chunk/"+name)
	if err != nil {
		return nil, err
	}
	a, err := gcm(k)
	if err != nil {
		return nil, err
	}
	pt, err := a.Open(nil, sealed[len(chunkMagic)+1:hdr], sealed[hdr:], chunkAAD(format, name))
	if err != nil {
		return nil, fmt.Errorf("%w: chunk %s does not authenticate", ErrIntegrity, name)
	}
	return pt, nil
}

// envelope is manifest.json: the wrapped data key, the manifest body as
// written, and the MAC over both.
type envelope struct {
	Format   int             `json:"format"`
	Key      []byte          `json:"key"`
	Manifest json.RawMessage `json:"manifest"`
	MAC      []byte          `json:"mac"`
}

// compact is the canonical form of the body the MAC covers: the bytes with
// insignificant whitespace removed, so a reformatted file still verifies.
func compact(body []byte) ([]byte, error) {
	var b bytes.Buffer
	if err := json.Compact(&b, body); err != nil {
		return nil, fmt.Errorf("%w: manifest is not JSON", ErrFormat)
	}
	return b.Bytes(), nil
}

func manifestMAC(dk []byte, format int, wrapped, body []byte) ([]byte, error) {
	k, err := derive(dk, "manifest")
	if err != nil {
		return nil, err
	}
	h := hmac.New(sha256.New, k)
	h.Write([]byte("sluis-backup/manifest\x00"))
	var n [8]byte
	binary.BigEndian.PutUint32(n[:4], uint32(format))
	binary.BigEndian.PutUint32(n[4:], uint32(len(wrapped)))
	h.Write(n[:])
	h.Write(wrapped)
	h.Write(body)
	return h.Sum(nil), nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
