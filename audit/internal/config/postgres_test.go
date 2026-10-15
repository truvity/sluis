package config_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/audit/internal/config"
)

const verified = "postgres://audit@db-rw.example.test:5432/audit?sslmode=verify-full&sslrootcert=/etc/ca/ca.crt"

func TestTheClientIsBuiltFromTheURLsParts(t *testing.T) {
	cfg, err := config.ClientConfigFromURL(verified+"&sslcert=/c/tls.crt&sslkey=/c/tls.key", config.PoolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "db-rw.example.test" || cfg.Port != 5432 || cfg.Database != "audit" || cfg.User != "audit" ||
		cfg.SSLRootCert != "/etc/ca/ca.crt" || cfg.SSLCert != "/c/tls.crt" || cfg.SSLKey != "/c/tls.key" {
		t.Errorf("the URL's parts were not carried: %v", cfg)
	}
}

func TestAWeakOrMissingTLSSettingIsRefused(t *testing.T) {
	t.Setenv("PGSSLROOTCERT", "")
	for name, raw := range map[string]string{
		"sslmode=require":     "postgres://u@h:5432/d?sslmode=require&sslrootcert=/ca",
		"sslmode=disable":     "postgres://u@h:5432/d?sslmode=disable",
		"no CA file":          "postgres://u@h:5432/d?sslmode=verify-full",
		"no sslmode, no CA":   "postgres://u@h:5432/d",
		"a certificate alone": verified + "&sslcert=/c/tls.crt",
		"several hosts":       "postgres://u@a,b:5432/d?sslrootcert=/ca",
		"a passfile":          verified + "&passfile=/p",
		"a host parameter":    verified + "&host=elsewhere",
		"not a URL":           "mysql://u@h/d",
	} {
		if _, err := config.ClientConfigFromURL(raw, config.PoolOptions{}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	head, _, _ := strings.Cut(minimalQuery, "database")
	if _, err := config.LoadQuery(write(t, head+"database: {url: 'postgres://u@h/db?sslmode=require'}\n")); err == nil ||
		!strings.Contains(err.Error(), "verify-full") {
		t.Errorf("the loader accepted sslmode=require: %v", err)
	}
}

func TestTheCAMayComeFromTheEnvironmentTheChartExports(t *testing.T) {
	t.Setenv("PGSSLROOTCERT", "/etc/ca/ca.crt")
	cfg, err := config.ClientConfigFromURL("postgres://u@h:5432/d", config.PoolOptions{})
	if err != nil || cfg.SSLRootCert != "/etc/ca/ca.crt" {
		t.Errorf("PGSSLROOTCERT was not used: %v", err)
	}
}

func TestAPasswordInTheURLIsForTheCommandLineOnly(t *testing.T) {
	raw := "postgres://u:s3cret@h:5432/d?sslrootcert=/ca"
	_, err := config.ClientConfigFromURL(raw, config.PoolOptions{})
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("a password in a configuration's URL: %v", err)
	}
	cfg, err := config.ClientConfigFromURL(raw, config.PoolOptions{URLPassword: true})
	if err != nil || cfg.Password != "s3cret" {
		t.Errorf("a command-line URL's password was not carried: %v", err)
	}
	if strings.Contains(cfg.String(), "s3cret") {
		t.Error("the configuration prints the password")
	}
}

// With the file source the password is the file, read again for each connection.
func TestAFileSecretIsHandedOverAsAPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "database-password"), []byte("pw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := config.Postgres{URL: verified, PasswordSecret: "database-password", MaxConnections: 3}
	cfg, err := p.ClientConfig(context.Background(), config.NewSecrets(config.SecretsSource{Source: config.SourceFile, Root: dir}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PasswordFile != filepath.Join(dir, "database-password") || cfg.Password != "" || cfg.MaxConns != 3 {
		t.Errorf("the secret is not the pool's file: %v", cfg)
	}
	p.PasswordSecret = "absent"
	if _, err := p.ClientConfig(context.Background(), config.NewSecrets(config.SecretsSource{Source: config.SourceFile, Root: dir})); err == nil {
		t.Error("a missing password file was accepted at start")
	}
}
