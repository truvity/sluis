//go:build !lambda

package main

// platformEntry is the Kubernetes build's: the command line is the only entry.
func platformEntry([]string, func(string) string) bool { return false }
