// Package sluispulumi is the AWS shape of sluis as a Pulumi Go library: the
// storage the service keeps its blobs in, the DynamoDB table of the State port,
// and, as the main path, the Lambda deployment: ONE function from the release
// zip with one role, an HTTP API, the token-signing key and the
// controllers' schedules. The custom domain with mutual TLS, the certificate and
// the truststore are a front door's: an edge module
// (github.com/truvity/sluis/deploy/pulumi/edge/cloudflare) takes Lambda.FrontDoor(). The EKS Pod Identity roles
// are kept, collapsed to ONE role for the one pod, for an installation that
// runs the Deployment.
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
//	state, err := sluispulumi.NewState(ctx, "access", &sluispulumi.StateArgs{
//		TableName: "acme-sluis",
//	}, pulumi.Providers(aws))
//	l, err := sluispulumi.NewLambda(ctx, "access", &sluispulumi.LambdaArgs{
//		Region: "eu-central-1", AccountID: accountID, Instance: "acme",
//		Package:        "sluis-lambda_1.63.0_linux_arm64.zip", // a path or an https URL
//		PackageSHA256:  releaseSHA256,                         // from the release's checksums
//		Config:         sluisYAML, // the v3 service document, controllers included
//		PolicyPath:     "policy/", // or Policy: a rendered document
//		Storage:        store.Grant(),
//		State:          state.Grant(),
//		AuditQueueArn:  auditQueueArn,
//		Schedule: sluispulumi.ScheduleArgs{GitHubOrgs: []string{"acme"}},
//	}, pulumi.Providers(aws))
//	_, err = edgecloudflare.NewEdge(ctx, "access", &edgecloudflare.Args{
//		FrontDoor: l.FrontDoor(), DomainName: "access.example.test",
//		CertificateArn: certArn, TruststorePEM: originPullCA, Storage: store,
//	}, pulumi.Providers(aws))
//
// The one function (`sluis`, or LambdaArgs.FunctionName) is the release zip's
// `bootstrap`, in no VPC, with one role: it serves the issuer and the console
// behind the API, and runs the GitHub and Slack controllers' passes (one
// invocation per target, from a schedule or a run-now) and the exports and
// directory refresh. The role may kms:Sign with the signing key (or, with
// WrappedSigning, generate and decrypt key pairs under the one symmetric key),
// and invoke itself; the controllers' code runs with it, so there is no
// isolation between the issuer and a controller. Its code is the release zip,
// byte for byte, held to its SHA-256; the service document (v3) and the policy,
// held to sluis's own loader, are an immutable configuration layer mounted last
// at /opt/sluis, and a change to either publishes a new layer version and
// updates the function (the old version is kept, for a rollback). The issuer's
// OAuth-state secret and the recovery password are generated and kept in SSM
// under the installation's root, /sluis/<instance> (layout v3).
// /sluis/<instance>/private/* is sluis's alone, and config/* under it the
// function reads; /sluis/<instance>/export/* is for consumers, and
// Lambda.ExportReadPolicyJSON is the policy that reads it and nothing else.
//
// RenderPorts renders the `ports:` block of the processes' configuration from
// the same names.
//
// docs/reference/pulumi-library.md is the guide: every input and output and the IAM.
package sluispulumi
