# drivers

A driver is an executable that belongs to one owner and is private to it (ADR 0013). It lives in `drivers/<owner>/<name>`, for example `drivers/store/rqlite`. A Rust driver has a `Cargo.toml` and a `src/main.rs`; a Go driver has a `main.go`.

The key drivers (`drivers/key/*`), the rqlite driver (`drivers/store/rqlite`) and the config and secret source drivers (`drivers/store/file`, `drivers/store/credential`) are hosts: the driver itself is a library of [atoom](https://github.com/truvity/atoom), pinned by commit (`third_party/atoom/README.md`), and the executable here is the socket with peer credentials, the registry owner check and the command line that sluis puts around it.

Cargo takes workspace members by glob (`drivers/*/*`). A Go driver directory must be listed in `exclude` in the root `Cargo.toml`, because Cargo fails on a matched directory that has no `Cargo.toml`.

Verified with `cargo metadata` (Cargo 1.99.1, 2026-11-04):

- A file matched by a glob is skipped, so `actors/README.md` and `drivers/README.md` do no harm.
- A matched directory without a `Cargo.toml` is an error, unless it is in `exclude`. A Go actor or driver directory is listed in `exclude` in the root `Cargo.toml` when its package adds it.
- A glob that matches nothing is an error. `drivers/store/README.md` is the file that `drivers/*/*` matches while no Rust driver exists; leave it in place.

