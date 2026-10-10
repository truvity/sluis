//go:build lambda

package main

import (
	"errors"
	"io"
)

// The operator command runs from a workstation or the Kubernetes build.
func maintenanceCmd(_ io.Writer, _ []string) error {
	return errors.New("sluis maintenance: not part of the Lambda build; run it from the Kubernetes build")
}
