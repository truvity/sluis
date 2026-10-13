// Package sluispulumi is the AWS shape of sluis as a Pulumi Go library: the
// storage the service keeps its blobs in, the DynamoDB tables of the State port
// (one per module on layout v5), and, as the main path, the Lambda deployment:
// one function per module from the release zips, each with its own role. The
// issuer function (issuer, signer and console) sits behind an HTTP API and
// owns the token-signing key; the github, slack, google and cloudflare
// functions hold their module's credentials and run its passes; the backup
// function writes one age-encrypted archive a day, and the restore function is
// the backup zip deployed a second time. The custom domain with mutual TLS, the
// certificate and the truststore are a front door's: an edge module
// (github.com/truvity/sluis/deploy/pulumi/edge/cloudflare) takes
// Lambda.FrontDoor(). The EKS Pod Identity role is kept for an installation
// that runs the Deployment.
//
// It is a library, not a program: a stack calls the constructors below and
// gets components with the outputs a deployment needs. It creates nothing by
// being imported, this repository deploys nothing with it, and it is a module
// of its own (github.com/truvity/sluis/deploy/pulumi) so that Pulumi is
// not in the root module's dependency graph.
//
//	store, err := sluispulumi.NewStorage(ctx, "access", &sluispulumi.StorageArgs{
//		BucketName: "acme-sluis", Versioning: true,
//		ProtectedPrefixes: []sluispulumi.ProtectedPrefix{edgecloudflare.Guard(applyRoleArn)},
//	}, pulumi.Providers(aws))
//	states, err := sluispulumi.NewStates(ctx, "access", &sluispulumi.StatesArgs{ /* one table per module */ }, pulumi.Providers(aws))
//	l, err := sluispulumi.NewLambda(ctx, "access", &sluispulumi.LambdaArgs{
//		Region: "eu-central-1", AccountID: accountID, Instance: "acme",
//		Layout:        "v5",
//		Package:       "sluis-issuer_1.76.3_linux_arm64.zip", // a path or an https URL; empty deploys the library's own release
//		PackageSHA256: releaseSHA256,                         // from the release's checksums, pinned in source
//		Installation:  installation, // *sluisconfig.Installation: the library renders every function's documents from it
//		Storage:       store.Grant(),
//		State:         states.Grant(),
//		Keys:          &sluispulumi.KeysArgs{Sign: "alias/sluis-acme-sign", Secrets: "alias/sluis-acme-secrets"},
//		Schedule:      sluispulumi.ScheduleArgs{GitHubOrgs: []string{"acme"}},
//	}, pulumi.Providers(aws))
//	b, err := sluispulumi.NewBackup(ctx, "access", &sluispulumi.BackupArgs{
//		BackupCommon:    common,               // the same instance, storage, tables and the archive bucket
//		ExtraRecipients: []string{"age1..."}, // an offline break-glass recipient (age-keygen)
//	}, pulumi.Providers(aws))
//	_, err = sluispulumi.NewRestore(ctx, "access", &sluispulumi.RestoreArgs{
//		BackupCommon:    common,
//		RestoreInvokers: sluispulumi.RestoreInvokers{AdminRoleArns: admins, BreakglassRoleArns: breakglass},
//	}, pulumi.Providers(aws))
//	_, err = edgecloudflare.NewEdge(ctx, "access", &edgecloudflare.Args{
//		FrontDoor: l.FrontDoor(), DomainName: "access.example.test",
//		CertificateArn: certArn, TruststorePEM: originPullCA, Storage: store,
//	}, pulumi.Providers(aws))
//
// With an Installation, LambdaArgs.Audit decides where the audit records go:
// the audit Pulumi library of this repository is installed by default (the
// operational preset, derived from the profiles; every function's role is a
// sender of its queue), Audit.Use sends them to an installation that exists, and
// Audit.Enabled false sends them nowhere (the service logs them).
//
// Each function is a release zip's `bootstrap`, in no VPC, with one role that
// holds its module's table, its `internal/<module>/*` and `external/<module>/*`
// parameters and its blob prefix, and nothing of another module's. The issuer's
// role alone uses the signing key. A module function answers only the caller
// class its alias names (`live-issuer`, `live-console`, `live-admin`,
// `live-breakglass`); the restore function answers administrators and
// break-glass roles only. Code is the release zip, byte for byte, held to its
// SHA-256; each function's documents, held to sluis's own loader, are an
// immutable configuration layer mounted last at /opt/sluis, and a change
// publishes a new layer version and updates the function (the old version is
// kept, for a rollback). The issuer's OAuth-state secret, the recovery password
// and the backups' restore identity are generated and kept in SSM under the
// installation's root, /sluis/<instance>. (docs/decisions/0041)
// /sluis/<instance>/internal/* is sluis's alone;
// /sluis/<instance>/external/<module>/<id> are the documents consumers read,
// each granted on its own side.
//
// A layout v4 installation (LambdaArgs.Layout, the default) is one function
// and one table; v1.76 is the last release that reads it (docs/decisions/0072).
//
// RenderPorts renders the `ports:` block of the processes' configuration from
// the same names.
//
// docs/reference/sluis/pulumi-library.md is the guide: every input and output and the IAM.
package sluispulumi
