---
title: "sluis: a pluggable identity and access broker"
description: sluis is an open-source identity and access broker for people, robots and external systems. v2 is being designed in the open.
status: target
template: splash
hero:
  tagline: A pluggable identity and access broker for people, robots and external systems.
  actions:
    - text: Start here
      link: /overview/
      icon: right-arrow
    - text: View on GitHub
      link: https://github.com/truvity/sluis
      icon: external
      variant: minimal
---

## What sluis is

sluis is a pluggable identity and access broker. It signs people in through the identity providers that already know them, exchanges the tokens of robots and CI jobs, and mints short-lived credentials for the systems they need to reach, with a tamper-evident audit trail of every success.

It hosts no user accounts and stores no passwords, it is not a general secrets engine, and it never writes Kubernetes objects: see [what sluis is, and is not](/principles/#what-sluis-is-and-is-not).

sluis holds intermediates only. Its roots belong to the sister product [ritueel](https://ritueel.id), which keeps them offline.

## Status

This site documents the **target state** of sluis v2. As of 2026-11-01, v2 is in the design phase and there is no v2 code yet. The design is recorded in 20 accepted [decision records](/decisions/). The current milestone is the first working version: see the [project status](/status/) and the [roadmap](/roadmap/). How to take part is in [Contributing](/contributing/).

v2 is a rewrite of the sluis that runs today; v1 docs are at [/v1/](/v1/).
