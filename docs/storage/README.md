# storage

The Go module `github.com/truvity/sluis/storage` that sluis and audit share for two things: a versioned key-value
**state** store, and **keys** asked for by purpose. A product names a backend in the `state` and `keys` blocks of its
configuration and never imports one directly ([0041](../decisions/0041-the-secret-contract.md)).

storage is a library, not a service, so most sections are short and point at the product that runs it.

| Section | Page |
|---|---|
| getting started | [Use the OpenBao backends](how-to/use-the-openbao-backends.md) |
| architecture | [Architecture](architecture.md) |
| deployment | [Where the backends run](deployment.md) |
| operations | nothing is storage's alone: rotation and recovery are [sluis operations](../sluis/operations/README.md) and [audit operations](../audit/README.md) |
| how-to | [Use the OpenBao backends](how-to/use-the-openbao-backends.md) |
| reference | [The adapter block](reference/adapter-block.md) |
| explanation | [The adapter block](explanation/adapter-block.md) |

The contract is owned here: the adapter block (`keys`, `state`) is documented in this tree and referenced from sluis
and audit. The Go API is in the packages' own documentation (`storage/state`, `storage/keys`, `storage/doc.go`).
