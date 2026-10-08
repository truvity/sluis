package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config is what `login` writes and every other command reads.
type Config struct {
	// Issuer is the sluis issuer this laptop signs in at.
	Issuer string `yaml:"issuer"`
	// ClientID is the public client the flow runs as.
	ClientID string `yaml:"clientId"`
	// SSHKnownHosts is this laptop's own list of SSH host certificate
	// authorities to trust -- see ssh_known_hosts.go and
	// docs/reference/sluisctl.md#ssh-known-hosts. Nobody's estate names
	// ship in this binary; this list (or $SLUISCTL_SSH_KNOWN_HOSTS, read
	// the same way when this is empty) is the only thing that decides
	// what `sluisctl ssh known-hosts` ever writes.
	SSHKnownHosts []sshKnownHostsEntry `yaml:"sshKnownHosts,omitempty"`
}

// Session is the cached login: the refresh token, and enough about the
// person to answer `whoami` without a round trip.
type Session struct {
	RefreshToken string    `json:"refresh_token"`
	Subject      string    `json:"sub,omitempty"`
	Email        string    `json:"email,omitempty"`
	Expires      time.Time `json:"expires,omitempty"`
	// AccessToken is the issuer's own token from the last refresh, and
	// AccessExpires is when it stops being accepted. Kept so that the next
	// command can present it instead of SPENDING the refresh token for one
	// exactly like it -- see refresh() for why spending it needlessly ends
	// the session rather than merely costing a round trip.
	//
	// No new exposure: it lives in the file that already holds the refresh
	// token, under the same mode, and it is the weaker of the two -- short
	// lived, and it cannot mint its own successor.
	AccessToken   string    `json:"access_token,omitempty"`
	AccessExpires time.Time `json:"access_expires,omitempty"`
	// Issuer is the installation this session was minted at. The
	// filename is derived from the issuer for a human reading the
	// directory; THIS is what a read checks against, so a name that
	// collides fails closed as "not signed in" rather than opening a
	// session at the wrong estate.
	Issuer string `json:"issuer,omitempty"`
}

// configDir is where both live: <OS config dir>/sluisctl.
//
// Under the OS config directory rather than the home directory, so it
// sits beside every other tool's and is covered by whatever already
// backs that up or excludes it.
//
// The directory was called accessctl before the rename to sluis, and the
// deprecated accessctl name of the binary uses this same one. The first
// call that finds the new directory without state while the old one has
// some copies it across; see migrateLegacyConfigDir.
func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find the configuration directory: %w", err)
	}
	dir := filepath.Join(base, "sluisctl")
	legacyMigration.Do(func() {
		migrateLegacyConfigDir(filepath.Join(base, "accessctl"), dir, os.Stderr)
	})
	return dir, nil
}

// legacyMigratedMarker sits inside the migrated tree, so it lands with the
// rename. It records that a migration ran: a later sign-out that leaves the
// directory with only this file is not undone by copying the old state back.
// Deleting the whole <config>/sluisctl directory, marker included, re-runs the
// migration from the old directory.
const legacyMigratedMarker = ".migrated-from-accessctl"

// stagingPrefix names the private directories a migration builds in.
const stagingPrefix = ".sluisctl-migrate-"

var legacyMigration sync.Once

// migrateLegacyConfigDir copies the state in oldDir to newDir, once.
//
// It runs only when newDir holds nothing (absent or empty) and oldDir holds
// something. The copy is built in a private staging directory beside newDir
// and renamed into place, so no other process ever sees a half-built newDir,
// and a failed copy leaves nothing behind (the next run retries). A rename
// onto a non-empty newDir fails: another process won, and newDir is used as
// it is. Files are created 0600 and directories 0700. oldDir (which may be a
// symlink) is left as it is. Failure is not fatal: the tool starts without
// the old state, as a fresh laptop would, and says why on w.
func migrateLegacyConfigDir(oldDir, newDir string, w io.Writer) {
	if entries, err := os.ReadDir(newDir); err == nil && len(entries) > 0 {
		return
	}
	src, err := filepath.EvalSymlinks(oldDir)
	if err != nil {
		return
	}
	if entries, err := os.ReadDir(src); err != nil || len(entries) == 0 {
		return
	}
	fail := func(err error) {
		_, _ = fmt.Fprintf(w, "sluisctl: could not migrate %s to %s (will retry next run): %v\n", oldDir, newDir, err)
	}
	parent := filepath.Dir(newDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		fail(err)
		return
	}
	removeStaleStaging(parent, time.Hour)
	staging, err := os.MkdirTemp(parent, stagingPrefix+"*")
	if err != nil {
		fail(err)
		return
	}
	defer os.RemoveAll(staging) // a no-op once renamed away
	if err = os.Chmod(staging, 0o700); err != nil {
		fail(err)
		return
	}
	copied, err := copyTree(src, staging)
	if err != nil {
		fail(err)
		return
	}
	if copied == 0 {
		return
	}
	if err = os.WriteFile(filepath.Join(staging, legacyMigratedMarker), nil, 0o600); err != nil {
		fail(err)
		return
	}
	err = os.Rename(staging, newDir)
	if err != nil {
		if entries, rerr := os.ReadDir(newDir); rerr == nil {
			if len(entries) > 0 {
				return // another process won
			}
			// An empty newDir that rename refused: clear it and retry once.
			if rerr = os.Remove(newDir); rerr == nil {
				err = os.Rename(staging, newDir)
			}
		}
	}
	if err != nil {
		fail(err)
		return
	}
	_, _ = fmt.Fprintf(w, "sluisctl: copied its saved state from %s to %s (the old directory is left in place)\n", oldDir, newDir)
}

