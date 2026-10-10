package sluispulumi

import (
	"encoding/json"
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/scheduler"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/sqs"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// newArchiveBucket is the archive bucket the library creates: versioned and
// under Object Lock with a default retention no longer than `retention.maxAge`,
// encrypted (the bucket key, or AES256), closed to the public and to plain HTTP,
// protected. Noncurrent versions go a day after the lock could have ended, so
// that a deleted backup does not stay for good.
func newArchiveBucket(ctx *pulumi.Context, name string, p *backupPlan, parent pulumi.ResourceOption) (*s3.Bucket, error) {
	c := p.Archive.Create
	tags := tagMap(c.Tags)
	bucket, err := s3.NewBucket(ctx, name+"-archive", &s3.BucketArgs{Bucket: pulumi.String(p.Archive.Bucket), Tags: tags},
		parent, pulumi.Protect(true))
	if err != nil {
		return nil, fmt.Errorf("sluis archive bucket: %w", err)
	}
	versioning, err := s3.NewBucketVersioning(ctx, name+"-archive-versioning", &s3.BucketVersioningArgs{
		Bucket:                  bucket.ID(),
		VersioningConfiguration: &s3.BucketVersioningVersioningConfigurationArgs{Status: pulumi.String("Enabled")},
	}, parent)
	if err != nil {
		return nil, fmt.Errorf("sluis archive versioning: %w", err)
	}
	mode := c.Mode
	if mode == "" {
		mode = "GOVERNANCE"
	}
	if _, err := s3.NewBucketObjectLockConfiguration(ctx, name+"-archive-lock", &s3.BucketObjectLockConfigurationArgs{
		Bucket: bucket.ID(), ObjectLockEnabled: pulumi.String("Enabled"),
		Rule: &s3.BucketObjectLockConfigurationRuleArgs{
			DefaultRetention: &s3.BucketObjectLockConfigurationRuleDefaultRetentionArgs{Mode: pulumi.String(mode), Days: pulumi.Int(p.lockDays)},
		},
	}, parent, pulumi.DependsOn([]pulumi.Resource{versioning})); err != nil {
		return nil, fmt.Errorf("sluis archive object lock: %w", err)
	}
	sse := &s3.BucketServerSideEncryptionConfigurationRuleApplyServerSideEncryptionByDefaultArgs{SseAlgorithm: pulumi.String("AES256")}
	if k := p.Archive.SSEKeyArn; k != "" {
		sse = &s3.BucketServerSideEncryptionConfigurationRuleApplyServerSideEncryptionByDefaultArgs{
			SseAlgorithm: pulumi.String("aws:kms"), KmsMasterKeyId: pulumi.String(k),
		}
	}
	if _, err := s3.NewBucketServerSideEncryptionConfiguration(ctx, name+"-archive-encryption", &s3.BucketServerSideEncryptionConfigurationArgs{
		Bucket: bucket.ID(),
		Rules: s3.BucketServerSideEncryptionConfigurationRuleArray{
			&s3.BucketServerSideEncryptionConfigurationRuleArgs{ApplyServerSideEncryptionByDefault: sse, BucketKeyEnabled: pulumi.Bool(p.Archive.SSEKeyArn != "")},
		},
	}, parent); err != nil {
		return nil, fmt.Errorf("sluis archive encryption: %w", err)
	}
	block, err := s3.NewBucketPublicAccessBlock(ctx, name+"-archive-public-access", &s3.BucketPublicAccessBlockArgs{
		Bucket: bucket.ID(), BlockPublicAcls: pulumi.Bool(true), BlockPublicPolicy: pulumi.Bool(true),
		IgnorePublicAcls: pulumi.Bool(true), RestrictPublicBuckets: pulumi.Bool(true),
	}, parent)
	if err != nil {
		return nil, fmt.Errorf("sluis archive public access block: %w", err)
	}
	policy := bucket.Arn.ApplyT(func(arn string) (string, error) {
		return document([]statement{{
			"Sid": "DenyPlainHTTP", "Effect": "Deny", "Principal": "*", "Action": "s3:*", "Resource": []string{arn, arn + "/*"},
			"Condition": map[string]any{"Bool": map[string]any{"aws:SecureTransport": "false"}},
		}})
	}).(pulumi.StringOutput)
	if _, err := s3.NewBucketPolicy(ctx, name+"-archive-policy", &s3.BucketPolicyArgs{Bucket: bucket.ID(), Policy: policy},
		parent, pulumi.DependsOn([]pulumi.Resource{block})); err != nil {
		return nil, fmt.Errorf("sluis archive policy: %w", err)
	}
	if _, err := s3.NewBucketLifecycleConfiguration(ctx, name+"-archive-lifecycle", &s3.BucketLifecycleConfigurationArgs{
		Bucket: bucket.ID(),
		Rules: s3.BucketLifecycleConfigurationRuleArray{
			&s3.BucketLifecycleConfigurationRuleArgs{
				Id: pulumi.String("noncurrent-versions"), Status: pulumi.String("Enabled"),
				Filter:                      &s3.BucketLifecycleConfigurationRuleFilterArgs{Prefix: pulumi.String(p.prefix + "backup/")},
				NoncurrentVersionExpiration: &s3.BucketLifecycleConfigurationRuleNoncurrentVersionExpirationArgs{NoncurrentDays: pulumi.Int(p.lockDays + 1)},
			},
			&s3.BucketLifecycleConfigurationRuleArgs{
				Id: pulumi.String("abort-incomplete-multipart-uploads"), Status: pulumi.String("Enabled"),
				Filter: &s3.BucketLifecycleConfigurationRuleFilterArgs{Prefix: pulumi.String(p.prefix + "backup/")},
				AbortIncompleteMultipartUpload: &s3.BucketLifecycleConfigurationRuleAbortIncompleteMultipartUploadArgs{
					DaysAfterInitiation: pulumi.Int(7),
				},
			},
		},
	}, parent, pulumi.DependsOn([]pulumi.Resource{versioning})); err != nil {
		return nil, fmt.Errorf("sluis archive lifecycle: %w", err)
	}
	return bucket, nil
}

// newBackupSchedules is the daily run and the resume schedule, both invoking
// the live alias through a scheduler role that may invoke that alias and send to
// the dead-letter queue, and nothing else. A schedule that cannot deliver sends
// its event to the queue.
func newBackupSchedules(ctx *pulumi.Context, name string, p *backupPlan, sch BackupScheduleArgs, out *Backup,
	parent pulumi.ResourceOption) error {
	liveArn := out.LiveAliasArn
	tags := tagMap(p.Tags)
	dlq, err := sqs.NewQueue(ctx, name+"-schedule-dlq", &sqs.QueueArgs{
		Name: pulumi.String(p.FunctionName + "-schedule-dlq"), MessageRetentionSeconds: pulumi.Int(14 * 24 * 3600),
		SqsManagedSseEnabled: pulumi.Bool(true), Tags: tags,
	}, parent)
	if err != nil {
		return fmt.Errorf("sluis schedule dead-letter queue: %w", err)
	}
	out.ScheduleDLQArn = dlq.Arn
	trust, _ := json.Marshal(map[string]any{
		"Version": polVersion,
		"Statement": []map[string]any{{
			"Effect": "Allow", "Principal": map[string]any{"Service": "scheduler.amazonaws.com"}, "Action": "sts:AssumeRole",
			"Condition": map[string]any{"StringEquals": map[string]any{"aws:SourceAccount": p.AccountID}},
		}},
	})
	rargs := &iam.RoleArgs{Name: pulumi.String(p.FunctionName + "-scheduler"), AssumeRolePolicy: pulumi.String(string(trust)), Tags: tags}
	if p.PermissionsBoundaryArn != "" {
		rargs.PermissionsBoundary = pulumi.String(p.PermissionsBoundaryArn)
	}
	role, err := iam.NewRole(ctx, name+"-scheduler-role", rargs, parent)
	if err != nil {
		return err
	}
	if _, err := iam.NewRolePolicy(ctx, name+"-scheduler-policy", &iam.RolePolicyArgs{
		Name: pulumi.String(p.FunctionName + "-scheduler"), Role: role.Name,
		Policy: pulumi.All(liveArn, dlq.Arn).ApplyT(func(v []any) (string, error) {
			return document([]statement{
				{"Sid": "SluisBackupTick", "Effect": "Allow", "Action": lambdaInvokeFunction, "Resource": []string{v[0].(string)}},
				{"Sid": "SluisBackupDLQ", "Effect": "Allow", "Action": sqsSendMessage, "Resource": []string{v[1].(string)}},
			})
		}).(pulumi.StringOutput),
	}, parent); err != nil {
		return err
	}
	out.SchedulerRoleArn = role.Arn
	var names pulumi.StringArray
	for _, s := range []struct{ suffix, expr, desc, input string }{
		{"daily", sch.Daily, "Takes the daily backup.", `{"kind":"backup"}`},
		{"resume", sch.ResumeRate, "Continues a paused backup run.", `{"kind":"backup","resume":true}`},
	} {
		sname := p.FunctionName + "-" + s.suffix
		if len(sname) > 64 {
			return fmt.Errorf("sluispulumi: the schedule name %q is longer than 64 characters", sname)
		}
		args := &scheduler.ScheduleArgs{
			Name: pulumi.String(sname), Description: pulumi.String(s.desc),
			ScheduleExpression: pulumi.String(s.expr), ScheduleExpressionTimezone: pulumi.String("UTC"),
			State:              scheduleState(sch.Paused),
			FlexibleTimeWindow: &scheduler.ScheduleFlexibleTimeWindowArgs{Mode: pulumi.String("OFF")},
			Target: &scheduler.ScheduleTargetArgs{
				Arn: liveArn, RoleArn: role.Arn, Input: pulumi.String(s.input),
				DeadLetterConfig: &scheduler.ScheduleTargetDeadLetterConfigArgs{Arn: dlq.Arn},
				RetryPolicy:      &scheduler.ScheduleTargetRetryPolicyArgs{MaximumRetryAttempts: pulumi.Int(2), MaximumEventAgeInSeconds: pulumi.Int(3600)},
			},
		}
		if _, err := scheduler.NewSchedule(ctx, name+"-"+s.suffix, args, parent); err != nil {
			return fmt.Errorf("sluis backup schedule %s: %w", sname, err)
		}
		names = append(names, pulumi.String(sname))
	}
	out.ScheduleNames = names.ToStringArrayOutput()
	return nil
}

// alarm declares one alarm on a metric of the function, treating missing data as
// the caller says.
func alarm(ctx *pulumi.Context, res, aname, desc string, actions []string, a *cloudwatch.MetricAlarmArgs, parent pulumi.ResourceOption) (string, error) {
	a.Name, a.AlarmDescription = pulumi.String(aname), pulumi.String(desc)
	if len(actions) > 0 {
		arns := pulumi.Array{}
		for _, x := range actions {
			arns = append(arns, pulumi.String(x))
		}
		a.AlarmActions = arns
	}
	if _, err := cloudwatch.NewMetricAlarm(ctx, res, a, parent); err != nil {
		return "", fmt.Errorf("sluis alarm %s: %w", aname, err)
	}
	return aname, nil
}

// backupNamespace is the namespace of the metrics the log filter makes.
func backupNamespace(instance string) string { return "Sluis/Backup/" + instance }

// newBackupAlarms are the three alarms of the backup function:
//
//   - no completed backup in 36 hours: the log line "a backup was written" is
//     counted by a metric filter; hourly sums below 1 for 36 hours in a row, and
//     missing data, alarm;
//   - a failed run: a failed run fails the invocation, so the function's Errors
//     metric is at least 1 in five minutes;
//   - a message in the schedules' dead-letter queue: an event that could not be
//     delivered.
func newBackupAlarms(ctx *pulumi.Context, name string, p *backupPlan, fn *backupFn, parent pulumi.ResourceOption) (pulumi.StringArray, error) {
	if _, err := cloudwatch.NewLogMetricFilter(ctx, name+"-completed-filter", &cloudwatch.LogMetricFilterArgs{
		Name: pulumi.String(p.FunctionName + "-completed"), LogGroupName: fn.logs.Name, Pattern: pulumi.String(`"a backup was written"`),
		MetricTransformation: &cloudwatch.LogMetricFilterMetricTransformationArgs{
			Name: pulumi.String("Completed"), Namespace: pulumi.String(backupNamespace(p.Instance)), Value: pulumi.String("1"),
		},
	}, parent); err != nil {
		return nil, fmt.Errorf("sluis completed-backup metric filter: %w", err)
	}
	var names pulumi.StringArray
	add := func(res, aname, desc string, a *cloudwatch.MetricAlarmArgs) error {
		n, err := alarm(ctx, name+"-"+res, aname, desc, p.AlarmActionArns, a, parent)
		if err == nil {
			names = append(names, pulumi.String(n))
		}
		return err
	}
	if err := add("alarm-no-backup", p.FunctionName+"-no-backup-"+fmt.Sprint(NoBackupAlarmHours)+"h",
		fmt.Sprintf("No backup completed in %d hours.", NoBackupAlarmHours), &cloudwatch.MetricAlarmArgs{
			Namespace: pulumi.String(backupNamespace(p.Instance)), MetricName: pulumi.String("Completed"), Statistic: pulumi.String("Sum"),
			Period: pulumi.Int(3600), EvaluationPeriods: pulumi.Int(NoBackupAlarmHours), DatapointsToAlarm: pulumi.Int(NoBackupAlarmHours),
			Threshold: pulumi.Float64(1), ComparisonOperator: pulumi.String("LessThanThreshold"), TreatMissingData: pulumi.String("breaching"),
		}); err != nil {
		return nil, err
	}
	if err := add("alarm-failed", p.FunctionName+"-failed-run", "A backup run failed.", &cloudwatch.MetricAlarmArgs{
		Namespace: pulumi.String("AWS/Lambda"), MetricName: pulumi.String("Errors"), Statistic: pulumi.String("Sum"),
		Dimensions: pulumi.StringMap{"FunctionName": fn.fn.Name},
		Period:     pulumi.Int(300), EvaluationPeriods: pulumi.Int(1), Threshold: pulumi.Float64(1),
		ComparisonOperator: pulumi.String("GreaterThanOrEqualToThreshold"), TreatMissingData: pulumi.String("notBreaching"),
	}); err != nil {
		return nil, err
	}
	if err := add("alarm-dlq", p.FunctionName+"-schedule-dlq", "A schedule's event could not be delivered.", &cloudwatch.MetricAlarmArgs{
		Namespace: pulumi.String("AWS/SQS"), MetricName: pulumi.String("ApproximateNumberOfMessagesVisible"), Statistic: pulumi.String("Maximum"),
		Dimensions: pulumi.StringMap{"QueueName": pulumi.String(p.FunctionName + "-schedule-dlq")},
		Period:     pulumi.Int(300), EvaluationPeriods: pulumi.Int(1), Threshold: pulumi.Float64(1),
		ComparisonOperator: pulumi.String("GreaterThanOrEqualToThreshold"), TreatMissingData: pulumi.String("notBreaching"),
	}); err != nil {
		return nil, err
	}
	return names, nil
}
