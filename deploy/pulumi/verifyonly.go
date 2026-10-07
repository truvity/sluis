package sluispulumi

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	sluisconfig "github.com/truvity/sluis/config"
)

// VerifyOnlyKeyPath is where a function finds LambdaArgs.VerifyOnly[index]:
// `/opt/sluis/verify-keys/<index>.pem`, in the configuration layer.
func VerifyOnlyKeyPath(index int) string {
	return LayerRoot + "/" + verifyOnlyDir + "/" + strconv.Itoa(index) + ".pem"
}

const verifyOnlyDir = "verify-keys"

// layerAssets is what the configuration layer holds: the two documents and,
// one file each, the verify-only keys.
func layerAssets(docs map[string]string, keys []VerifyOnlyKeyArgs) map[string]any {
	out := map[string]any{
		"sluis/" + docSluis + ".yaml":  pulumi.NewStringAsset(docs[docSluis]),
		"sluis/" + docPolicy + ".yaml": pulumi.NewStringAsset(docs[docPolicy]),
	}
	for i, k := range keys {
		out["sluis/"+verifyOnlyDir+"/"+strconv.Itoa(i)+".pem"] = pulumi.NewStringAsset(strings.TrimSpace(k.PEM) + "\n")
	}
	return out
}

// verifyOnlyEntries is `signingKey.verifyOnly` as the service document says
// it, one entry per key, in order: the file the layer holds it at, its kid and
// alg when given, and its until in RFC 3339 (UTC).
func verifyOnlyEntries(keys []VerifyOnlyKeyArgs) []any {
	out := make([]any, 0, len(keys))
	for i, k := range keys {
		e := map[string]any{"file": VerifyOnlyKeyPath(i), "until": k.Until.UTC().Format(time.RFC3339)}
		if k.KeyID != "" {
			e["kid"] = k.KeyID
		}
		if k.Alg != "" {
			e["alg"] = k.Alg
		}
		out = append(out, e)
	}
	return out
}

// ownVerifyOnly writes `signingKey.verifyOnly` from LambdaArgs.VerifyOnly. The
// files a document names must be in the layer, and only the library puts them
// there, so a document that names another list is refused.
func ownVerifyOnly(doc map[string]any, a *LambdaArgs) error {
	want := verifyOnlyEntries(a.VerifyOnly)
	var got any
	if signing, ok := doc["signingKey"].(map[string]any); ok {
		got = signing["verifyOnly"]
	}
	named := got != nil && !reflect.DeepEqual(got, []any{})
	if len(want) == 0 {
		if named {
			return errors.New("sluispulumi: LambdaArgs.Config names signingKey.verifyOnly: on Lambda the keys are LambdaArgs.VerifyOnly, " +
				"which the library puts in the configuration layer and names in the document")
		}
		return nil
	}
	if named && !reflect.DeepEqual(got, want) {
		return errors.New("sluispulumi: LambdaArgs.Config: signingKey.verifyOnly is the library's when LambdaArgs.VerifyOnly is set: leave it out")
	}
	signing, err := child(doc, "Config", "signingKey")
	if err != nil {
		return err
	}
	signing["verifyOnly"] = want
	return nil
}

// withVerifyOnly is the installation with LambdaArgs.VerifyOnly as its
// `signingKey.verifyOnly`, so that the rendered document names the files the
// layer holds. An installation that names verify-only keys itself is refused:
// the files it names are not in the layer.
func withVerifyOnly(in *sluisconfig.Installation, keys []VerifyOnlyKeyArgs) error {
	if in.SigningKey != nil && len(in.SigningKey.VerifyOnly) > 0 {
		return errors.New("sluispulumi: LambdaArgs.Installation names signingKey.verifyOnly: on Lambda the keys are LambdaArgs.VerifyOnly, " +
			"which the library puts in the configuration layer and names in the document")
	}
	if len(keys) == 0 {
		return nil
	}
	k := sluisconfig.SigningKey{}
	if in.SigningKey != nil {
		k = *in.SigningKey
	}
	// The entry's type is the binary's own; it is filled through its JSON
	// form, which is the document's.
	raw, err := json.Marshal(verifyOnlyEntries(keys))
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &k.VerifyOnly); err != nil {
		return err
	}
	in.SigningKey = &k
	return nil
}

