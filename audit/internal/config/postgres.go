package config

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/truvity/cnpg/v2/clients/go/pgclient"
)

// PoolOptions is what a caller adds to a database URL.
type PoolOptions struct {
	// Password is a password already in hand, for a caller that has read it
	// from somewhere the pool cannot re-read.
	Password string
	// PasswordFile is a file the pool reads again for every new connection.
	// It wins over Password.
	PasswordFile string
	// MaxConnections, when positive, is the pool size.
	MaxConnections int
	// URLPassword admits a password in the URL's user information. A
	// configuration file never does (the schema refuses it); a command-line
	// flag, which has no `passwordSecret`, may.
	URLPassword bool
}

// urlParameters are the query parameters of a database URL that carry meaning
// here. Any other is refused by name: the pool is built from parts, and a
// parameter it would drop silently (host, hostaddr, options, passfile) is a
// connection that is not what the file says.
var urlParameters = map[string]bool{
	"sslmode": true, "sslrootcert": true, "sslcert": true, "sslkey": true,
	"application_name": true, "connect_timeout": true,
}

// ClientConfig is the pgclient configuration the database block describes, and
// the single place a URL is turned into one. The password is read from the
// secret the block names; when the installation's `secrets` source is `file`
// that is the file itself, which the pool reads again for every new
// connection, so a rotated Secret reaches the process within one connection
// lifetime. From any other source it is read once, here.
//
// The result is held to the contract of truvity/cnpg's clients: verify-full
// only, against the CA file the URL's `sslrootcert` (or PGSSLROOTCERT) names.
func (p Postgres) ClientConfig(ctx context.Context, secrets *Secrets) (pgclient.Config, error) {
	opts := PoolOptions{MaxConnections: p.MaxConnections}
	if p.PasswordSecret != "" {
		file, password, err := secrets.fileOrValue(ctx, "database.passwordSecret", p.PasswordSecret)
		if err != nil {
			return pgclient.Config{}, err
		}
		opts.PasswordFile, opts.Password = file, password
	}
	return ClientConfigFromURL(p.URL, opts)
}

// Pool opens the pool the database block describes, proving the first
// connection under the client's retry.
func (p Postgres) Pool(ctx context.Context, secrets *Secrets) (*pgclient.Pool, error) {
	cfg, err := p.ClientConfig(ctx, secrets)
	if err != nil {
		return nil, err
	}
	return openPool(ctx, cfg)
}

// OpenPool opens a pool from a database URL given on a command line.
func OpenPool(ctx context.Context, rawURL string, opts PoolOptions) (*pgclient.Pool, error) {
	cfg, err := ClientConfigFromURL(rawURL, opts)
	if err != nil {
		return nil, err
	}
	return openPool(ctx, cfg)
}

func openPool(ctx context.Context, cfg pgclient.Config) (*pgclient.Pool, error) {
	return pgclient.New(ctx, cfg)
}

// ClientConfigFromURL turns a PostgreSQL URL into a pgclient configuration. The
// host, port, database and user come from the URL; the certificate files from
// its `sslrootcert`, `sslcert` and `sslkey`. What it does not say, the contract's
// environment (PGHOST, PGSSLROOTCERT, CNPG_CLIENT_POOL_MAX, ...) may; a password
// is the exception: PGPASSWORD and CNPG_CLIENT_PASSWORD_FILE are never read, so
// the only way in is the secret the block names.
//
// `sslmode` other than verify-full is refused, and so is a URL without a CA
// file anywhere.
func ClientConfigFromURL(rawURL string, opts PoolOptions) (pgclient.Config, error) {
	// url.Parse's error quotes the URL, which a command-line URL may carry a
	// password in: say where, not what.
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return pgclient.Config{}, errors.New("the database URL is not a postgres:// URL")
	}
	set := map[string]string{
		"PGHOST":     u.Hostname(),
		"PGPORT":     u.Port(),
		"PGDATABASE": strings.TrimPrefix(u.Path, "/"),
		"PGUSER":     u.User.Username(),
	}
	var problems []error
	query := u.Query()
	for name := range query {
		if !urlParameters[name] {
			problems = append(problems, fmt.Errorf("the database URL's parameter %q is not accepted", name))
		}
	}
	for param, env := range map[string]string{
		"sslmode": "PGSSLMODE", "sslrootcert": "PGSSLROOTCERT", "sslcert": "PGSSLCERT", "sslkey": "PGSSLKEY",
		"application_name": "PGAPPNAME", "connect_timeout": "PGCONNECT_TIMEOUT",
	} {
		if v := query.Get(param); v != "" {
			set[env] = v
		}
	}
	// Whatever the process's environment says about a password is ignored, and
	// so a password a URL or the caller gave is the one used.
	set["PGPASSWORD"], set["CNPG_CLIENT_PASSWORD_FILE"] = "", ""
	if password, ok := u.User.Password(); ok {
		if !opts.URLPassword {
			problems = append(problems, errors.New("the database URL carries a password: name a secret instead"))
		}
		set["PGPASSWORD"] = password
	}
	if opts.Password != "" {
		set["PGPASSWORD"] = opts.Password
	}
	if opts.PasswordFile != "" {
		set["CNPG_CLIENT_PASSWORD_FILE"] = opts.PasswordFile
	}
	if len(problems) > 0 {
		return pgclient.Config{}, errors.Join(problems...)
	}

	cfg, err := pgclient.FromEnv(func(name string) string {
		if v, ok := set[name]; ok {
			return v
		}
		return os.Getenv(name)
	})
	if err != nil {
		return pgclient.Config{}, err
	}
	if opts.MaxConnections > 0 {
		cfg.MaxConns = int32(opts.MaxConnections)
		if err := cfg.Validate(); err != nil {
			return pgclient.Config{}, err
		}
	}
	return cfg, nil
}

// fileOrValue is the secret `name` that `field` holds: its path, when the source
// is a directory of files (so that the pool reads it again for each connection),
// and its value otherwise. Either way the name is checked and the secret read
// once here, so a name that is wrong or a file that is empty fails at start.
func (s *Secrets) fileOrValue(ctx context.Context, field, name string) (file, value string, err error) {
	value, err = s.Get(ctx, field, name)
	if err != nil {
		return "", "", err
	}
	if s.Source() == SourceFile {
		return filepath.Join(s.src.Root, filepath.FromSlash(name)), "", nil
	}
	return "", value, nil
}
