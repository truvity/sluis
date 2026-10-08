package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/truvity/sluis/tokens"
)

// cloudflareGrant is a Cloudflare preset this identity may ask for, as
// `/.access/grants` lists it.
type cloudflareGrant struct {
	Preset      string `json:"preset"`
	Description string `json:"description,omitempty"`
	R2          bool   `json:"r2"`
	Endpoint    string `json:"endpoint,omitempty"`
	Lifetime    int64  `json:"lifetime_seconds"`
}

// cloudflareCommand is `sluisctl cloudflare token|r2 <preset>`: a Cloudflare
// credential minted for this identity by the issuer, from a preset's
// prototype. The proof is the same as for every other command: the job's own
// token in CI, the sign-in on a laptop.
func cloudflareCommand(args []string) error {
	if len(args) == 0 {
		return badUsage("cloudflare: name what you want: `token <preset>` or `r2 <preset>`")
	}
	switch args[0] {
	case "token":
		return cloudflareToken(args[1:])
	case "r2":
		return cloudflareR2(args[1:])
	default:
		return badUsage("cloudflare: %q is not a command: want `token` or `r2`", args[0])
	}
}

// cloudflareRequest is a cloudflare command line, read.
type cloudflareRequest struct {
	preset, issuer, client, format, file string
	lifetime                             time.Duration
}

// parseCloudflareFlags reads `<preset> [flags]` in either order.
func parseCloudflareFlags(name string, args []string, r2 bool) (cloudflareRequest, error) {
	var out cloudflareRequest
	flags := flag.NewFlagSet("cloudflare "+name, flag.ContinueOnError)
	flags.StringVar(&out.issuer, "issuer", "", "the issuer, when not configured")
	flags.StringVar(&out.client, "client", "", "the client to present")
	flags.DurationVar(&out.lifetime, "lifetime", 0, "how long the credential should live, at most the preset's (default: the preset's)")
	if r2 {
		flags.StringVar(&out.file, "file", "", "read the cloudflare/v1 document a secrets operator projected, and sign in to nothing")
	} else {
		flags.StringVar(&out.format, "format", "env", "env (CLOUDFLARE_API_TOKEN=...) or json")
	}
	// Go's flag package stops at the first argument that is not a flag, so the
	// preset is lifted out and the rest parsed again: both `token dns --format
	// json` and `token --format json dns` work.
	var positional []string
	rest := args
	for {
		if err := flags.Parse(rest); err != nil {
			return out, usageError{err}
		}
		if flags.NArg() == 0 {
			break
		}
		positional = append(positional, flags.Arg(0))
		rest = flags.Args()[1:]
	}
	if len(positional) != 1 {
		return out, badUsage("cloudflare %s: name exactly one preset", name)
	}
	out.preset = strings.TrimSpace(positional[0])
	if out.preset == "" || strings.ContainsAny(out.preset, ": /") {
		return out, badUsage("cloudflare %s: %q is not a preset name", name, out.preset)
	}
	if !r2 && out.format != "env" && out.format != "json" {
		return out, badUsage("--format %q: want env or json", out.format)
	}
	if out.lifetime < 0 {
		return out, badUsage("--lifetime must not be negative")
	}
	return out, nil
}

// cloudflareToken prints an API token of a preset.
func cloudflareToken(args []string) error {
	request, err := parseCloudflareFlags("token", args, false)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(request.issuer, request.client)
	if err != nil {
		return err
	}
	cred, err := cloudflareCredential(context.Background(), cfg, request.preset, request.lifetime)
	if err != nil {
		return err
	}
	if cred.R2() {
		return badUsage("preset %q hands out R2 credentials: use `sluisctl cloudflare r2 %s`", request.preset, request.preset)
	}
	if request.format == "json" {
		return json.NewEncoder(stdout).Encode(map[string]string{
			"token": cred.Token, "expires_on": cred.ExpiresOn.UTC().Format(time.RFC3339),
		})
	}
	_, _ = fmt.Fprintf(stdout, "CLOUDFLARE_API_TOKEN=%s\n", cred.Token)
	return nil
}

