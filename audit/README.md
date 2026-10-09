# audit

audit is a tamper-evident audit trail an application owns: one record format, one write path, an immutable archive, and
projections for security, billing and history. An application emits records through a library; a writer puts them in an
Object-Locked S3 store, an indexer follows the store into PostgreSQL for search, a notary seals each hour, and
`audit verify` lets an auditor check the archive with read access to the store and nothing else.

audit is one of two products in the [sluis repository](../README.md), released together at one version. This directory
holds its Go module; its documentation is in [`docs/audit/`](../docs/concepts/audit/README.md).

- What it is and why: [why audit](../docs/concepts/audit/why.md) and [concepts](../docs/concepts/audit/concepts.md).
- Install it: [on Kubernetes](../docs/get-started/audit/kubernetes.md) or
  [on AWS Lambda](../docs/get-started/audit/aws-lambda.md); [deployment shapes](../docs/concepts/audit/deployment-shapes.md).
- What is published and where: [artifacts](../docs/reference/sluis/artifacts.md).
- Status: **stabilizing**; what is built, designed and run live is in
  [capabilities](../docs/reference/audit/capabilities.md). A minor release may carry a breaking change with a
  **Breaking:** entry in the [CHANGELOG](../CHANGELOG.md) and a migration page; a patch never does.
- Working on it: [CONTRIBUTING](../CONTRIBUTING.md#audit) and the
  [repository layout](../docs/reference/audit/repository-layout.md). Security: [SECURITY](../SECURITY.md).

Licence: MIT, see [LICENSE](LICENSE).
