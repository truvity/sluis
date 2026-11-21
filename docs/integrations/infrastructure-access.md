---
title: PostgreSQL and SSH
description: "How people and workloads open PostgreSQL sessions and SSH logins with short-lived certificates. Each has its own page."
status: target
sidebar:
  order: 8
  hidden: true
---

PostgreSQL and SSH are **client profiles**, not integrations: sluis calls nothing outside for them. They have no adapter, no connection and no credential of a vendor. The `ca` and `ssh-ca` actors of the PKI add-on sign short-lived certificates, each after `policy` sealed a grant for the request ([0023](/decisions/0023-certificate-authorities/#two-authorities-two-actors)); the database or the host decides what the certificate may do. Both actors hold no key: they ask key@ to sign in place.

## PostgreSQL

A short-lived X.509 client certificate whose subject names the database role and the requester. The page is [PostgreSQL](/integrations/postgresql/).

## SSH

A short-lived user or host certificate, with the authority held on the host and a break-glass CA that ritueel keeps offline. The page is [SSH](/integrations/ssh/).

### The break-glass SSH CA

Emergency access to hosts comes from an offline SSH CA that ritueel keeps, never from sluis; the details are on the [SSH page](/integrations/ssh/#the-break-glass-ssh-ca).
