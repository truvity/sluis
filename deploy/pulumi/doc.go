// Package sluispulumi is the AWS shape of sluis as a Pulumi Go library: the
// storage the service keeps its blobs in, the DynamoDB table of the State port,
// and, as the main path, the Lambda deployment: three functions from one zip, a
// role each, an HTTP API with a mutual-TLS custom domain, the token-signing key
// and the controllers' schedules. The EKS Pod Identity roles of the three
// processes are kept for an installation that still runs the Deployments.
//
// It is a library, not a program: a stack calls the constructors below and
// gets components with the outputs a deployment needs. It creates nothing by
// being imported, this repository deploys nothing with it, and it is a module
// of its own (github.com/truvity/sluis/deploy/pulumi) so that Pulumi is
// not in the root module's dependency graph.
//
//	store, err := sluispulumi.NewStorage(ctx, "access", &sluispulumi.StorageArgs{
//		BucketName: "acme-sluis",
//	}, pulumi.Providers(aws))
//	state, err := sluispulumi.NewState(ctx, "access", &sluispulumi.StateArgs{
//		TableName: "acme-sluis",
//	}, pulumi.Providers(aws))
//	l, err := sluispulumi.NewLambda(ctx, "access", &sluispulumi.LambdaArgs{
//		Region: "eu-central-1", AccountID: accountID,
//		Package:        "sluis-lambda_1.58.0_linux_arm64.zip", // a path or an https URL
//		Config:         sluisYAML, GitHubConfig: githubYAML, SlackConfig: slackYAML, // config/{sluis,github,slack}.yaml
//		CataloguePaths: []string{"catalogues/github-apps.yaml"},
//		Storage:        store.Grant(),
//		State:          state.Grant(),
//		AuditQueueArn:  auditQueueArn,
//		API: sluispulumi.APIArgs{
//			DomainName:           "access.example.test",
//			CertificateArn:       certArn,
//			TruststorePEM:        originPullCA,
//			TruststoreBucketName: "acme-sluis-truststore",
//		},
//		Schedule: sluispulumi.ScheduleArgs{GitHubOrgs: []string{"acme"}},
//	}, pulumi.Providers(aws))
//
// The three functions (sluis-http, sluis-github, sluis-slack) are one
// `bootstrap` told apart by SLUIS_ROLE, in no VPC, each with a role of its own:
// only sluis-http may kms:Sign with the signing key (or, with WrappedSigning, generate
// and decrypt key pairs under the one symmetric key) and invoke the controllers.
// The estate's configuration and its catalogues are added to the zip, so a change
// to either changes the package and redeploys. The issuer's OAuth-state secret is
// generated and kept in SSM (Lambda.StateSecretParameter). /sluis/private/* in SSM is
// sluis's alone; /sluis/export/* is for consumers, and
// Lambda.ExportReadPolicyJSON is the policy that reads it and nothing else.
//
// RenderPorts renders the `ports:` block of the processes' configuration from
// the same names.
//
// docs/deployment/aws.md is the guide: every input and output and the IAM.
package sluispulumi
