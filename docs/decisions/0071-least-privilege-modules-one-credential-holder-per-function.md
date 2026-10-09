# 0071 — Least-privilege modules: one function per credential holder, the signer inside the issuer and a binary per module

**Status:** Accepted (2026-10-09); amended 2026-10-09 by [0072](0072-storage-layout-v5-module-first.md) (points 1, 3, 5, 8, 9 and 11: the signer and the console stay inside `sluis-issuer`; six binaries and seven functions; restore is a second deployment of the backup binary). Supersedes [0037](0037-one-process-everywhere.md) (one process everywhere); amends
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

1. **Each module is the only holder of its credentials.** *Amended by [0072](0072-storage-layout-v5-module-first.md):
   the signer and the console are not modules of their own; they stay inside `sluis-issuer`.*

   | Module | Holds | Does |
   |---|---|---|
   | `issuer` (with the signer and the console) | sign-in state secret, client secrets, the signing key ring and its wrapping key, the console session key | The OIDC protocol front-end, token issue, refresh and revocation, the SPA and the console API. Provider answers come from the source modules |
   | `github`, `slack` | the provider's App keys and tokens | The reconciler passes, the provider's connect callbacks, App installation tokens, group membership answers |
   | `google` | the Google credential | Answers directory lookups on demand behind an S3 snapshot cache |
   | `cloudflare` | the minter credential | The STS of ADR 0070 |
   | `backup` | read of everything, encrypted out | A scheduled job with no inbound API; deployed a second time as `restore`, which only an administrator or the break-glass role may invoke |

   The providers' credentials stay out of the issuer's role once their modules have their own functions (one per
   release, from v1.76); until then the issuer's function role is the union of the modules it hosts.

2. **One source module per provider.** `google`, `github` and `slack` are the only holders of their provider's
   credentials and the only answerers of "which groups does this subject hold" for that provider. `google` answers on
   demand: a request that misses the S3 snapshot cache, or finds the snapshot past a staleness bound, fetches, stores and
   answers; there is no refresh schedule and no refresh on the issuer's request path. The connect callbacks and
   `Begin*Connect` operations live in `github` and `slack`; the console links to them and reads status.

3. **The issuer verifies proofs itself.** Within `sluis-issuer` the signer code verifies a Google ID token (signature,
   our nonce, a fresh `iat`), a GitHub OIDC token and an AWS web-identity token, and its own signed refresh tokens
   against a revocation list. It takes groups from the source modules and owns the absolute session limit, refresh
   rotation and reuse detection. Client-facing protocols, token shapes, discovery and JWKS do not change. The signing
   code keeps its own package boundary (it imports a JOSE verifier and a bounded HTTP client for key sets, and no HTTP
   server, cookie, form or OIDC protocol packages), so that it can be split out later without a redesign. The split is
   not part of this record.

4. **AWS: calls between modules are `lambda:InvokeFunction`, never a Function URL.** Each callee's resource policy names
   its callers; the callee learns which caller class invoked it from the alias it was invoked through (one alias per
   caller class) and from a web-identity token in the request. API Gateway has explicit routes only, no default route.
   **Kubernetes:** one Deployment, ServiceAccount and Service per module; a caller presents a projected
   ServiceAccount token whose audience is the callee, verified against the cluster's published key set. The chart
   renders a plain `NetworkPolicy` and a `CiliumNetworkPolicy` for each module, and supports EKS Pod Identity and IRSA
   for each ServiceAccount. A ConfigMap per Deployment; secrets arrive through External Secrets into only the
   Deployment that needs them. Each module's internal API is Connect services with two transports: Lambda invoke on AWS,
   HTTP on Kubernetes and in local development.

5. **A binary per module, composed from shared packages.** *Amended by [0072](0072-storage-layout-v5-module-first.md).*
   There are six server binaries: `sluis-issuer` (issuer, signer and console), `sluis-cloudflare`, `sluis-github`,
   `sluis-slack`, `sluis-google` and `sluis-backup`, and the unchanged `cmd/sluisctl`. A binary's module is pinned when
   it is built, so no document can enable code its role allows. `cmd/sluis-signer` and `cmd/sluis-restore` are not
   built: restore is the backup binary deployed a second time. Each server main is built once per platform (build tag
   `lambda` and its negation; `k8s` is added with the release move) and composes its module with that platform's
   adapters (Lambda: DynamoDB, SSM, KMS, the invoke transport, AppConfig; Kubernetes: the cluster state adapters,
   OpenBao or SSM, the Service and projected-token transport, ConfigMaps, probes). Least privilege comes from the
   per-function role and configuration; the build adds a smaller file per function. Import-boundary tests hold the
   line. Module packages must not import each other's credential adapters (the `github` module cannot import the
   Slack adapter, the `google` module cannot import the Cloudflare minter, and so on); a Lambda build imports no
   client-go or Kubernetes-only adapter, a Kubernetes build imports no Lambda-only adapter, and the signing package
   imports no request-parsing package. Each test lists the binary's dependencies and fails on a forbidden prefix and
   on a sweep that finds too few packages, and the linter's depguard rules say the same per directory. Until a
   provider's function exists, the issuer's binary hosts that provider's module.

