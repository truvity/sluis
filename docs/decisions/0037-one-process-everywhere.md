# 0037 — One process everywhere: one Lambda function, one Deployment, one service document

**Status:** Accepted; refined by [0038](0038-estates-render-through-sluis.md); amends [0036](0036-configuration-is-immutable-per-instance.md) (three documents and three Deployments → one unified process)
**Date:** 2026-10-05

## Context

0036 established that configuration is immutable per instance and delivered separately
from code, with one service document per role (`serve`, `controller-github`, `controller-slack`)
and one Deployment per role on Kubernetes or three Lambda functions on AWS.

Operators found that three functions, three roles, three service documents, three
Deployments and three sets of IAM roles created operational overhead and fragmentation
without corresponding benefit:

- **Resources.** Each function, Deployment and role added to fleet management.
- **Configurations.** Changes to GitHub or Slack behaviour required editing and testing
  multiple documents; one document change risks inconsistency across roles.
- **IAM.** Per-role isolation meant the GitHub and Slack controller roles had no access
  to secrets they sometimes needed to read, and the serve role read configuration it did
  not use. Scattering permissions prevented a clean audit trail.
- **Operational model.** Three separate failure surfaces, three separate deployments,
  three schedules and monitoring rules.

The unified process removes the role boundaries inside the running instance while
keeping the isolation at the IAM layer, configuration validation and release signature
verification.

## Decision

1. **One process everywhere.**
   - **AWS Lambda:** Replace the three functions (`sluis-http`, `sluis-github`,
     `sluis-slack`) with one function, configured by one service document and one policy
     document, mounted as a layer just as before.
   - **Kubernetes:** Replace the three Deployments (`serve`, `controller-github`,
     `controller-slack`) with one Deployment, using the same configuration and policy
     documents.
   - The process detects its role from the environment (set by the cluster or function
     configuration) and does not require separate binaries.

2. **One service document.** The three service documents consolidate into a single
   `sluis.yaml` (apiVersion `sluis.truvity.github.io/sluis/v3`) with sections
   for each role:
   - `serve` — HTTP server configuration
   - `controllers.github` — GitHub event handling and reconciliation
   - `controllers.slack` — Slack event handling and reconciliation

   All other top-level configuration (clients, providers, directory, exports) applies
   to all roles, since they share the same process. The canonical `policy.yaml` remains
   unchanged from 0036.

3. **Unified IAM.** On Lambda, a single function role replaces three. The role carries
   all permissions needed by any role (GitHub/Slack controller code now has permission
   to read configuration secrets and decrypt signing keys). The boundary moves to
   configuration validation: invalid configuration is rejected at deploy time, not at
   runtime. Release-zip verification and config validation before publish remain in place.

4. **Kubernetes ConfigMaps.** The serve and controller roles no longer split
   configuration delivery; one set of ConfigMaps and Secrets delivers the entire
   configuration to the single Deployment. The `checksum/*` annotation mechanism
   (0036) still applies.

5. **Backward compatibility.** Documents written in v2 format (`serve.yaml`,
   `controller-github.yaml`, `controller-slack.yaml`) are loaded for one release
   (N-1); the converter fills in empty `controllers.*` sections and reads the three
   documents as if they were one. After one release, v2 documents are rejected.

## Consequences

- **Per-role IAM isolation removed.** The GitHub and Slack controller code runs with
  permission to read configuration secrets. This is mitigated by:
  - IAM scoped to `/sluis/<instance>/*` (0036, unchanged)
  - Configuration validation at deploy time
  - Release-zip signature verification (0036, unchanged)
  - No endpoint overrides possible from configuration
  
  This trade-off was accepted by the owner as worth the operational simplification.

- **Lambda timeout is shared.** A controller pass shares the function's timeout and
  memory with the HTTP server (timeout ~5 minutes; API Gateway still cuts HTTP
  requests at 30 seconds). Long-running controller operations cannot extend the
  timeout independently.

- **Kubernetes migration.** Estates update their Pulumi or Helm to deploy one
  Deployment instead of three; no data migration, no State reload.

- **Lambda migration.** Estates delete the `sluis-github` and `sluis-slack` function
  roles and resources; update Pulumi to deploy the single unified function. This is
  a control-plane change with no impact on running state.

- **Configuration rollback.** A policy or document change is still immutable until
  the next deployment (0036 unchanged); rollback is a redeployment, not a reload.

## Alternatives considered

**Keep the three functions but unify the binary.** Rejected: it still requires three
Lambda layers, three function configurations, three IAM roles and three reconciliation
schedules. The overhead is not reduced.

**Merge only on Kubernetes, keep three functions on Lambda.** Rejected: it splits
the operational model between platforms, requiring different migration procedures,
different configuration formats and different monitoring. The unified model is simpler.

**Unified process with per-role permission checks at runtime.** Rejected: adds code
complexity and still requires each role to validate that it is running the operations
it is permitted to perform. Static IAM isolation (0036) is simpler and the new
mitigations (config validation, no overrides) cover the risk.

