package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/truvity/sluis/internal/migrate"
)

const ssmLayoutUsage = `Usage: sluis migrate ssm-layout --to-root /sluis/<instance> [--from-root /sluis] [flags]

Copies the configuration secrets an operator seeded in SSM Parameter Store from
layout v2 (<from-root>/private/config/...) to layout v3
(<to-root>/private/config/..., docs/decisions/0036), under the names the
documents give them: oauth/client-id and client-secret become
providers/google/default/client-id and client-secret, clients/<id> becomes
clients/<id>/secret, and issuer/state-secret, recovery/password and every other
name keep theirs. Nothing is deleted: remove the v2 parameters once the
installation runs on v3.

The credentials (private/credentials/...) are the Secrets port's: copy them
with 'sluis migrate --from <v1 serve config> --to <v2 serve config>' whose
secrets adapters name the two roots. The exports (export/...) are written again
by the service's next exports pass.

The report is JSON on stdout: the account and region it ran in, and the names,
never a value. Each copy is encrypted with the source parameter's own KMS key
unless --kms-key names one. The credentials are the AWS SDK's own (a profile,
the environment).

`

func migrateSSMLayout(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("sluis migrate ssm-layout", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { _, _ = fmt.Fprint(out, ssmLayoutUsage); fs.PrintDefaults() }
	var o migrate.SSMLayoutOptions
	var region string
	fs.StringVar(&o.From, "from-root", "/sluis", "the layout v2 root")
	fs.StringVar(&o.To, "to-root", "", "the layout v3 root: /sluis/<instance>")
	fs.StringVar(&o.KMSKeyID, "kms-key", "", "the KMS key the copies are encrypted with (default: each parameter's own key)")
	fs.StringVar(&region, "region", "", "the region (default: the AWS SDK's own resolution)")
	fs.BoolVar(&o.DryRun, "dry-run", false, "report what would be copied; write nothing")
	fs.BoolVar(&o.Overwrite, "overwrite", false, "replace a v3 parameter that holds a different value (default: refuse, naming it)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if o.To == "" || fs.NArg() > 0 {
		return fmt.Errorf("%w: sluis migrate ssm-layout takes --to-root /sluis/<instance> and flags only", errUsage)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var loaders []func(*awsconfig.LoadOptions) error
	if region != "" {
		loaders = append(loaders, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, loaders...)
	if err != nil {
		return fmt.Errorf("ssm: %w", err)
	}
	report, err := migrate.MoveSSMLayout(ctx, awsssm.NewFromConfig(cfg), o)
	// Where it ran: what an operator checks before trusting a dry run.
	report.Region = cfg.Region
	if who, idErr := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}); idErr == nil {
		report.Account = aws.ToString(who.Account)
	} else if err == nil {
		err = fmt.Errorf("sts: who is this: %w", idErr)
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if encErr := enc.Encode(report); encErr != nil && err == nil {
		err = encErr
	}
	return err
}
