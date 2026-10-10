package maintenance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/truvity/sluis/internal/port"
)

// The operator's side of the flag: `sluis maintenance on|off|status`. The
// restore function writes StateRestoring on every table itself; an operator
// writes StateMigration, with credentials the function roles do not hold (the
// Pulumi library's MaintenanceOperatorStatements).

// Table is one State that holds a flag, and the name it is shown by.
type Table struct {
	// Name is the module, or "all" for the one table of layout v4.
	Name  string
	State port.State
}

// Status is the flag of one table as read now. Err is set when it could not be
// read, in which case the table is neither on nor off.
type Status struct {
	Table string `json:"table"`
	Flag  Flag   `json:"flag"`
	On    bool   `json:"on"`
	Err   string `json:"error,omitempty"`
}

// Statuses reads the flag of every table, with no cache.
func Statuses(ctx context.Context, tables []Table) []Status {
	out := make([]Status, 0, len(tables))
	for _, t := range tables {
		s := Status{Table: t.Name}
		f, err := Read(ctx, t.State)
		if err != nil {
			s.Err = err.Error()
		} else {
			s.Flag, s.On = f, f.Active()
		}
		out = append(out, s)
	}
	return out
}

// ErrRestoring is a flag a restore holds, which `off` lifts only when forced.
var ErrRestoring = errors.New("maintenance: a restore holds the flag; lifting it mid-restore serves a half-written installation. " +
	"Run `sluis restore start --resume`, or pass --force when the restore is abandoned")

// Freeze sets StateMigration on every table. A table that is already frozen
// keeps its own Since, By and Reason, so that setting it twice is not a
// second freeze. A table a restore holds is not touched, and nothing is
// written to any table before every one has been read.
func Freeze(ctx context.Context, tables []Table, by, reason string, now time.Time) ([]Status, error) {
	if len(tables) == 0 {
		return nil, errors.New("maintenance: no table to set the flag in")
	}
	before := Statuses(ctx, tables)
	for _, s := range before {
		switch {
		case s.Err != "":
			return nil, fmt.Errorf("maintenance: read the flag of %s: %s", s.Table, s.Err)
		case s.Flag.Active() && !s.Flag.Frozen():
			return nil, fmt.Errorf("maintenance: %s is under %q (%s): %w", s.Table, s.Flag.State, s.Flag.Reason, ErrRestoring)
		}
	}
	for i, t := range tables {
		if before[i].On {
			continue
		}
		if err := Write(ctx, t.State, Flag{State: StateMigration, Since: now.UTC(), By: by, Reason: reason}); err != nil {
			return nil, fmt.Errorf("maintenance: set the flag of %s: %w", t.Name, err)
		}
	}
	after := Statuses(ctx, tables)
	for _, s := range after {
		if !s.On || !s.Flag.Frozen() {
			return nil, fmt.Errorf("maintenance: the flag of %s reads back as %q after the write", s.Table, s.Flag.State)
		}
	}
	return after, nil
}

// Lift clears the flag of every table. A flag a restore holds is lifted only
// with force. It reports how many tables carried a flag and whether a restore's
// was among them.
func Lift(ctx context.Context, tables []Table, force bool) (lifted int, forced bool, err error) {
	if len(tables) == 0 {
		return 0, false, errors.New("maintenance: no table to lift the flag in")
	}
	before := Statuses(ctx, tables)
	for _, s := range before {
		switch {
		case s.Err != "":
			return 0, false, fmt.Errorf("maintenance: read the flag of %s: %s", s.Table, s.Err)
		case s.Flag.Active() && !s.Flag.Frozen():
			if !force {
				return 0, false, fmt.Errorf("maintenance: %s is under %q: %w", s.Table, s.Flag.State, ErrRestoring)
			}
			forced = true
		}
	}
	for i, t := range tables {
		if !before[i].On {
			continue
		}
		if err := Clear(ctx, t.State); err != nil {
			return lifted, forced, fmt.Errorf("maintenance: lift the flag of %s: %w", t.Name, err)
		}
		lifted++
	}
	for _, s := range Statuses(ctx, tables) {
		if s.On {
			return lifted, forced, fmt.Errorf("maintenance: the flag of %s still reads %q after the delete", s.Table, s.Flag.State)
		}
	}
	return lifted, forced, nil
}
