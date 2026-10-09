# Day one: the first sign-in of a standalone installation

## Purpose

Sign the first operator in to a standalone installation and finish its setup in the console.

## Preconditions

- The chart is installed and the Deployment is Ready.
- You are a cluster administrator, or somebody who may `create` on `serviceaccounts/token` for the recovery
  ServiceAccount (`config.recovery.serviceAccount`).
- A deployment whose values carry a workspace and a non-empty operators group has **no day one**: people sign in through
  the directory from the first boot. Go to [connect a Google Workspace](../connect/google-workspace.md) when you add the
  next tenant.

## Before you start

- **The redirect URI is the installation's own hostname, and that is where a day-one setup goes wrong.** Overview shows
  this installation's own values to copy; paste those, not a placeholder.
- **Outside a cluster there is no API server to prove access to.** The service prints a generated recovery password once
  at start; read it off the log (on AWS Lambda it is an SSM parameter: [recover on Lambda](recover-on-lambda.md)). That
  installation gets a fifth setup step, turn the password off, because a stored password is a standing credential and a
  token is not.

## Steps

### 1. Install

**Run** the install ([install with Helm](install-with-helm.md)).
**Expect** `Secret <release>-session-key` and the recovery ServiceAccount to exist; there is no password anywhere.
**Verify** `kubectl -n <namespace> get secret <release>-session-key serviceaccount/<recovery serviceAccount>`.
**Rollback**: uninstall the release.

### 2. Mint a recovery token

**Run**

```sh
kubectl -n <namespace> create token <recovery serviceAccount> \
  --audience <recovery audience> --duration 10m
```

`config.recovery.serviceAccount` and `config.recovery.audience` are the names the values carry. Granting `create` on
`serviceaccounts/token` for that account is how you let somebody else do this.
**Expect** a JWT on stdout.
**Verify** it is accepted in step 3.
**Rollback**: none, because a token expires in ten minutes and is not stored.

### 3. Sign in with it

**Run** port-forward the service's port (or go through the gateway), open `/login`, expand **Recovery sign-in**, paste
the token.
**Expect** the console, with Overview on top.
**Verify** the session shows the identity that minted the token. A refusal is one of the cases in
[lost operator access](lost-operator-access.md#before-you-start).
**Rollback**: sign out.

### 4. Follow Overview

**Run** the four steps Overview lists: register an OAuth client with the directory, give it to the service, connect the
first directory, attach a directory group to the operators group. Each disappears as it completes. The first one is the
only one that leaves the console: [connect a Google Workspace](../connect/google-workspace.md) walks the cloud-console
visit, and Overview has the two values to paste into it.
**Expect** Overview empties.
**Verify** the directory page shows the workspace healthy.
**Rollback**: *Disconnect* the directory; the other steps are values you can change again.

### 5. Sign in as yourself

**Run** sign out, sign in through the directory, search for yourself.
**Expect** your page shows *operator* and the membership that granted it.
**Verify** the same page.
**Rollback**: none, because it only reads.

## Afterwards

- The console signs people in as a client of the issuer it shares an origin with, so steps 3 and 5 are the issuer's own
  sign-in page, and recovery stays reachable by port-forward.
- Check [the installation's health](check-health.md) once the first snapshot has run.
- Tell the operators group who is in it, and that [recovery](lost-operator-access.md) exists.
