# Change what a source records

Add or change an action, schema or message of a catalogue and roll it out in the order that dead-letters nothing: emitter tag, writer catalogue, writer, emitter.

## Before you start

- A catalogue is a document plus the JSON schemas it references ([catalogue reference](../../../reference/audit/catalogue.md)). The writer refuses to start when a referenced schema is missing or a schema is unreferenced. Ship the directory, not only the `.yaml`. A directory holds one catalogue, and its `.yaml` needs `source`, `version` and `actions`.

- Bump `version` for any change, even one new action. The archive refuses a different document under a version it holds, which stops the writer and the application. Keep the released document as a fixture.

- A record naming a catalogue version the writer lacks is dead-lettered and acknowledged, so no queue alarm fires. Look for `event=unknown_catalogue`, the counter `audit_writer_catalogue_unknown_total`, and on AWS the alarm `<name>-writer-unknown-catalogue`.

## Steps

1. Tag an emitter release with the new catalogue version. Check that its CI confirms every emitted action is declared and the reverse, and that the release carries the catalogue bundle. Sluis ships `sluis-audit-catalogue_<version>.tar.gz`.

2. Give the writer the catalogue.

   On Kubernetes, an application registers its catalogue at start-up, so the writer needs no change. A catalogue given through the chart's `catalogues` value is a values change. On AWS Lambda the writer has no registry. Fetch the release asset, check it against `checksums.txt`, unpack it and give the directory to the stack:

   ```sh
   curl -fsSLO "https://github.com/truvity/sluis/releases/download/v<version>/sluis-audit-catalogue_<version>.tar.gz"
   grep "sluis-audit-catalogue_<version>.tar.gz" checksums.txt | sha256sum -c -
   tar -xzf sluis-audit-catalogue_<version>.tar.gz -C catalogue/
   ```

   ```go
   Writer: auditpulumi.WriterArgs{
       CatalogueDirs: []string{"catalogue/roster"}, // the document and its *.json; roster is sluis's source name
   },
   ```

   `pulumi preview` must show a new configuration layer version and pass the catalogue guard ([ship a release](../operate/aws-ship-a-release.md)).

3. Deploy the writer. Its `audit.writer.started` record carries the catalogues' digest, and on Kubernetes `/readyz` answers.

4. Deploy the emitter. It logs its registration at start, and a record of the new action reaches the archive.

## Roll back

- Writer: redeploy the previous program. On AWS the previous layer version is kept (`SkipDestroy`). Old catalogue versions stay in the archive, so their records still read.
- Emitter: redeploy the previous emitter. The writer knows both versions.

## After the rollout

Watch `event=unknown_catalogue` for an hour. An increase means a pod runs a version the writer lacks. Replay its records from `dlq/` with [replay dead-lettered records](../operate/replay-dead-lettered-records.md) once the writer has the version. Tell readers of the trail if a message template changed.
