# 0071 — Least-privilege modules: one function per credential holder, one binary per function and platform

**Status:** Accepted (2026-10-09). Supersedes [0037](0037-one-process-everywhere.md) (one process everywhere); amends
[0036](0036-configuration-is-immutable-per-instance.md) (immutability moves from the instance to the execution
environment, and AWS configuration is delivered through AppConfig); builds on [0026](0026-two-platforms-permanently-kubernetes-and-aws-lambda.md),
[0030](0030-workload-identity-on-both-platforms.md), [0041](0041-the-secret-contract.md) and
[0042](0042-one-repository-one-release-train.md). The Cloudflare module is the one ADR 0070 (in review) decides.
**Date:** 2026-10-09

## Context

Since [0037](0037-one-process-everywhere.md) sluis runs as one process: one Lambda function with one IAM role, or one
Deployment with one ServiceAccount. The issuer, the console, the GitHub and Slack passes and the directory refresh share
that role, so a flaw in any of them holds every credential the others use: the signing key's wrapping key, the sign-in
secrets, each GitHub App's private key, each Slack token, the directory service-account keys. The wrapped signing key can
be decrypted by whatever runs with the role, and anyone who can write the State can publish a key into the key set.
[0037](0037-one-process-everywhere.md) chose one process to cut the operational cost of three roles, three documents and
three schedules; it accepted that "the controllers' code runs with the issuer's permissions".

Two things changed. A Cloudflare module (ADR 0070) holds
a parent credential as powerful as the person who created it, and cannot sit beside a service that parses browser
requests. And the operational cost 0037 avoided is now mostly generated: one Pulumi library renders every role, profile
and schedule from one declaration, and one chart renders every Deployment.

## Decision

1. **Eight modules, each the only holder of its credentials.**

   | Module | Holds | Does |
   |---|---|---|
   | `issuer` | sign-in state secret, client secrets | OIDC protocol front-end only: authorize, token, userinfo, logout, discovery, JWKS (public keys), sign-in pages and cookies. It parses requests and asks the signer for tokens |
   | `signer` | the signing key ring and its wrapping key | Verifies upstream signed proofs itself, takes groups from the source modules, issues, refreshes and revokes tokens, owns refresh and revocation state |
   | `console` | its session key | The SPA and the console API |
   | `github`, `slack` | the provider's App keys and tokens | The reconciler passes, the provider's connect callbacks, App installation tokens, group membership answers |
   | `google` | the Google credential | Answers directory lookups on demand behind an S3 snapshot cache |
   | `cloudflare` | the minter credential | The STS of ADR 0070 |
   | `backup` | read of everything, encrypted out | A scheduled job with no inbound API |

   The issuer holds no signing key, no KMS permission and no provider credential. The console holds no GitHub or Slack
   credential.

2. **One source module per provider.** `google`, `github` and `slack` are the only holders of their provider's
   credentials and the only answerers of "which groups does this subject hold" for that provider. `google` answers on
   demand: a request that misses the S3 snapshot cache, or finds the snapshot past a staleness bound, fetches, stores and
   answers; there is no refresh schedule and no refresh on the issuer's request path. The connect callbacks and
   `Begin*Connect` operations live in `github` and `slack`; the console links to them and reads status.

3. **The signer verifies proofs; the issuer fronts the protocol.** The signer verifies a Google ID token (signature,
   our nonce, a fresh `iat`), a GitHub OIDC token and an AWS web-identity token itself, and its own signed refresh
   tokens against a revocation list. It takes groups from the source modules and owns the absolute session limit,
   refresh rotation and reuse detection. Client-facing protocols, token shapes, discovery and JWKS do not change. A
   change in the refresh-token format is bridged by the signer accepting the previous format until those tokens
   expire, **at most 30 days**, after which that reader is removed. The signer imports a JOSE verifier and a bounded
   HTTP client for key sets, and no HTTP server, cookie, form or OIDC protocol packages.

4. **AWS: calls between modules are `lambda:InvokeFunction`, never a Function URL.** Each callee's resource policy names
   its callers; the callee learns which caller class invoked it from the alias it was invoked through (one alias per
   caller class) and from a web-identity token in the request. API Gateway has explicit routes only, no default route.
   **Kubernetes:** one Deployment, ServiceAccount and Service per module; a caller presents a projected
   ServiceAccount token whose audience is the callee, verified against the cluster's published key set. The chart
   renders a plain `NetworkPolicy` and a `CiliumNetworkPolicy` for each module, and supports EKS Pod Identity and IRSA
   for each ServiceAccount. A ConfigMap per Deployment; secrets arrive through External Secrets into only the
   Deployment that needs them. Each module's internal API is Connect services with two transports: Lambda invoke on AWS,
   HTTP on Kubernetes and in local development.

