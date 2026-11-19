# kits

The libraries every actor and driver is built on, one directory tree per language: `kits/go/<name>` in the Go module `github.com/truvity/sluis/v2`, and `kits/rust/<crate>` as members of the Cargo workspace (matched by glob, so a new crate edits no manifest at the root). `kits/conformance` holds the language-neutral test vectors that both languages must pass. A kit holds code that two or more executables share and nothing else. `kits/go/version` and `kits/rust/sluis-version` carry the one release version (ADR 0013).

The ports of the drivers (key, item, source, blob, transport, index, destination), their conformance kits and the driver libraries are not here: they live in [atoom](https://github.com/truvity/atoom) and sluis pins them by commit (`third_party/atoom/README.md`). `drivers/` holds only the executables that host them. `kits/conformance` keeps sluis's own vectors; the kits of the ports are atoom's, and `just conformance` runs them against sluis's hosts.