// removeStaleStaging deletes staging directories a crashed migration left in
// parent, once they are older than maxAge.
func removeStaleStaging(parent string, maxAge time.Duration) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), stagingPrefix) {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > maxAge {
			_ = os.RemoveAll(filepath.Join(parent, e.Name()))
		}
	}
}

// copyTree copies the regular files and directories under src into the
// private directory dst and returns how many files it copied. Anything else
// (a symlink, a socket) is skipped. The first error stops the copy.
func copyTree(src, dst string) (int, error) {
	copied := 0
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.Mkdir(target, 0o700)
		case d.Type().IsRegular():
			if err := copyFile(path, target); err != nil {
				return err
			}
			copied++
		}
		return nil
	})
	return copied, err
}

// copyFile copies src to the new file dst (mode 0600, fsynced). The source is
// Lstat-ed, opened, and Fstat-ed: it must be a regular file and the same file
// both times, so a symlink swapped in after the walk is refused.
func copyFile(src, dst string) error {
	before, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	after, err := in.Stat()
	if err != nil {
		return err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return fmt.Errorf("%s changed while it was being copied", src)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err = out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func configPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// legacySessionPath is the single-issuer cache this tool kept until the
// sessions moved per issuer. Read once, so nobody signs in again.
func legacySessionPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "session.json"), nil
}

// sessionPath is where ONE installation's login is cached.
//
// One file per issuer, rather than one file holding them all. Two
// reasons, and the second is the load-bearing one:
//
//   - A laptop belongs to more than one estate. With a single
//     session.json, signing in at the second issuer silently replaced the
//     first one's refresh token. `--issuer` then selected the right
//     endpoint and handed it the WRONG token, which the issuer refuses as
//     `subject_token is invalid` -- a message that reads as expiry and is
//     not, and which sent more than one person to re-run a login that had
//     already worked.
//   - More than one process writes here, as saveSession's own note says:
//     every kubectl, every provider of a Pulumi stack. Were the sessions
//     in one file, two exec plugins for DIFFERENT estates would rewrite
//     the same file and one would lose its token. Separate files make
//     that impossible rather than unlikely.
func sessionPath(issuer string) (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "sessions", sessionFileName(issuer)+".json"), nil
}

// sessionFileName renders an issuer as a filename: readable where it can
// be, hashed where it cannot.
//
// The digest is of the FULL issuer, so two installations differing only
// in something the mapping flattens -- a scheme, a port, a path -- still
// get their own file. Correctness does not rest on it either way: the
// issuer inside the file is what a read checks.
func sessionFileName(issuer string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(issuer), "/")

	bare := strings.TrimPrefix(strings.TrimPrefix(trimmed, "https://"), "http://")

	readable := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		default:
			return '_'
		}
	}, strings.ToLower(bare))

	const readableMax = 64
	if len(readable) > readableMax {
		readable = readable[:readableMax]
	}

	sum := sha256.Sum256([]byte(trimmed))

	return readable + "-" + hex.EncodeToString(sum[:4])
}