5. **Binaries per function and per platform, composed from shared packages.** `cmd/sluis-<module>-lambda` and
   `cmd/sluis-<module>-k8s` for each of the eight modules, plus `cmd/sluis-restore` and `cmd/sluisctl`. A main only
   composes its module with its platform's adapters (Lambda: DynamoDB, SSM, KMS, the invoke transport, AppConfig;
   Kubernetes: the cluster state adapters, OpenBao or SSM, the Service and projected-token transport, ConfigMaps,
   probes). There is no all-in-one binary. Import-boundary tests hold the line: a `-lambda` binary imports no
   client-go or Kubernetes-only adapter, a `-k8s` binary imports no Lambda-only adapter, and the signer imports no
   request-parsing package. Each test lists the binary's dependencies and fails on a forbidden prefix and on a sweep
   that finds too few packages, and the linter's depguard rules say the same per directory.

6. **Lambda binaries are plain HTTP servers behind the AWS Lambda Web Adapter.** Each function is a zip on
   `provided.al2023` with a zip layer **we build** from a pinned adapter release and checksum, not the adapter
   project's public layer. Non-HTTP events (scheduler ticks, module-to-module invokes) arrive through the adapter's
   pass-through path, `/events`, as a POST of the raw event. `/events` exists **only in the `-lambda` binaries**: its
   server is bound to the loopback address, API Gateway never routes it, the handler refuses a request whose context
   is an API Gateway request, and the right to invoke is IAM. It dispatches a scheduler tick (`{"kind": ...}`) to the
   module's `Tick(ctx, kind)` and an `rpc` envelope (deliberately not shaped like an API Gateway event) to the
   module's typed internal calls. The `-k8s` binaries have no `/events`: an in-process scheduler with leases calls
   `Tick`, and module calls arrive on an authenticated `/rpc` route (projected token and NetworkPolicy). The business
   logic is one set of functions per module; the two mains differ only in how they are reached.

7. **Configuration.** On AWS each function reads one **AppConfig** profile, rendered and deployed by Pulumi; only the
   deployment role may call `appconfig:StartDeployment`. The validator is a Lambda that runs sluis's real loader on the
   module's document (AppConfig's JSON Schema validators support draft 4 only; the schemas here are 2020-12). The
   AppConfig agent is the AWS-published extension layer. To keep the guarantee of [0036](0036-configuration-is-immutable-per-instance.md),
   a function reads its profile once at initialisation and exits after the response when it sees a new version, so a
   document is immutable per execution environment. Secrets stay in SSM or OpenBao through the `secrets` source; no
   secret is in a document. Telemetry: the OpenTelemetry Collector runs as a Lambda extension and the binaries export
   OTLP to the loopback address; the adapter owns `AWS_LAMBDA_EXEC_WRAPPER`.

8. **State, secrets, keys and blobs are per module.**
   - **One DynamoDB table per module**, on demand. A module reads another module's data by calling it, never by
     reading its table. The signer's table holds the key ring, sessions, refresh tokens and revocations. Most data
     start fresh in the release that carries this record (sessions, the ring, provider state, Cloudflare records);
     what must survive moves through backup and restore.
   - **Secrets layout v5.** `internal/<module>/{config,credentials}/...` is read and written only by that module;
     `external/<kind>/<id>` is written only by its owning module (`oidc` by the issuer, and `github`, `slack` and
     `cloudflare` by theirs) and granted to consumers by exact address as in [0041](0041-the-secret-contract.md).
     Estates still on layout v4 for the unified-release cutovers stay there until they move.
   - **One shared KMS key per estate**, because a key is billed per month and an alias is not. Each module has an
     alias `alias/sluis-<instance>-<module>-<purpose>`; the key port always passes the alias, also on decrypt. A
     module role's every KMS permission is conditioned on `kms:RequestAlias` and on the encryption context
     (`instance`, `purpose`, `module`, with `kms:EncryptionContextKeys` fixing the set of context keys); the key
     policy names only the module roles, an admin role and a break-glass role. SSM parameters are bound by
     `kms:ViaService` and the parameter ARN in the context, not by the alias. `kms:ResourceAliases` separates nothing
     on one key and is not used. An estate may re-point one alias at a dedicated key without a code change.
   - **Blobs:** one bucket with a prefix per module on AWS (object ARNs per prefix, `s3:prefix` on listing, a bucket
     policy that denies the other roles); a bucket per module on R2, whose credentials cannot be scoped to a prefix.
   - **Policy tests.** Every generated policy has a golden test and a deny-matrix test: module X is denied module Y's
     table, prefix, secret path and key alias, for every pair, generated from the module list.

9. **Backup and restore replace migration.** `sluis migrate` is not part of the end state. `backup` is a scheduled
   module (EventBridge on Lambda, a CronJob on Kubernetes) with read access to State, secrets and blobs, no inbound API,
   writing encrypted to an S3-compatible bucket under its own optional KMS or OpenBao key. `cmd/sluis-restore` is an
   operator tool that reads a backup back. The cutover tools shipped for the move to unified releases stay until that
   move is complete, then go.

