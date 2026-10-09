# Fix a writer that refuses to start on its key directory

Recover from `this writer's key directory is not the deployment's`.

## Before you start

- This applies to the `local` key provider only. The transit provider has no directory.

- You need access to the pods' volumes and the database.

- The wrapped data keys are random, so the directory is the only copy. Recreating it re-keys every tenant: the same person gets a new pseudonym and the trail before no longer links to the trail after.

- A restored directory is accepted as it was.

## Steps

1. Find the cause.

   - The directory is not shared. Every replica must mount the same directory (`ReadWriteMany`). Fix the mount.

   - The directory was lost and recreated. Restore it from backup.

2. Only with no backup, and only if you accept the new pseudonyms, remove the old binding and record why in the trail by hand.

   ```
   delete from audit_key_directory;
   ```

   The next writer to start registers its directory and the rest must share it.

## Verify

The writer starts.

## Roll back

None. The old pseudonyms do not come back.

## Afterwards

Back up the key directory (the chart's `keysVolume`) and record the re-keying in your decision log. Move a deployment that hit this to the transit provider.
