// Command software is the software key driver of key@: the key-store port over P-384 keys held in files. It is
// for the local installation, for CI and for a smoke installation without cards, and it refuses production.
//
//	software serve --socket PATH --state-dir DIR --environment development|staging|production [--socket-gid GID]
//	software check --state-dir DIR
//	software version
package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"strconv"

	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/software"

	"github.com/truvity/sluis/v2/kits/go/cli"
	"github.com/truvity/sluis/v2/kits/go/registry"
	"github.com/truvity/sluis/v2/kits/go/rpc"
)

// This is the host of the software key driver: sluis's socket with peer credentials, the registry owner and the command line. The driver
// itself, its handler and its logic, is the library github.com/truvity/atoom/key/software; sluis turns its production floor on and declares
// its purposes: purpose.Reference(), the eleven of docs/architecture/keys.yaml, each with its algorithm and its operations.

// name is the registry name of the driver's user; its owner is key.
const name = "driver-key-software"

// owner returns the actor that owns the driver: key. The driver's socket serves nobody else.
func owner() string {
	o, _ := registry.DriverOwner(name)
	return o
}

// floor is the floor in code (ADR 0006): the driver does not run in production. It returns an error that exits 3 for production and a
// usage error for a value that is not an environment.
func floor(environment string) error {
	switch err := software.Floor(environment, true); {
	case errors.Is(err, software.ErrProduction):
		return cli.Refused(software.RefusedInProduction)
	case err != nil:
		return cli.Usage(errors.New("--environment must be development, staging or production"))
	}
	if environment == "" {
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

// runDriver applies the floor, opens the state directory and serves the key-store port on cfg until ctx ends. The floor comes first: in
// production nothing is opened and no key is read.
func runDriver(ctx context.Context, env, stateDir string, cfg rpc.ServerConfig) error {
	if err := floor(env); err != nil {
		return err
	}
	host := software.Host{
		HandlerOptions: rpc.HandlerOptions(cfg),
		Serve: func(ctx context.Context, path string, h http.Handler) error {
			return rpc.Serve(ctx, cfg, rpc.Handler{Path: path, H: h})
		},
	}
	err := software.Run(ctx, software.Options{Environment: env, StateDir: stateDir, Purposes: purpose.Reference(), RefuseProduction: true, Caller: peerName}, host)
	var pe *software.Problems
	if errors.As(err, &pe) {
		return cli.Refused(pe.Problems...)
	}
	return err
}

func main() {
	cli.Main("software", cli.Commands{Serve: serve, Check: check}, flags)
}

func flags(fs *flag.FlagSet) {
	fs.String("state-dir", "", "the directory of the key files (mode 0700, owned by the driver's user)")
	fs.String("environment", "", "development, staging or production (required; production is refused)")
	fs.Uint("socket-gid", 0, "open the socket (mode 0660) to this group, which the owner joins; 0 keeps mode 0600 for the driver's user alone")
}

func value(f cli.Flags, name string) string {
	if fl := f.Extra.Lookup(name); fl != nil {
		return fl.Value.String()
	}
	return ""
}

func serve(ctx context.Context, f cli.Flags) error {
	env := value(f, "environment")
	if env == "" {
		return cli.Usage(errors.New("--environment is required"))
	}
	// The floor comes before anything is opened: no socket, no key.
	if err := floor(env); err != nil {
		return err
	}
	if f.Socket == "" {
		return cli.Usage(errors.New("--socket is required"))
	}
	dir := value(f, "state-dir")
	if dir == "" {
		return cli.Usage(errors.New("--state-dir is required"))
	}
	cfg := rpc.ServerConfig{
		Actor:    name,
		Boundary: f.Boundary,
		Socket:   f.Socket,
		Mode:     0o600,
		Allow:    rpc.OwnerOnly(owner()),
	}
	cfg = withTestCaller(cfg)
	// The owner is another uid: a group that only the owner joins may connect. OwnerOnly still decides who may call.
	if gid := value(f, "socket-gid"); gid != "0" && gid != "" {
		n, err := strconv.ParseUint(gid, 10, 32)
		if err != nil {
			return cli.Usage(errors.New("--socket-gid is not a group number"))
		}
		cfg.Mode, cfg.GID = 0o660, uint32(n)
	}
	return runDriver(ctx, env, dir, cfg)
}

func check(_ context.Context, f cli.Flags) error {
	dir := value(f, "state-dir")
	if dir == "" {
		return cli.Usage(errors.New("--state-dir is required"))
	}
	// An environment given to check is held to the same floor as serve: a check must not pass what serve refuses.
	if env := value(f, "environment"); env != "" {
		if err := floor(env); err != nil {
			return err
		}
	}
	if err := software.Check(dir, purpose.Reference()); err != nil {
		var pe *software.Problems
		if errors.As(err, &pe) {
			return cli.Refused(pe.Problems...)
		}
		return err
	}
	return nil
}
