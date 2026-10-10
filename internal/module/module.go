// Package module is the contract of a role of the service: the one thing
// `sluis <module>` starts (docs/decisions/0071). A module is assembled from
// one configuration file, runs until its context ends, and may tick once for a
// target. The roles are issuer (which hosts the signer and the console in its
// process), github, slack, cloudflare, google and backup; each has its own
// subpackage once it has a process of its own, and [Unsplit] stands in until then.
//
// This package imports no role: a role's package imports it, and the
// import-boundary test (internal/boundaries) holds the roles apart.
package module

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/truvity/sluis/internal/telemetry"
)

// Module is one role of the service.
type Module interface {
	// Name is the subcommand: issuer, github, ...
	Name() string
	// Run assembles the module from file and serves until ctx ends.
	Run(ctx context.Context, file string) error
	// Tick assembles the module from file, does one unit of work for target
	// once, and returns. unsafeLocal opts in to ticking while the lease State
	// is this process's own memory. A module with no ticks returns [ErrNoTick].
	Tick(ctx context.Context, file, target string, unsafeLocal bool) error
}

var (
	// ErrNotSplit is the answer of a module with no process of its own yet.
	ErrNotSplit = errors.New("not yet split")
	// ErrNoTick is the answer of a module that has no tick.
	ErrNoTick = errors.New("has no tick")
)

// Unsplit is a module whose role still runs inside the issuer process.
type Unsplit string

// Name implements [Module].
func (u Unsplit) Name() string { return string(u) }

// Run implements [Module].
func (u Unsplit) Run(context.Context, string) error { return u.err() }

// Tick implements [Module].
func (u Unsplit) Tick(context.Context, string, string, bool) error { return u.err() }

func (u Unsplit) err() error {
	return fmt.Errorf("sluis %s: %w: this module has no process of its own yet; its role still runs inside the issuer process (`sluis issuer`)", u, ErrNotSplit)
}

// Logger builds the process's JSON logger at the level its file chose, and
// starts telemetry under service. The returned function flushes the last
// pass's metrics, bounded: never a hung stop.
//
// service is the name each of the three binaries reported before they were one
// (access-issuer, github-roster, slack-roster), so a dashboard or an alert that
// selects on it still finds the process.
func Logger(ctx context.Context, w interface{ Write([]byte) (int, error) }, service string, level slog.Level) (*slog.Logger, func(), error) {
	log := slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)
	shutdown, err := telemetry.Start(ctx, service, log)
	if err != nil {
		return nil, nil, err
	}
	return log, func() {
		flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(flush); err != nil {
			log.WarnContext(ctx, "metrics could not be flushed", slog.Any("error", err))
		}
	}, nil
}

// CloseEmitter closes a controller's audit emitter and says so when what its
// queue held is dropped.
func CloseEmitter(log *slog.Logger, c interface{ Close() error }) {
	if err := c.Close(); err != nil {
		log.WarnContext(context.Background(), "the audit emitter could not be closed cleanly; what its queue held is dropped", slog.Any("error", err))
	}
}
