//go:build sluis_test

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"go.yaml.in/yaml/v3"

	keykit "github.com/truvity/atoom/key/conformance"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"

	"github.com/truvity/sluis/v2/kits/go/cli"
	"github.com/truvity/sluis/v2/kits/go/rpc"
)

// The driver itself, its handler and its logic, is tested in atoom (github.com/truvity/atoom/key/software). What is sluis's is the host
// around it: the socket with its mode, the peer check that only the owner passes, the production floor that sluis turns on, and the way a
// refused state directory exits. These tests run the host over a real socket.

// asSelf names the uid of the test process: the registry's pinned uids do not apply to an ordinary user.
func asSelf(name string) rpc.NameResolver {
	self := uint32(os.Getuid()) //nolint:gosec // a uid is non-negative
	return func(uid uint32) (string, uint32, bool) { return name, 0, uid == self }
}

// shortDir returns a short directory: a Unix socket path holds about 108 bytes.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type running struct {
	client keyv1alpha1connect.KeyStoreServiceClient
	socket string
	state  string
}

// start serves the driver on a new socket and state directory as caller (the name the test process has for the driver's peers), and
// stops it at the end of the test.
func start(t *testing.T, environment, caller string) running {
	t.Helper()
	base := shortDir(t)
	r := running{socket: filepath.Join(base, "d.sock"), state: filepath.Join(base, "state")}
	cfg := rpc.ServerConfig{Actor: name, Boundary: "test", Socket: r.socket, Mode: 0o600, Allow: rpc.OwnerOnly(owner())}
	cfg = rpc.WithNameResolver(cfg, asSelf(caller))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runDriver(ctx, environment, r.state, cfg) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("run did not return")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("run returned early: %v", err)
		default:
		}
		if fi, err := os.Lstat(r.socket); err == nil && fi.Mode()&os.ModeSocket != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the socket did not appear")
		}
		time.Sleep(5 * time.Millisecond)
	}
	hc, url := rpc.Dial(r.socket)
	r.client = keyv1alpha1connect.NewKeyStoreServiceClient(hc, url, rpc.ClientOptions()...)
	return r
}

func TestOnlyTheOwnerMayCall(t *testing.T) {
	r := start(t, "development", "session")
	_, err := r.client.Capabilities(context.Background(), connect.NewRequest(&keyv1alpha1.CapabilitiesRequest{}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a caller named session: %v", err)
	}
	_, err = r.client.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "token-signing", Algorithm: "ES384"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("Ensure by a caller named session: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.state, "token-signing.p8")); err == nil {
		t.Fatal("a refused caller made a key")
	}
}

func TestSocketMode(t *testing.T) {
	r := start(t, "development", "key")
	fi, err := os.Stat(r.socket)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("err=%v mode=%v", err, fi.Mode())
	}
}

// The kit of atoom's key port passes against the host over its socket.
func TestConformancePassesAgainstItself(t *testing.T) {
	r := start(t, "development", "key")
	var out bytes.Buffer
	if err := keykit.Run(context.Background(), r.client, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "0 failed") {
		t.Fatalf("report: %s", out.String())
	}
}

// sluis turns the floor on: production serves nothing and opens nothing.
func TestProductionServesNothing(t *testing.T) {
	base := shortDir(t)
	cfg := rpc.ServerConfig{Actor: name, Socket: filepath.Join(base, "d.sock"), Mode: 0o600, Allow: rpc.OwnerOnly("key")}
	cfg = rpc.WithNameResolver(cfg, asSelf("key"))
	err := runDriver(context.Background(), "production", filepath.Join(base, "state"), cfg)
	if !errors.Is(err, cli.ErrRefused) {
		t.Fatalf("production: %v", err)
	}
	for _, p := range []string{"d.sock", "state"} {
		if _, err := os.Lstat(filepath.Join(base, p)); err == nil {
			t.Fatalf("%s was made", p)
		}
	}
}

func TestARefusedStateDirectoryExitsAsRefused(t *testing.T) {
	base := shortDir(t)
	state := filepath.Join(base, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0o755); err != nil { //nolint:gosec // the point of the test
		t.Fatal(err)
	}
	cfg := rpc.ServerConfig{Actor: name, Socket: filepath.Join(base, "d.sock"), Mode: 0o600, Allow: rpc.OwnerOnly("key")}
	cfg = rpc.WithNameResolver(cfg, asSelf("key"))
	err := runDriver(context.Background(), "development", state, cfg)
	if !errors.Is(err, cli.ErrRefused) || !strings.Contains(err.Error(), state) {
		t.Fatalf("0755 state directory: %v", err)
	}
	if _, err := os.Lstat(cfg.Socket); err == nil {
		t.Fatal("the socket was opened")
	}
}

// sluis holds intermediates and never a root (REQ-PKI-027): no purpose of docs/architecture/keys.yaml is a root. (The driver's half, that it
// makes no key for a root purpose and that the port has no operation that takes a private key out, is tested in atoom.)
//
// verifies: REQ-PKI-027
func TestTheRegistryHasNoRootPurpose(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/architecture/keys.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var keys struct {
		Purposes []struct {
			Name  string `yaml:"name"`
			Kind  string `yaml:"kind"`
			Notes string `yaml:"notes"`
		} `yaml:"purposes"`
	}
	if err := yaml.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys.Purposes) < 10 {
		t.Fatalf("%d purposes in keys.yaml: the scan checked nothing", len(keys.Purposes))
	}
	for _, p := range keys.Purposes {
		if strings.Contains(strings.ToLower(p.Name), "root") || strings.Contains(strings.ToLower(p.Kind), "root") {
			t.Errorf("purpose %q (kind %q) is a root", p.Name, p.Kind)
		}
	}
}
