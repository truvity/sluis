# Redrive the ingest dead-letter queue

Move the messages in the ingest queue's DLQ back to the ingest queue once the cause is fixed.

## Before you start

- The installation runs on AWS Lambda. See [getting started](../../../get-started/audit/aws-lambda.md).

- You know `DlqArn` and `QueueArn`, both stack outputs.

- Your operator role is named in `Ingest.Redrivers`. Its identity policy has `sqs:SendMessage` on `QueueArn`, `sqs:StartMessageMoveTask`, `sqs:ReceiveMessage` and `sqs:DeleteMessage` on the DLQ.

- The queue policy denies `sqs:SendMessage` to every principal not named. Without `Redrivers` you get `AccessDenied` from `StartMessageMoveTask`. Add it with `pulumi up`.

- `Senders` and `Redrivers` take role or user ARNs (`arn:aws:iam::<account>:role/<path>/<name>`). The library refuses assumed-role session ARNs, `sts` ARNs, bare account ids and wildcards at preview.

- Fix the cause first. A message moved back with its cause in place returns to the DLQ after `MaxReceiveCount` deliveries.

- A message that is not a record returns every time. Inspect it before you redrive the batch.

- A record naming a catalogue version the writer lacks is not in this DLQ. See [replay dead-lettered records](replay-dead-lettered-records.md) and the `<name>-writer-unknown-catalogue` alarm.

## Steps

1. See what is waiting. The `<name>-ingest-dlq-not-empty` alarm sends you here.

   ```sh
   aws sqs get-queue-attributes --queue-url "$DLQ_URL" \
     --attribute-names ApproximateNumberOfMessages
   ```

2. Fix the cause (writer release, catalogue, your own permission) and deploy it. The writer log shows no new failures.

3. Start the move, or use the console's "Start DLQ redrive".

   ```sh
   aws sqs start-message-move-task --source-arn "$DLQ_ARN" --destination-arn "$QUEUE_ARN"
   ```

   The command returns a `TaskHandle`.

## Verify

```sh
aws sqs list-message-move-tasks --source-arn "$DLQ_ARN"
```

The task is `COMPLETED`, the DLQ is empty and the alarm returns to OK. Records keep their identifiers, so one that got through is deduplicated.

## Roll back

`aws sqs cancel-message-move-task --task-handle <handle>` stops the task. Messages already moved stay in the ingest queue and the writer archives them. To undo step 2, redeploy the previous program.

## If messages return

The cause was not the one you fixed. Read the writer log for the reason. Tell the emitting application's owner which records were late: they carry their original `occurred_at`, and `recorded_at` shows the delay.
