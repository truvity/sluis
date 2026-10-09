//go:build lambda

package main

import (
	"errors"
	"io"
)

// The Lambda build carries no cluster or Valkey storage (docs/decisions/0071),
// and `migrate` copies between storages that include them: it is run from the
// Kubernetes build or from a workstation.
func migrateCmd(_ io.Writer, _ []string) error {
	return errors.New("sluis migrate: not part of the Lambda build; run it from the Kubernetes build")
}
