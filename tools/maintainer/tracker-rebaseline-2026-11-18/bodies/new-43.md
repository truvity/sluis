## Scope

Decision K2 B (2026-11-18): the Kubernetes store is PostgreSQL through CloudNativePG; amends ADR 0005. `Get`, `Query`, `Transact` and `TailLog` against the store port conformance suite (#851). **TailLog is the hard part** (ordered change feed for watch without DynamoDB Streams: the design of the tail, retention and a reader that falls behind decide the size). No DynamoDB on Kubernetes. Links #1238 (its store half moves here; the key-source half stays Backlog; `aws-kms` stays, so the tested installation still needs AWS KMS).

## Decision codes

K2 B (maintainer decisions of 2026-11-18).

## Milestone

Core: kubernetes

Linear: mirrored.
