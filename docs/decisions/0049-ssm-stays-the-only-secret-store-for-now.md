# 0049 — SSM stays the only secret store for now, and External Secrets reading sluis KV is a later option

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #798](https://github.com/truvity/sluis/issues/798).

## Context

[0047](/decisions/0047-kv-is-a-core-service-of-stores-that-no-tenant-owns/) designs KV, a core service of versioned static secrets. It also gives KV a job: External Secrets reads its secrets from sluis and not from a cloud parameter store, so that an installation on boards needs no cloud for its consumers, and the secrets that sluis generates for a consumer are written into KV instead of being exported.

The maintainer decided that the secret store in use stays as it is. AWS SSM is the only secret store for now, and External Secrets keeps reading it. Moving External Secrets onto sluis KV changes where every consumer reads its secrets, and it is weighed when sluis v2 has reached parity with v1.

## Decision

- **SSM stays the only secret store for now.** Nothing that a consumer reads moves from SSM to sluis. The roadmap holds no step that waits for it.
- **sluis KV serving External Secrets is a later option.** The maintainer decides at parity whether sluis KV replaces SSM for External Secrets. Until then the option is not planned work.
- **The KV product of [0047](/decisions/0047-kv-is-a-core-service-of-stores-that-no-tenant-owns/) stays as decided:** what KV is, the stores and paths, the policy rules, the storage and the data key per store, and the audit.
- **These parts of 0047 are the later option, and are not planned before the decision:** the Vault KV v2 subset that External Secrets reads (its section, the login by ServiceAccount token, and the External Secrets job in CI); the slice of workload exchange built for that login; and the section "Secrets that sluis generates live in KV", including the retirement of `ClientSecretExport`. The generated secrets that consumers read keep going through `exporter@` as they do today.
- **Wherever a record or a page says that KV replaces SSM for External Secrets,** read it as this later option. The statement that a board installation serves its consumers from KV without a cloud account holds only if the option is taken.

## Consequences

- An operator keeps one secret store to run, and sluis takes on no secret that consumers depend on before it has proved itself at parity.
- The core of the roadmap no longer includes the External Secrets facade, the slice of workload exchange for it, or the External Secrets job. What is left of KV in the core is the `kv` actor and `sluis kv`, and whether they are built before parity is open.
- The requirement rows of the facade and of the External Secrets job move to the backlog milestone, and keep their ids.

## Roads not taken

- **Move External Secrets to sluis KV now, as 0047 planned.** It ties the secrets of every consumer to a service that has not reached parity.
- **Drop KV from the design.** The product has uses besides External Secrets (staff reads through `sluis kv`, policy by path, a data key per store), and the option is kept open.
- **Decide a date for the option.** Parity has no date. The decision is taken when its condition is met.

## Follow-ups

- Open: whether the `kv` actor and `sluis kv` are built before parity, or wait for the first consumer that needs them ([#798](https://github.com/truvity/sluis/issues/798)).
- The decision at parity: whether External Secrets reads sluis KV ([#798](https://github.com/truvity/sluis/issues/798), [#1714](https://github.com/truvity/sluis/issues/1714)).