// cloudflareR2 prints R2 credentials of a preset as an AWS credential process
// answers: {"Version":1,"AccessKeyId","SecretAccessKey","Expiration"}.
func cloudflareR2(args []string) error {
	request, err := parseCloudflareFlags("r2", args, true)
	if err != nil {
		return err
	}
	var cred tokens.CloudflareCredential
	if request.file != "" {
		cred, err = readCloudflareFile(request.file)
	} else {
		var cfg Config
		if cfg, err = loadConfig(request.issuer, request.client); err == nil {
			cred, err = cloudflareCredential(context.Background(), cfg, request.preset, request.lifetime)
		}
	}
	if err != nil {
		return err
	}
	if !cred.R2() {
		return badUsage("preset %q hands out API tokens, not R2 credentials: use `sluisctl cloudflare token %s`", request.preset, request.preset)
	}
	return tokens.WriteCredentialProcess(stdout, tokens.Credentials{
		AccessKeyID: cred.AccessKeyID, SecretAccessKey: cred.SecretAccessKey, Expires: cred.ExpiresOn,
	})
}

// readCloudflareFile reads the cloudflare/v1 document a secrets operator
// projected into a pod: the credential sluis rotates on its schedule. It signs
// in to nothing, which is the point: the workload that has the file needs no
// identity of its own.
func readCloudflareFile(path string) (tokens.CloudflareCredential, error) {
	body, err := os.ReadFile(path) //nolint:gosec // the path the caller named
	if err != nil {
		return tokens.CloudflareCredential{}, fmt.Errorf("read %s: %w", path, err)
	}
	var doc struct {
		Schema string `json:"schema"`
		tokens.CloudflareCredential
		ExpiresOn string `json:"expires_on"`
	}
	if err = json.Unmarshal(body, &doc); err != nil {
		return tokens.CloudflareCredential{}, fmt.Errorf("%s is not a cloudflare/v1 document: %w", path, err)
	}
	if doc.Schema != "cloudflare/v1" {
		return tokens.CloudflareCredential{}, fmt.Errorf("%s is not a cloudflare/v1 document (schema %q)", path, doc.Schema)
	}
	cred := doc.CloudflareCredential
	if cred.ExpiresOn, err = time.Parse(time.RFC3339, doc.ExpiresOn); err != nil {
		return tokens.CloudflareCredential{}, fmt.Errorf("%s: expires_on %q is not RFC 3339", path, doc.ExpiresOn)
	}
	if !cred.ExpiresOn.After(time.Now()) {
		// A credential Cloudflare no longer accepts is not handed on: the SDK
		// would fail with a 403 that names neither this file nor its age.
		return tokens.CloudflareCredential{}, fmt.Errorf("%s: the credential expired at %s; is the secrets operator syncing it?", path, doc.ExpiresOn)
	}
	return cred, nil
}

// cloudflareCredential returns a credential of the preset, from the cache
// while a third of its lifetime is left, and minted by the issuer otherwise.
func cloudflareCredential(ctx context.Context, cfg Config, preset string, lifetime time.Duration) (tokens.CloudflareCredential, error) {
	audience := tokens.CloudflareAudiencePrefix + preset
	path, cacheErr := cloudflareCachePath(cfg.Issuer, cfg.ClientID, preset, lifetime)
	if cacheErr == nil {
		if cached, ok := readCloudflareCache(path, time.Now()); ok {
			return cached, nil
		}
	}
	mint := func() (tokens.CloudflareCredential, error) {
		// Re-read under the lock: a caller that waited finds the answer
		// written, which is the point of the lock.
		if cacheErr == nil {
			if cached, ok := readCloudflareCache(path, time.Now()); ok {
				return cached, nil
			}
		}
		held, err := proofFor(ctx, cfg, audience)
		if err != nil {
			return tokens.CloudflareCredential{}, err
		}
		exchanger := &tokens.Exchanger{Issuer: cfg.Issuer, ClientID: held.Client, Client: retryingClient()}
		started := time.Now()
		cred, err := exchanger.CloudflareToken(ctx, held.Subject, held.Type, preset, lifetime)
		switch {
		case errors.Is(err, tokens.ErrRefused):
			return tokens.CloudflareCredential{}, fmt.Errorf("%w: %w", errNotGranted, err)
		case err != nil:
			return tokens.CloudflareCredential{}, fmt.Errorf("%w: %w", errUnreachable, err)
		}
		if cacheErr == nil {
			// A cache that cannot be written is not a reason to withhold a
			// credential the caller already has.
			_ = writeCloudflareCache(path, cred, started)
		}
		return cred, nil
	}
	if cacheErr != nil {
		return mint()
	}
	var result tokens.CloudflareCredential
	err := withCacheLock(path, func() error {
		var mintErr error
		result, mintErr = mint()
		return mintErr
	})
	return result, err
}

