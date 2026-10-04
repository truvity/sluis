// Package kmsfake is a KMS that wraps data key pairs, for tests of the
// `kms-wrapped` signing adapter. It generates real key pairs locally and
// "wraps" the private key by keeping it under a random blob together with the
// encryption context; Decrypt refuses a context that is not exactly the one the
// blob was made under, as KMS does.
package kmsfake

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"maps"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/smithy-go"
)

type wrapped struct {
	context   map[string]string
	plaintext []byte
}

// KMS implements the calls the adapter makes.
type KMS struct {
	mu    sync.Mutex
	blobs map[string]wrapped

	// Generated and Decrypted count the calls that succeeded.
	Generated, Decrypted int
	// DenyDecrypt and DenyGenerate make the calls fail with AccessDenied.
	DenyDecrypt, DenyGenerate bool
	// Contexts are the encryption contexts of every generate call, in order.
	Contexts []map[string]string
	// Plaintexts are every private key ever wrapped (PKCS#8 DER), so that a test
	// can prove none of them is stored anywhere.
	Plaintexts [][]byte
}

// New returns an empty KMS.
func New() *KMS { return &KMS{blobs: map[string]wrapped{}} }

func denied() error {
	return &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized"}
}

// GenerateDataKeyPairWithoutPlaintext implements the adapter's KMS client.
func (k *KMS) GenerateDataKeyPairWithoutPlaintext(_ context.Context, in *kms.GenerateDataKeyPairWithoutPlaintextInput,
	_ ...func(*kms.Options)) (*kms.GenerateDataKeyPairWithoutPlaintextOutput, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.DenyGenerate {
		return nil, denied()
	}
	var pub any
	var der []byte
	var err error
	switch in.KeyPairSpec {
	case types.DataKeyPairSpecEccNistP384:
		key, e := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
		if e != nil {
			return nil, e
		}
		pub = &key.PublicKey
		der, err = x509.MarshalPKCS8PrivateKey(key)
	case types.DataKeyPairSpecRsa3072:
		key, e := rsa.GenerateKey(rand.Reader, 3072)
		if e != nil {
			return nil, e
		}
		pub = &key.PublicKey
		der, err = x509.MarshalPKCS8PrivateKey(key)
	default:
		return nil, &smithy.GenericAPIError{Code: "ValidationException", Message: "unsupported spec " + string(in.KeyPairSpec)}
	}
	if err != nil {
		return nil, err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	blob := make([]byte, 48)
	_, _ = rand.Read(blob)
	k.blobs[string(blob)] = wrapped{context: maps.Clone(in.EncryptionContext), plaintext: der}
	k.Plaintexts = append(k.Plaintexts, der)
	k.Contexts = append(k.Contexts, maps.Clone(in.EncryptionContext))
	k.Generated++
	return &kms.GenerateDataKeyPairWithoutPlaintextOutput{
		KeyPairSpec: in.KeyPairSpec, PrivateKeyCiphertextBlob: blob, PublicKey: pubDER,
	}, nil
}

// Decrypt implements the adapter's KMS client.
func (k *KMS) Decrypt(_ context.Context, in *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.DenyDecrypt {
		return nil, denied()
	}
	w, ok := k.blobs[string(in.CiphertextBlob)]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "InvalidCiphertextException", Message: "unknown ciphertext"}
	}
	if !maps.Equal(w.context, in.EncryptionContext) {
		return nil, &smithy.GenericAPIError{Code: "InvalidCiphertextException", Message: "the encryption context does not match"}
	}
	k.Decrypted++
	return &kms.DecryptOutput{Plaintext: append([]byte(nil), w.plaintext...)}, nil
}