// readConfigFile reads config.yaml as it is, with no issuer required and
// no flag applied -- the raw half of loadConfig, split out for a caller
// that wants a section of this file (sshKnownHosts.go's own list) without
// needing a signed-in issuer at all. A file that does not exist yet reads
// as an empty Config, exactly as a fresh `login` would leave it.
func readConfigFile() (Config, error) {
	cfg := Config{}
	path, err := configPath()
	if err != nil {
		return cfg, err
	}
	raw, err := os.ReadFile(path) //nolint:gosec // a path this tool owns
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return cfg, fmt.Errorf("read %s: %w", path, err)
	default:
		if err = yaml.Unmarshal(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	return cfg, nil
}

// loadConfig reads what login wrote, with the flags overriding it.
//
// A flag wins so that one laptop can talk to a second installation
// without losing the first: `--issuer` is enough for a one-off, and
// nothing is written unless login is what was asked for.
func loadConfig(issuer, clientID string) (Config, error) {
	cfg, err := readConfigFile()
	if err != nil {
		return cfg, err
	}

	if issuer = strings.TrimSpace(issuer); issuer != "" {
		cfg.Issuer = issuer
	}
	if clientID = strings.TrimSpace(clientID); clientID != "" {
		cfg.ClientID = clientID
	}
	cfg.Issuer = strings.TrimSuffix(cfg.Issuer, "/")

	if cfg.Issuer == "" {
		return cfg, badUsage("no issuer: pass --issuer, or run `sluisctl login --issuer ...` once")
	}
	if cfg.ClientID == "" {
		cfg.ClientID = DefaultClientID
	}
	return cfg, nil
}

// DefaultClientID is what a laptop signs in as when nothing says
// otherwise. It has to be declared in the policy with a loopback
// redirect; the reference names the same string.
const DefaultClientID = "accessctl"

// saveConfig writes what login learned.
func saveConfig(cfg Config) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("render the configuration: %w", err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err = os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// loadSession reads the cached login.
func loadSession(issuer string) (Session, error) {
	issuer = strings.TrimSuffix(strings.TrimSpace(issuer), "/")

	path, err := sessionPath(issuer)
	if err != nil {
		return Session{}, err
	}

	session, err := readSession(path)
	if errors.Is(err, os.ErrNotExist) {
		// Nothing for this issuer yet. It may still be the one the
		// single-file cache held, from before sessions moved per
		// issuer.
		return adoptLegacySession(issuer)
	}

	if err != nil {
		return Session{}, err
	}

	// The file says which installation it belongs to, and that is what
	// decides -- not the name it was found under. A session from the
	// wrong estate is the failure this whole split exists to prevent,
	// and presenting it earns `subject_token is invalid` from the
	// issuer, which reads as expiry and sends people to re-run a login
	// that already worked.
	if session.Issuer != "" && session.Issuer != issuer {
		return Session{}, errNotSignedIn
	}

	return session, nil
}

// readSession reads one session file. A cache that cannot be parsed is a
// cache to replace, not an error to stop at: signing in again fixes it
// and nothing is lost.
func readSession(path string) (Session, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a path this tool owns
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Session{}, err
		}

		return Session{}, fmt.Errorf("read %s: %w", path, err)
	}

	var session Session
	if err = json.Unmarshal(raw, &session); err != nil {
		return Session{}, errNotSignedIn
	}

	if session.RefreshToken == "" {
		return Session{}, errNotSignedIn
	}

	return session, nil
}

// adoptLegacySession hands the old single-issuer cache to whoever asks
// for it first, so an upgrade costs nobody a login.
//
// It carries NO issuer of its own -- that is the defect being repaired --
// so there is no way to tell which installation it belongs to. Adopting
// it for the asker is therefore a guess, and a safe one: if the guess is
// wrong the issuer refuses the token exactly as it does today, and one
// `sluisctl login` replaces it with a file that says what it is.
//
// Deliberately NOT deleted or rewritten here. A read is a read, and more
// than one process reaches this line at once; the legacy file goes when
// the next login writes a proper one.
func adoptLegacySession(issuer string) (Session, error) {
	path, err := legacySessionPath()
	if err != nil {
		return Session{}, err
	}

	session, err := readSession(path)
	if errors.Is(err, os.ErrNotExist) {
		return Session{}, errNotSignedIn
	}

	if err != nil {
		return Session{}, err
	}

	session.Issuer = issuer

	return session, nil
}

// saveSession writes the cached login, readable only by this account.
//
// A file rather than the OS keyring, and deliberately: the keyring means
// a platform-specific dependency on every laptop and a prompt in the
// middle of a `kubectl` call on some of them. A refresh token in a
// 0600 file beside the configuration is the same secret the browser
// already keeps in a cookie jar, and `login` replaces it in one command
// if it leaks.
func saveSession(issuer string, session Session) error {
	issuer = strings.TrimSuffix(strings.TrimSpace(issuer), "/")
	session.Issuer = issuer

	path, err := sessionPath(issuer)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	body, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("render the session: %w", err)
	}

	// Written to a temporary file and renamed. A session half-written is a
	// session lost -- the refresh token is the only copy -- and more than
	// one process reaches this line: every kubectl, every provider of a
	// Pulumi stack. A truncated file would read as "not signed in", which
	// names neither the writer nor the moment.
	tmp, err := os.CreateTemp(dir, ".session-*.tmp")
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
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}
