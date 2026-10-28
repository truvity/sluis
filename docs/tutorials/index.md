---
title: Tutorials
description: Step-by-step guides for installing and trying sluis v2. The first one installs sluis on a single-board computer with a YubiKey.
status: target
sidebar:
  order: 0
---

Each tutorial has a task to finish at the end: a running sluis you can sign in to, and a token you can verify.

The tutorials describe the target state. They show the experience sluis v2 is built to give, and they can't be run end to end yet. Names such as `sluis.example.com` are examples, and so are the commands and outputs marked as examples.

## Tutorials

| Tutorial | Shape | You finish with |
|---|---|---|
| [Install sluis on a single node](/tutorials/single-node-install/) | [T1 Single node](/shapes/single-node/) | One arm64 board with a YubiKey, signed in, with a token verified against the JWKS and the audit trail read |

## How the tutorials are kept true

The tutorials are part of the test suite. Each one lives under `tests/tutorials` in the repository and runs on all five shapes T1 to T5, so a step that stops working fails CI. Development uses [T0](/shapes/local/).

## Before you start

- Read the [overview](/overview/) for what sluis is and does.
- Pick the shape with the comparison on [Shapes](/shapes/).
- Check [Platforms](/distribution/platforms/) for the distributions and architectures you can use.