10. **Schedules and rollout.** Schedules are written once in a neutral form (`every: 5m` or a five-field cron);
    Pulumi translates them to EventBridge Scheduler expressions (a role per schedule, a retry policy, a dead-letter
    queue) and the `-k8s` binaries give them to an in-process scheduler (gocron v2) that takes a lease per tick, so
    handlers are idempotent and replicas do not double-run. Ticks: signer every 5 minutes (key ring); github, slack and
    google every 15 minutes plus on demand (run-now, and refresh on a cache miss); cloudflare a third of the shortest
    preset rotation, clamped to 1 to 15 minutes; backup daily; issuer and console have none. A tick with nothing to do
    reads the State once, calls no KMS, writes no log line and counts a metric, because Lambda bills duration at 1 ms
    and bills initialisation. On AWS every function
    is served through a `live` alias that a CodeDeploy canary shifts (10% for 5 minutes, then 100%, rolled back on a
    CloudWatch alarm for errors or throttles; a window of 0 is all at once), and callers target the alias, never
    `$LATEST`. On Kubernetes a rolling update (`maxUnavailable: 0`, `maxSurge: 1`) with probes, configurable
    replicas, requests and limits per module, a PodDisruptionBudget (`maxUnavailable: 1` from two replicas) and
    topology spread. Per-module defaults for replicas, resources, Lambda memory, timeout and reserved concurrency are
    in the chart values and the Pulumi library, not in this record.

11. **Release.** Eight modules, each shipped as a native zip for Lambda and a dedicated multi-arch image for Kubernetes,
    plus the adapter layer zip and `sluisctl` and `sluis-restore` archives, each with an SBOM and a checksum in one
    `checksums.txt`, signed. Lambda functions receive only native zips and zip layers, never container images, so that
    the package digest an installation pins is the zip the function executes. Kubernetes receives one dedicated multi-arch
    image per binary; there is no all-in-one binary or image.

## Consequences

- A compromise of the issuer, the console, one provider module or the backup job no longer yields the signing key, the
  other providers' credentials or the ability to mint a token for a subject that cannot be proven to the signer. What
  the issuer can still do is deny service, mislead a browser and capture the codes and cookies it parses.
- The signer carries the policy engine, a JOSE verifier and a key-set client, and it becomes the single place that
  decides refresh and revocation. That is more code in the most privileged module and is the price of the property
  above; the import tests keep it from growing server-side packages.
- A sign-in costs more calls: the issuer to the signer, the signer to a source module (cached), and on AWS each is an
  invoke. The signer's latency is budgeted and measured before the issuer stops signing locally; the local signer stays
  behind a flag for one release.
- Session and refresh state moves from the issuer's records to the signer's. The format bridge is at most 30 days and
  then removed; during it the signer reads the previous records.
- A table per module makes the isolation a property of the table's ARN, not of a key condition; the cost is that a
  module needing another's data calls it. The release that carries this record starts most state fresh: everyone signs
  in again and the key set is republished, so relying parties' key-set caches must be allowed for in the cutover.
- One KMS key shared by all modules is separated by alias and encryption-context conditions, which hold for symmetric
  keys only; the remote-signing mode with asymmetric keys is outside this design.
- The Cloudflare minter credential, which Cloudflare does not bound by the creator's rights, lives only in the
  `cloudflare` module, with the refusals of ADR 0070
  enforced there.
- Release and documentation grow: eight modules, sixteen mains, per-module documents, AppConfig profiles and chart
  values. The Pulumi library renders them from one declaration, and a library and its binaries move together as before.
- A Lambda function carries up to three extensions (the adapter, the AppConfig agent and the Collector); their start-up
  cost is measured, not assumed.
- Breaking: the all-in-one binary, the unified Lambda function and the one-document layout go, in the release that
  carries this record ([0007](0007-breaking-changes-inside-1x.md) applies; an upgrade page ships with it).
- The Collector authenticates to a telemetry endpoint differently from the current layer, which exchanges the
  function role's web-identity token on demand; the replacement has to do the same, or the current layer stays as the
  exporter beside it. This is a follow-up, not a reason against.

## Alternatives considered

- **Keep one process (0037).** One role for every credential; rejected for the blast radius above.
- **In-process privilege separation.** On Lambda every goroutine and child process of an environment shares the role's
  credentials and memory, so it gives an accounting boundary and no enforcement.
- **One binary that selects its module by configuration.** Every module's code is present in every function, so a wrong
  or hostile document can enable code the role allows. Distinct binaries make the boundary a property of the file.
- **Function URLs.** Internet-reachable by construction. `lambda:InvokeFunction` is reachable only from the principals
  named and leaves a trail.
- **A signer that signs digests or claims the issuer built.** The signer could verify nothing about what it signs, and
  a compromised issuer could obtain any token. Rejected for verification inside the signer.
- **Refreshing the directory on a schedule inside the issuer.** Needs a provider credential in the issuer and fails
  when the schedule stalls. Replaced by on-demand lookups behind a cache in the source module.
- **The adapter project's public layer, or a container image for Lambda.** The first runs unchecked inside every
  function with its credentials; the second drops the pinned zip digest. Rejected for a layer we build and zips.
- **A draft-4 down-levelled schema as the AppConfig validator.** Drops the cross-field rules the loader enforces.
  Rejected for a validator that runs the loader itself.
- **Three functions as in v1.62.** Split by process role, not by credential, with three documents. Rejected by 0037 for
  its cost; the cost is now generated.
