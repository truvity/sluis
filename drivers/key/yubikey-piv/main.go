// Command yubikey-piv is the card key driver of key@: the key-store port over the keys of a board's YubiKey, through
// piv-go and pcsc-lite (ADR 0006, 0012, 0013). It holds the card exclusively and starts locked: it has no PIN until
// an administrator delivers it on the local admin socket with `sluis node unlock`.
//
//	yubikey-piv serve --socket PATH --admin-socket PATH --node NAME --environment development|staging|production
//	              --bootstrap PATH --state-dir PATH [--serial N] [--socket-gid GID]
//	yubikey-piv check [--serial N]
//	yubikey-piv conformance --socket PATH
//	yubikey-piv version
//
// The executable that talks to a card is built natively with the tag atoom_card and CGO_ENABLED=1 (`just
// build-arm64-native`). A build without the tag has the same commands and refuses to open a card.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"

	keykit "github.com/truvity/atoom/key/conformance"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/yubikeypiv"

	"github.com/truvity/sluis/v2/kits/go/cli"
	"github.com/truvity/sluis/v2/kits/go/registry"
	"github.com/truvity/sluis/v2/kits/go/rpc"
	"github.com/truvity/sluis/v2/kits/go/signedconfig"
)

// This is the host of the card key driver: sluis's socket with peer credentials, the registry owner, the command line and the check of
// the signed KeyPlan of a PIN release (keyplan.go). The driver itself, its handler and its logic, is the library
// github.com/truvity/atoom/key/yubikeypiv; the executable that talks to a card is built natively with the tag atoom_card.

// name is the registry name of the driver's user; its owner is key.
const name = "driver-key-yubikey-piv"

// owner returns the actor that owns the driver: key. The driver's socket serves nobody else.
func owner() string {
	o, _ := registry.DriverOwner(name)
	return o
}

// floor checks the environment: a usage error for a value that is not one. This driver runs in every environment.
func floor(environment string) error {
	if err := yubikeypiv.Floor(environment); err != nil {
		return cli.Usage(errors.New("--environment must be development, staging or production"))
	}
	return nil
}

// peerName names the caller of a call for the journal.
func peerName(ctx context.Context) string {
	if p, ok := rpc.PeerFromContext(ctx); ok {
		return p.Name
	}
	return ""
}

// keyServerConfig is the socket of the key-store service: mode 0600 for the driver's user alone, and only the owner may call.
func keyServerConfig(boundary, socket string) rpc.ServerConfig {
	return rpc.ServerConfig{
		Actor:    name,
		Boundary: boundary,
		Socket:   socket,
		Mode:     0o600,
		Allow:    rpc.OwnerOnly(owner()),
	}
}

// refused turns what the library refuses into the exit status of a refusal; a missing serial is a usage error.
func refused(err error) error {
	var pe *yubikeypiv.Problems
	switch {
	case errors.As(err, &pe):
		return cli.Refused(pe.List...)
	case errors.Is(err, yubikeypiv.ErrSerial):
		return cli.Usage(errors.New("--serial is required outside development: the driver is bound to one card"))
	case errors.Is(err, yubikeypiv.ErrEnvironment):
		return cli.Usage(err)
	}
	return err
}

func main() {
	cli.Main("yubikey-piv", cli.Commands{Serve: serve, Check: check, Conformance: conformance}, flags)
}

func flags(fs *flag.FlagSet) {
	fs.String("environment", "", "development, staging or production (required); only development makes a key with the factory management key")
	fs.String("node", "", "the node whose card this is (required for serve)")
	fs.String("admin-socket", "", "the absolute path of the local admin socket that takes the PIN (required for serve); root only")
	fs.Uint("serial", 0, "the serial number of the card the driver is bound to; required outside development (in development, none means the only card, and two cards are refused)")
	fs.String("bootstrap", "", "the bootstrap file of the boundary (required for serve): its publisher keys verify the KeyPlan of a PIN release, as key@'s do")
	fs.String("state-dir", "", "the driver's own state directory, mode 0700 (required for serve): it holds the highest KeyPlan seq verified")
	fs.Uint("socket-gid", 0, "open the socket (mode 0660) to this group, which the owner joins; 0 keeps mode 0600 for the driver's user alone")
}

func value(f cli.Flags, name string) string {
	if fl := f.Extra.Lookup(name); fl != nil {
		return fl.Value.String()
	}
	return ""
}

