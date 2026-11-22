// Command sluis-mimic is mimic, the test identity provider of development (ADR 0054): it stands in for a company's
// Google Workspace or Entra ID tenant and signs people and CI jobs in through the stable sluis. It is a test tool,
// never part of a product executable, and it refuses to start unless its config says `environment: devel`.
//
//	sluis-mimic serve -config mimic.yaml
//	sluis-mimic check -config mimic.yaml
//	sluis-mimic version
//
// The exit codes are 0 done, 1 failed and 2 usage.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/truvity/sluis/v2/kits/go/version"
	"github.com/truvity/sluis/v2/tools/mimic"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: sluis-mimic serve|check|version [-config file]")
		return 2
	}
	cmd := args[0]
	fs := flag.NewFlagSet("sluis-mimic "+cmd, flag.ContinueOnError)
	config := fs.String("config", "", "the config file")
	listen := fs.String("listen", "", "the address to listen on; overrides the config's listen")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return 2
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	switch cmd {
	case "version":
		fmt.Println("sluis-mimic", version.Version)
		return 0
	case "check", "serve":
	default:
		fmt.Fprintln(os.Stderr, "usage: sluis-mimic serve|check|version [-config file]")
		return 2
	}
	if *config == "" {
		fmt.Fprintln(os.Stderr, "sluis-mimic: -config is required")
		return 2
	}
	cfg, err := mimic.LoadConfig(*config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sluis-mimic:", err)
		return 1
	}
	srv, err := mimic.New(cfg, mimic.Options{Logger: log})
	if err != nil {
		fmt.Fprintln(os.Stderr, "sluis-mimic:", err)
		return 1
	}
	if cmd == "check" {
		fmt.Println("sluis-mimic: the config is good")
		return 0
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if err := serve(cfg.Listen, srv, log); err != nil {
		fmt.Fprintln(os.Stderr, "sluis-mimic:", err)
		return 1
	}
	return 0
}

func serve(addr string, h http.Handler, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	hs := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() { done <- hs.Serve(ln) }()
	log.Info("sluis-mimic: serving", "listen", ln.Addr().String())
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := hs.Shutdown(shut); err != nil {
		return err
	}
	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
