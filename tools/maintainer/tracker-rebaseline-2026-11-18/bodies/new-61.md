## Scope

Policy by path for the KV add-on (ADR 0047, "Stores are boundary-wide, and a path is a hierarchy for policy").

- The policy schema gains version 2 with the entity types `KvGroup`, `KvStore in [KvGroup]` and `KvNode in [KvNode, KvStore]`, and the actions `kv.read`, `kv.list`, `kv.metadata.read`, `kv.write`, `kv.metadata.write`, `kv.delete`, `kv.undelete` and `kv.destroy`, all on a node.
- Every prefix of a path is a node, used only for policy. A rule on a node applies to everything below it. `kv.list` is asked on the folder.
- A store may name groups, so one rule covers several stores.
- A `Workload` gains `cluster`, `namespace`, `serviceaccount` and `tenant`. A workload's `tenant` is declared by the trust configuration for its issuer and never reported by the workload.
- Step-up is a rule on `auth_age` in policy, not a flag on a store.
- Done when a rule for one subtree lets a workload read that subtree and nothing else, a rule on a group covers its stores, a rule for staff requires a fresh sign-in on a named subtree, and each case has a test.

Decision: ADR 0047. Related: {{ref:60}}, {{ref:64}}.

## Decision codes

K3, K5 A (ADR 0047); decision A4 A, 2026-11-18.

## Milestone

Core: kv

Linear: mirrored.