6. **Lambda builds are plain HTTP servers behind the AWS Lambda Web Adapter.** Each function is a zip on
   `provided.al2023` with a zip layer **we build** from a pinned adapter release and checksum, not the adapter
   project's public layer. Non-HTTP events (scheduler ticks, module-to-module invokes) arrive through the adapter's
   pass-through path, `/events`, as a POST of the raw event. `/events` exists **only in the Lambda builds**: its
   server is bound to the loopback address, API Gateway never routes it, the handler refuses a request whose context
   is an API Gateway request, and the right to invoke is IAM. It dispatches a scheduler tick (`{"kind": ...}`) to the
   module's `Tick(ctx, kind)` and an `rpc` envelope (deliberately not shaped like an API Gateway event) to the
   module's typed internal calls. The Kubernetes builds have no `/events`: an in-process scheduler with leases calls
   `Tick`, and module calls arrive on an authenticated `/rpc` route (projected token and NetworkPolicy). The business
   logic is one set of functions per module; the two platform builds differ only in how they are reached.

7. **Configuration.** On AWS each function reads one **AppConfig** profile, rendered and deployed by Pulumi; only the
   deployment role may call `appconfig:StartDeployment`. The validator is a Lambda that runs sluis's real loader on the
   module's document (AppConfig's JSON Schema validators support draft 4 only; the schemas here are 2020-12). The
   AppConfig agent is the AWS-published extension layer. To keep the guarantee of [0036](0036-configuration-is-immutable-per-instance.md),
   a function reads its profile once at initialisation and exits after the response when it sees a new version, so a
   document is immutable per execution environment. Secrets stay in SSM or OpenBao through the `secrets` source; no
   secret is in a document. Telemetry: the OpenTelemetry Collector runs as a Lambda extension and the binaries export
   OTLP to the loopback address; the adapter owns `AWS_LAMBDA_EXEC_WRAPPER`.

8. **State, secrets, keys and blobs are per module.**
   - **One DynamoDB table per module**, on demand. A module reads another module's data by calling it, or through a
     named read-only cross-grant (the issuer reads the `google`, `github` and `slack` tables). The issuer's table
     holds the key ring, sessions, refresh tokens and revocations. *Amended by 0072:* state is not started fresh;
     sessions, refresh tokens and the ring are copied by a one-time migration.
   - **Secrets layout v5.** `internal/<module>/<name>` is read and written only by that module, with no `config` or
     `credentials` level; `external/<module>/<name>` is written only by its owning module and granted to consumers by
     exact address as in [0041](0041-the-secret-contract.md). The names are fixed by
     [0072](0072-storage-layout-v5-module-first.md). v1.75 reads both layouts for one release.
   - **One shared KMS key per estate**, because a key is billed per month and an alias is not. Each module has an
     alias `alias/sluis-<instance>-<module>-<purpose>`; the key port always passes the alias, also on decrypt. A
     module role's every KMS permission is conditioned on `kms:RequestAlias` and on the encryption context
     (`instance`, `purpose`, `module`, with `kms:EncryptionContextKeys` fixing the set of context keys); the key
     policy names only the module roles, an admin role and a break-glass role. SSM parameters are bound by
     `kms:ViaService` and the parameter ARN in the context, not by the alias. `kms:ResourceAliases` separates nothing
     on one key and is not used. An estate may re-point one alias at a dedicated key without a code change. The
     signing context stays `{instance, purpose}` in v1.75 (0072); `module` joins it when the signer's key is split.
   - **Blobs:** one bucket with a prefix per module on AWS (object ARNs per prefix, `s3:prefix` on listing, a bucket
     policy that denies the other roles); a bucket per module on R2, whose credentials cannot be scoped to a prefix.
   - **Policy tests.** Every generated policy has a golden test and a deny-matrix test: module X is denied module Y's
     table, prefix, secret path and key alias, for every pair, generated from the module list.

