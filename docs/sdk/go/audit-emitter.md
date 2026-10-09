# Go audit emitter

```sh
go get github.com/truvity/sluis/audit/sdk@vX.Y.Z   # import github.com/truvity/sluis/audit/sdk/emit
```

The API is on [pkg.go.dev](https://pkg.go.dev/github.com/truvity/sluis/audit/sdk/emit). The walk-through is [emit records from Go](../../guides/audit/connect/emit-records.md). Server settings are in [configuration](../../reference/audit/configuration.md).

## Options

`emit.New(emit.Options{…})`:

| option | meaning |
|---|---|
| `Source`, `Catalogue` | the source this emitter speaks for, and its loaded catalogue; a record of an action the catalogue does not declare is refused |
| `Sink` | where records go: `sink.NewClient(httpClient, receiverURL)` for the receiver over Connect, with `auth.TokenFile` for the workload token. The application's sink is the receiver in its own namespace; the JetStream hop, in stream mode, is the receiver's, not the application's |
| `Timeout` | how long a `block` write may take. Default 10s |
| `Queue`, `Batch`, `Flush` | the `async` queue: how many records may wait (1024), how many are sent together (100), and how often (one second). A full queue drops the oldest, counts it and calls `OnDropped` |
| `Retry` | how long to wait before retrying a batch the sink refused. Default one second, doubling up to a minute |
| `Bounds` | size limits; default `record.Default` |
| `Version`, `Instance` | this process on every record; the writer replaces the observer's identity with the one it verified |
| `Hooks` | `OnDropped`, `OnFailed`, `OnWritten`, `OnRefused`: where a deployment counts and alerts |

There is no outbox option and no file. Decided in [0054](../../decisions/0054-two-deliveries-and-a-durable-ack.md).

## Middleware and registration

`emit.Middleware(trustedHops)` records each request's client address, user agent, request id and trace id on every record made while serving it. Set `trustedHops` to the number of your own proxies in front. A wrong value records a load balancer as the actor's address. `0` records the connection's peer.

`emit.Register(ctx, emit.Registration{…})` registers the catalogue with the receiver at start-up. Use the sink's address: the receiver serves `RegistryService`.

## Metrics

Alert on these two.

| metric | means |
|---|---|
| `audit.emit.queue.pending` | how many records are waiting to be acknowledged, and so what this process would lose if it stopped now. A number that only climbs is a receiver that has stopped acknowledging; drops follow. Published by `emit.InstrumentQueue` |
| `audit.emit.records.dropped` | records the queue overflowed and gave up on. Published by `emit.Instrument`. Unless the application wires its own `OnDropped`, the emitter also logs each one (`Options.Logger`, or `slog.Default()` under `emit.Instrument`). This is the incident; the one above is the alert |
