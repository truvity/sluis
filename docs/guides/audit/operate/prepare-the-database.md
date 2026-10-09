# Prepare the database

Create one Postgres database with an owner, one role per part (writer, indexer, query service, optional purge job) and the schema applied.

## Before you start

- Use a Postgres the installation can reach. One instance is enough: the index is rebuildable from the archive, so it needs no backup or replica. See [repair or rebuild the index](rebuild-the-index.md).

- On AWS Lambda the writer has no database: deduplication is DynamoDB. Observe and query still need one.

- Use different roles. The tenant row-level policies bind the query role and an owner bypasses them. The chart refuses to render when the writer, indexer or query service names the owner's or one another's credentials.

- The writer and indexer never migrate themselves and refuse to start against an unknown schema version.

- A password in a database URL is refused. Name a secret with `passwordSecret` and let the [`secrets` block](../../../reference/audit/configuration.md#secrets) resolve it.

## Steps

1. Create the database and the roles.

   ```sql
   create database audit;
   create role audit_owner login password :'owner';
   create role audit_writer login password :'writer';
   create role audit_observe login password :'observe';
   create role audit_query login password :'reader';
   grant all privileges on database audit to audit_owner;
   ```

2. Apply the schema and grant each role what its part needs, as the owner.

   ```console
   $ audit migrate --database "$OWNER_URL" --writer audit_writer \
       --observe audit_observe --reader audit_query
   ```

   `--writer` grants the deduplication table, registry and key directory. `--observe` grants the index, its cursors and the function that creates a month's partition. `--reader` grants select on the index only. `--purge` names the purge role. Each is also a key of the job's file.

3. In the chart, the `migrate` hook (a pre-install and pre-upgrade Job) does the same. Set `database.passwordSecret` on each component and project the Secrets with `secretFiles`. With a declarative Postgres operator, put the database and users in the cluster manifest.

## Verify

`\du` lists the four roles, `audit migrate` reports the schema version, and the writer starts. After records land, `audit conformance` passes against the query service: see [verify the trail](verify-the-trail.md).

## Roll back

The schema is additive. Drop the database to start over.

```sql
drop database audit;
```