func serial(f cli.Flags) (uint32, error) {
	s := value(f, "serial")
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, cli.Usage(errors.New("--serial is not a serial number"))
	}
	return uint32(n), nil
}

func serve(ctx context.Context, f cli.Flags) error {
	env := value(f, "environment")
	if env == "" {
		return cli.Usage(errors.New("--environment is required"))
	}
	// The environment is checked before anything is opened: no socket, no card.
	if err := floor(env); err != nil {
		return err
	}
	if f.Socket == "" {
		return cli.Usage(errors.New("--socket is required"))
	}
	adminSocket, node := value(f, "admin-socket"), value(f, "node")
	if adminSocket == "" {
		return cli.Usage(errors.New("--admin-socket is required"))
	}
	if node == "" {
		return cli.Usage(errors.New("--node is required"))
	}
	// The PIN release is verified by the driver itself against the publisher keys key@ pins (ADR 0006, "Touch and PIN"),
	// with a mark of the highest KeyPlan seq in a state directory of its own.
	bootstrap, stateDir := value(f, "bootstrap"), value(f, "state-dir")
	if bootstrap == "" || stateDir == "" {
		return cli.Usage(errors.New("--bootstrap and --state-dir are required"))
	}
	raw, err := os.ReadFile(bootstrap) //nolint:gosec // the operator names the boundary's bootstrap file
	if err != nil {
		return errors.New("the bootstrap file cannot be read")
	}
	publishers, err := signedconfig.ParsePublishers(raw)
	if err != nil {
		return fmt.Errorf("the bootstrap file: %w", err)
	}
	sn, err := serial(f)
	if err != nil {
		return err
	}
	// The driver is bound to one card outside development, so that a genuine YubiKey that is not the board's cannot be
	// adopted because its attestations match the slot map.
	if env != yubikeypiv.Development && sn == 0 {
		return cli.Usage(errors.New("--serial is required outside development"))
	}
	cfg := keyServerConfig(f.Boundary, f.Socket)
	// The owner is another uid: a group that only the owner joins may connect. OwnerOnly still decides who may call.
	if gid := value(f, "socket-gid"); gid != "0" && gid != "" {
		n, err := strconv.ParseUint(gid, 10, 32)
		if err != nil {
			return cli.Usage(errors.New("--socket-gid is not a group number"))
		}
		cfg.Mode, cfg.GID = 0o660, uint32(n)
	}
	// The administrator of a node is root, reached through sudo: the socket is mode 0600 and the driver checks the
	// peer's uid again before it reads the PIN.
	admin := yubikeypiv.AdminConfig{Socket: adminSocket, Allow: func(uid uint32) bool { return uid == 0 }}
	// The KeyPlan of a PIN release is verified here, with the publisher keys of the bootstrap file and the mark in the state directory.
	peers, err := newPlanKeys(publishers, stateDir)
	if err != nil {
		return err
	}
	host := yubikeypiv.Host{
		HandlerOptions: rpc.HandlerOptions(cfg),
		Serve: func(ctx context.Context, path string, h http.Handler) error {
			return rpc.Serve(ctx, cfg, rpc.Handler{Path: path, H: h})
		},
	}
	opts := yubikeypiv.Options{Environment: env, Node: node, Boundary: cfg.Boundary, Serial: sn, PeerKeys: peers, StateDir: stateDir, Caller: peerName}
	return refused(yubikeypiv.Run(ctx, opts, host, admin))
}

// check opens the card, applies the firmware floor and verifies the attestation of every key it holds. It
// writes nothing. The card is exclusive, so it cannot run beside a driver that serves.
func check(_ context.Context, f cli.Flags) error {
	env := value(f, "environment")
	if env == "" {
		env = yubikeypiv.Staging // check never makes a key; the environment only has to be a real one
	}
	sn, err := serial(f)
	if err != nil {
		return err
	}
	if env != yubikeypiv.Development && sn == 0 {
		return cli.Usage(errors.New("--serial is required outside development"))
	}
	return refused(yubikeypiv.Check(yubikeypiv.Options{Environment: env, Serial: sn}))
}

func conformance(ctx context.Context, f cli.Flags) error {
	if f.Socket == "" {
		return cli.Usage(errors.New("--socket is required"))
	}
	hc, url := rpc.Dial(f.Socket)
	c := keyv1alpha1connect.NewKeyStoreServiceClient(hc, url, rpc.ClientOptions()...)
	if err := keykit.Run(ctx, c, os.Stdout); err != nil {
		return cli.Refused(err.Error())
	}
	return nil
}