// A cached credential: what the issuer minted, and for how long it was minted,
// so that the renewal point (a third of the lifetime left) can be found
// without asking the issuer.
type cloudflareCached struct {
	tokens.CloudflareCredential
	LifetimeSeconds int64 `json:"lifetime_seconds"`
}

// cloudflareCachePath names the cache file of one issuer, client, preset and
// asked-for lifetime. Hashed: an issuer is a URL.
func cloudflareCachePath(issuer, clientID, preset string, lifetime time.Duration) (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(issuer + "\x00" + clientID + "\x00" + preset + "\x00" + lifetime.String()))
	return filepath.Join(dir, "cloudflare", hex.EncodeToString(sum[:16])+".json"), nil
}

// readCloudflareCache returns a cached credential that still has more than a
// third of its lifetime left. Every failure is a miss.
func readCloudflareCache(path string, now time.Time) (tokens.CloudflareCredential, bool) {
	body, err := os.ReadFile(path) //nolint:gosec // our own cache file
	if err != nil {
		return tokens.CloudflareCredential{}, false
	}
	var c cloudflareCached
	if err = json.Unmarshal(body, &c); err != nil || (c.Token == "" && c.AccessKeyID == "") || c.ExpiresOn.IsZero() || c.LifetimeSeconds <= 0 {
		return tokens.CloudflareCredential{}, false
	}
	renewAt := c.ExpiresOn.Add(-time.Duration(c.LifetimeSeconds) * time.Second / 3)
	if !now.Before(renewAt) {
		return tokens.CloudflareCredential{}, false
	}
	return c.CloudflareCredential, true
}

// writeCloudflareCache stores a credential, atomically and readable only by
// this account. mintedAt is when the request was started: the lifetime is
// counted from it, a little short of the truth, which only renews early.
func writeCloudflareCache(path string, cred tokens.CloudflareCredential, mintedAt time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	body, err := json.Marshal(cloudflareCached{
		CloudflareCredential: cred, LifetimeSeconds: int64(cred.ExpiresOn.Sub(mintedAt).Seconds()),
	})
	if err != nil {
		return fmt.Errorf("render the credential: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cloudflare-*.tmp")
	if err != nil {
		return fmt.Errorf("create a temporary file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err = tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("restrict %s: %w", tmp.Name(), err)
	}
	if _, err = tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp.Name(), err)
	}
	return os.Rename(tmp.Name(), path)
}

// writeCloudflareGrants lists the presets for `whoami`.
func writeCloudflareGrants(w io.Writer, grants []cloudflareGrant) {
	if len(grants) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w, "\nCloudflare presets (sluisctl cloudflare token|r2 <preset>):")
	for _, g := range grants {
		kind, what := "token", g.Description
		if g.R2 {
			kind, what = "r2", strings.TrimSpace(g.Endpoint+"  "+g.Description)
		}
		_, _ = fmt.Fprintf(w, "  %-28s %-5s %s\n", g.Preset, kind, what)
	}
}

// r2AWSProfiles are the AWS profiles of the R2 presets this identity is
// granted, and their names. An R2 endpoint is not S3: path-style addressing,
// region `auto`, and checksums only where the API requires them, or R2 refuses
// the SDK's default.
func r2AWSProfiles(binary, issuer string, grants []cloudflareGrant) (string, []string) {
	var b strings.Builder
	var names []string
	for _, g := range grants {
		if !g.R2 || g.Endpoint == "" {
			continue
		}
		name := g.Preset + "@r2"
		names = append(names, name)
		_, _ = fmt.Fprintf(&b, "\n[profile %s]\n", name)
		_, _ = fmt.Fprintf(&b, "credential_process = %s cloudflare r2 %s --issuer %s\n", binary, g.Preset, issuer)
		_, _ = fmt.Fprintf(&b, "endpoint_url = %s\n", g.Endpoint)
		b.WriteString("region = auto\n")
		b.WriteString("request_checksum_calculation = when_required\n")
		b.WriteString("response_checksum_validation = when_required\n")
		b.WriteString("s3 =\n  addressing_style = path\n")
	}
	return b.String(), names
}