// checkVerifyOnly holds each verify-only key to what the function accepts at
// start (a layer outlives the deploy that published it): one public key, RSA
// or ECDSA, the algorithm it says, an until, and no key twice. An error names
// the entry and never the key material.
func checkVerifyOnly(keys []VerifyOnlyKeyArgs) error {
	seen := map[string]int{}
	for i, k := range keys {
		where := fmt.Sprintf("sluispulumi: LambdaArgs.VerifyOnly[%d]", i)
		if k.Until.IsZero() {
			return fmt.Errorf("%s.Until is required: a key published for good is not an overlap", where)
		}
		pub, err := verifyOnlyPublicKey(k.PEM)
		if err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		alg, err := verifyOnlyAlg(pub)
		if err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		if k.Alg != "" && k.Alg != alg {
			return fmt.Errorf("%s: the key is for %s and Alg says %s", where, alg, k.Alg)
		}
		der, err := x509.MarshalPKIXPublicKey(pub)
		if err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		sum := sha256.Sum256(der)
		for _, id := range []string{"key:" + hex.EncodeToString(sum[:]), "kid:" + k.KeyID} {
			if id == "kid:" {
				continue
			}
			if j, dup := seen[id]; dup {
				return fmt.Errorf("%s: names the same key or kid as VerifyOnly[%d]", where, j)
			}
			seen[id] = i
		}
	}
	return nil
}

// verifyOnlyPublicKey is the one public key a PEM holds. A private key is
// refused whatever its block says, as is anything that says PRIVATE.
func verifyOnlyPublicKey(text string) (any, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, errors.New("PEM is required")
	}
	if strings.Contains(strings.ToUpper(trimmed), "PRIVATE") {
		return nil, errors.New("PEM holds a private key: only the PUBLIC key is published (if a private key was ever put in a stack's configuration, rotate it)")
	}
	var found any
	rest := []byte(trimmed)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		var pub any
		var err error
		switch block.Type {
		case "PUBLIC KEY":
			pub, err = x509.ParsePKIXPublicKey(block.Bytes)
		case "RSA PUBLIC KEY":
			pub, err = x509.ParsePKCS1PublicKey(block.Bytes)
		case "CERTIFICATE":
			var cert *x509.Certificate
			if cert, err = x509.ParseCertificate(block.Bytes); err == nil {
				pub = cert.PublicKey
			}
		default:
			return nil, fmt.Errorf("PEM holds a %s block: only PUBLIC KEY, RSA PUBLIC KEY and CERTIFICATE are accepted", block.Type)
		}
		if err != nil {
			return nil, fmt.Errorf("PEM: a %s block is not a public key: %w", block.Type, err)
		}
		if found != nil {
			return nil, errors.New("PEM holds several keys: one key each")
		}
		found = pub
	}
	if found == nil || strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("PEM is not a PEM public key or certificate")
	}
	return found, nil
}

// verifyOnlyAlg is the algorithm a public key signs with, as the issuer
// publishes it.
func verifyOnlyAlg(pub any) (string, error) {
	switch k := pub.(type) {
	case *rsa.PublicKey:
		return "RS256", nil
	case *ecdsa.PublicKey:
		switch k.Curve {
		case elliptic.P256():
			return "ES256", nil
		case elliptic.P384():
			return "ES384", nil
		case elliptic.P521():
			return "ES512", nil
		}
	}
	return "", fmt.Errorf("the key is %T: a verify-only key is RSA, or ECDSA on P-256, P-384 or P-521", pub)
}
