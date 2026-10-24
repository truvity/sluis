# Connect and enable an Outline workspace

Have the Outline module keep an Outline wiki's admins, groups and collections, first as a dry run.

## Before you start

- Outline signs in through sluis, by a client the policy declares.

- The storage adapter is not `legacy`. The workspace's connection is kept in the module's own table.

- The module can reach the wiki's URL over https. Loopback and link-local addresses are refused.

## 1. Declare the workspace

Add the table to the policy. A key we choose names the workspace:

```yaml
outline:
  workspaces:
    wiki:
      client: wiki
      roles: {admin: [all:wiki:admin]}
      ignore: [key.owner@example.com]
      collections:
        General: {mode: strict, read_write: [all:wiki:everyone], admin: [all:wiki:admin]}
        Acme: {mode: strict, directory: example.com, read_write: [all:wiki:acme]}
```

`directory` is the directory's primary domain. Every key is in the [Outline reference](../../reference/sluis/outline.md#the-policy-table). Leave the key out of `controllers.outline.enabledWorkspaces`.

## 2. Run the module

- **Lambda:** set `controllers.outline: {}` in the installation document. The render adds the module's document and the issuer's `outline.remote`. Give the library the module's package and `Schedule.OutlineWorkspaces`, which ticks the function every two minutes.

- **Kubernetes:** enable `modules.outline` and write its document under `modules.outline.config`. The chart adds the console's URL, `outline.serve` and the issuer's `outline.remote`.

The module reads the console as a roster controller, so the policy's exchange must admit its ServiceAccount or its function role.

## 3. Connect it

Create an API key as an Outline admin. Outline has no service accounts, so the key belongs to a person. Put that person's address in `ignore`. A pilot may use your own admin account; use a dedicated one before the module manages more.

On the console, open SYSTEMS, Outline, and **Connect**: the URL and the key. The module asks Outline who the key is. It refuses a key that is not an admin's.

## 4. Read the dry run

Wait for a pass, or press **Refresh**. The report says what enabling would do: accounts to suspend, groups to create, grants to add or remove. Read the held rows and the findings. A collection held by `directory` names the line to fix.

## 5. Enable

Add the key to `controllers.outline.enabledWorkspaces` and roll out. The card says it **acts**, and `roster.outline_*` records appear.

A set over a breaker's limit waits for **Confirm** on the card. A confirmation lapses after 24 hours.

## Roll back

Remove the key from `enabledWorkspaces` and roll out. The workspace returns to a dry run. What was changed stays.

**Disconnect** forgets the URL and the key. Nothing in Outline changes.
