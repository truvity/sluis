// Package config is sluis's configuration as a public, stable surface: the
// documents a process is started with, how to load and check them, and the
// installation document an estate writes once and renders them from.
//
// An estate knows one thing about an installation (where it runs, which AWS
// resources and which OpenBao it is built on, whom it trusts, what its clients
// are) and sluis needs two documents of it: the service document
// (`sluis.yaml`, apiVersion sluis.truvity.github.io/sluis/v3) and the policy
// document (`policy.yaml`, .../policy/v2). The installation
// (apiVersion sluis.truvity.github.io/installation/v1, [Installation]) is the
// estate's side of that line and [Render] is sluis's: the same function
// `sluisctl render` runs, and the one the Pulumi library
// (github.com/truvity/sluis/deploy/pulumi) calls, so a Lambda estate and a
// Kubernetes estate write the documents the same way and neither hand-renders
// them.
//
//	in, err := config.LoadInstallation("installation.yaml")
//	service, policy, err := config.Render(in)
//
// Render is deterministic (sorted keys, a fixed layout) and holds both outputs
// to the loader the service runs at start, so a document it returns is one the
// service accepts. Nothing here reads a secret: a document names each secret
// and the installation never carries a value.
//
// The documents' own types are the binary's, re-exported: a field of one is
// reachable by value, and the types are held to their schemas
// (schemas/config/) by the tests of this repository. A key this package does
// not name is refused by the schema, never dropped.
package config
