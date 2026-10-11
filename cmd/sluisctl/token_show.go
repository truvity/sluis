package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// registered are the claims `token --show` names itself; the rest follow
// under `claims`.
var registered = map[string]bool{
	"iss": true, "sub": true, "aud": true, "exp": true, "iat": true, "groups": true,
}

// showClaims prints the claims of a token this process was just issued.
//
// The token is decoded, not verified: it came from the issuer in this very
// exchange, and the signature is neither read nor printed. Nothing of the
// compact token is written, so the output is safe to paste.
func showClaims(w io.Writer, compact, format string) error {
	parts := strings.Split(strings.TrimSpace(compact), ".")
	if len(parts) != 3 {
		return errors.New("the issued token is not a JWT, so it has no claims to show")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("the issued token's claims are not base64url: %w", err)
	}
	var all map[string]any
	if err := json.Unmarshal(raw, &all); err != nil {
		return fmt.Errorf("the issued token's claims are not JSON: %w", err)
	}

	out := map[string]any{}
	for _, key := range []string{"iss", "sub", "aud", "groups"} {
		if v, ok := all[key]; ok {
			out[key] = v
		}
	}
	for _, key := range []string{"exp", "iat"} {
		if seconds, ok := all[key].(float64); ok {
			out[key] = time.Unix(int64(seconds), 0).UTC().Format(time.RFC3339)
		}
	}
	extra := map[string]any{}
	for key, v := range all {
		if !registered[key] {
			extra[key] = v
		}
	}
	if len(extra) > 0 {
		out["claims"] = extra
	}

	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	// yaml.v3 sorts map keys, so the order is alphabetical and stable.
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(out); err != nil {
		return err
	}
	return enc.Close()
}
