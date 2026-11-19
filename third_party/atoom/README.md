# atoom, vendored

[atoom](https://github.com/truvity/atoom) holds the ports, drivers and kits that sluis shares with session and ritueel. sluis's own contracts import atoom's messages (`proto/sluis/store`, `proto/sluis/port/item` import `atoom/common/v1alpha1/common.proto`), and sluis hosts the drivers of its key port (`atoom/key/v1alpha1`), so the proto files of atoom are copied here, unchanged, at the commit named in [PIN](PIN). Nothing here is edited by hand.

- `proto/`: the proto files of every atoom port, from `<port>/proto/atoom/` at the pinned commit. `buf` reads them as a dependency; they are not an output (`buf.gen.yaml` leaves them out), because the generated Go comes from atoom's own modules, `github.com/truvity/atoom/<port>`, which `go.mod` requires. The Go package of each file is set in `buf.gen.yaml` (managed mode) to that module's `gen` directory.
- `versions.yaml`: atoom's pinned runtimes at that commit. `just atoom-check` holds this repository's ConnectRPC, Protobuf and buffa pins to it, as the plan says: atoom bumps first, then the consumers.
- `PIN`: the source, the commit, and the sha256 of the files above.

The Rust side takes the crate `atoom-common` as a git dependency at the same commit (`[workspace.dependencies]` of the root `Cargo.toml`). `sluis-proto` also generates the vendored messages (`connectrpc-build` cannot point a message at another crate), so it holds a second, wire-identical copy of `Refusal` and `IndexEntry`; the helpers of the refusal detail come from `atoom-common`.

To move to another commit: `just atoom-sync <commit>` (40 hex digits; needs the network), then `just proto-gen`, and commit everything. `just atoom-check` (in `just check-rules`, offline) fails when the commit differs between `go.mod`, `Cargo.toml` and the PIN, or a vendored file changed. Until atoom has a release the pin is a commit; the first release (`v0.1.0`) replaces the commit by the tag in all four places.

- `testdata/slotplan-vectors.json`: the vectors of the slot map check of `github.com/truvity/atoom/key/slotplan`, which the Rust twin of that check (`actors/store/src/config/slotplan.rs`) reads, so that both implementations are held to one file. It is part of the PIN's digest.
- **Placeholder versions.** atoom's Go modules require each other at `v0.0.0` until its first release, and a version that has no tag cannot be looked up, so `go.mod` has one `replace` line for each, `<module> v0.0.0 => <module> <the pseudo-version of the pinned commit>`. `just atoom-sync` writes them and `just atoom-check` holds them to the commit; they go away with the move to the tag.