9. **Backup and restore.** *Amended by [0072](0072-storage-layout-v5-module-first.md).* `backup` is a scheduled module
   (EventBridge on Lambda, a CronJob on Kubernetes) with read access to State, secrets and blobs, no inbound API,
   writing encrypted to an S3-compatible bucket under its own optional KMS or OpenBao key. Restore is a second
   deployment of the backup binary, selected by the function's configuration and enforced by its role (write access to
   everything); only an administrator or the break-glass role may invoke it. The console shows backups and has no
   restore button. `sluis migrate v5` ships for one release for the move to layout v5 and goes in v1.77 with layout v4.

10. **Schedules and rollout.** Schedules are written once in a neutral form (`every: 5m` or a five-field cron);
    Pulumi translates them to EventBridge Scheduler expressions (a role per schedule, a retry policy, a dead-letter
    queue) and the Kubernetes builds give them to an in-process scheduler (gocron v2) that takes a lease per tick, so
    handlers are idempotent and replicas do not double-run. Ticks: issuer every 5 minutes (key ring); github, slack and
    google every 15 minutes plus on demand (run-now, and refresh on a cache miss); cloudflare a third of the shortest
    preset rotation, clamped to 1 to 15 minutes; backup daily; the console has none. A tick with nothing to do
    reads the State once, calls no KMS, writes no log line and counts a metric, because Lambda bills duration at 1 ms
    and bills initialisation. On AWS every function
    is served through a `live` alias that a CodeDeploy canary shifts (10% for 5 minutes, then 100%, rolled back on a
    CloudWatch alarm for errors or throttles; a window of 0 is all at once), and callers target the alias, never
    `$LATEST`. On Kubernetes a rolling update (`maxUnavailable: 0`, `maxSurge: 1`) with probes, configurable
    replicas, requests and limits per module, a PodDisruptionBudget (`maxUnavailable: 1` from two replicas) and
    topology spread. Per-module defaults for replicas, resources, Lambda memory, timeout and reserved concurrency are
    in the chart values and the Pulumi library, not in this record.

11. **Release.** *Amended by [0072](0072-storage-layout-v5-module-first.md).* Six server binaries, each shipped as a native
    zip for Lambda and as one dedicated multi-arch image for Kubernetes, plus the `sluisctl` archive and the adapter
    layer zip, each with an SBOM and a checksum in one `checksums.txt`, signed. Lambda functions receive only native
    zips and zip layers, never container images, so that the package digest an installation pins is the zip the
    function executes. There are seven functions: the backup zip runs as both `sluis-backup` and `sluis-restore`.

## Consequences

- A compromise of one provider module or the backup job no longer yields the signing key or the other providers'
  credentials. The issuer's function still holds the signing key, the console and the issuer's secrets together
  (amended by 0072: the signer and the console stay inside it); what a compromise of it can do is bounded by its role,
  its own table and the import-boundary tests, not by a separate function.
- The signing code stays a package with a narrow import set, so a later split is a deployment change.
- Session and refresh state stay in the issuer's records; there is no format bridge.
- A table per module makes the isolation a property of the table's ARN, not of a key condition; the cost is that a
  module needing another's data calls it or holds a named read-only grant. State is copied by the one-time migration
  of [0072](0072-storage-layout-v5-module-first.md), so people stay signed in and the key set does not change.
- One KMS key shared by all modules is separated by alias and encryption-context conditions, which hold for symmetric
  keys only; the remote-signing mode with asymmetric keys is outside this design.
- The Cloudflare minter credential, which Cloudflare does not bound by the creator's rights, lives only in the
  `cloudflare` module, with the refusals of ADR 0070
  enforced there.
- Release and documentation grow: six module binaries, seven functions, per-module documents, AppConfig profiles and
  chart values. The Pulumi library renders them from one declaration, and a library and its binaries move together as
  before. The cost of a file per module is that the issuer's function carries code of the providers still hosted in it
  until they split (one per release from v1.76), so the protection there is the role, the document and the
  import-graph test.
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
- **One binary that selects its module by configuration.** A document could then enable code the role allows. Rejected
  for the multi-call binary above, where the subcommand fixes the module before any document is read and the role
  grants only that module's rights.
- **One binary per module (D95 c, superseded by D204 c).** Rejected at first for the release and signing work. Adopted
  by 0072 for the six module binaries, since the build pins the module and the cost is generated; the signer and the
  console are not given binaries of their own.
- **A separate signer function and binary (the first text of this record).** A smaller CVE surface and a function that
  other releases do not redeploy. Dropped by 0072: the extra function, the invoke on every sign-in and the second
  deployment cost more than they buy while the issuer's role is already narrow.
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
