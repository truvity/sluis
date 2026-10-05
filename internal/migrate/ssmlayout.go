package migrate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/truvity/sluis/internal/secrets"
)

// The SSM layouts the configuration secrets have had (docs/decisions/0036).
// Layout v2 keeps them under /sluis/private/config/, one installation per
// account; layout v3 under /sluis/<instance>/private/config/, with the names
// the documents give them. The credentials (`private/credentials/...`) are the
// Secrets port's and move with `sluis migrate --from --to` through the ports;
// the exports (`export/...`) are made again by the service's next exports pass.

// SSMAPI is the part of the SSM client the layout migration calls.
type SSMAPI interface {
	GetParametersByPath(ctx context.Context, in *awsssm.GetParametersByPathInput, opts ...func(*awsssm.Options)) (*awsssm.GetParametersByPathOutput, error)
	PutParameter(ctx context.Context, in *awsssm.PutParameterInput, opts ...func(*awsssm.Options)) (*awsssm.PutParameterOutput, error)
	DescribeParameters(ctx context.Context, in *awsssm.DescribeParametersInput, opts ...func(*awsssm.Options)) (*awsssm.DescribeParametersOutput, error)
}

// SSMLayoutOptions is one run of the configuration secrets' move to layout v3.
type SSMLayoutOptions struct {
	// From is the v2 root (`/sluis`), To the v3 one (`/sluis/<instance>`).
	From, To string
	// KMSKeyID encrypts what is written. Empty keeps each parameter's own key:
	// a customer key is never silently replaced by the AWS-managed one.
	KMSKeyID string
	// DryRun plans and writes nothing; Overwrite replaces a parameter that
	// is already there with a different value.
	DryRun, Overwrite bool
}

// SSMLayoutReport is what a run did, by v3 name. It holds names, never a value.
type SSMLayoutReport struct {
	// Account and Region are where it ran, as STS and the SDK say: what an
	// operator checks before trusting a dry run.
	Account   string   `json:"account,omitempty"`
	Region    string   `json:"region,omitempty"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	Copied    []string `json:"copied"`
	Unchanged []string `json:"unchanged"`
	Conflicts []string `json:"conflicts,omitempty"`
	DryRun    bool     `json:"dryRun"`
}

// V3ConfigName is the v3 name of a configuration secret v2 kept at
// `<root>/private/config/<v2>`: the Google OAuth client becomes the provider
// `default`, and a client's secret gains its `/secret`. Every other name is
// the same in both.
func V3ConfigName(v2 string) string {
	switch v2 {
	case "oauth/client-id":
		return "providers/google/default/client-id"
	case "oauth/client-secret":
		return "providers/google/default/client-secret"
	}
	if id, ok := strings.CutPrefix(v2, "clients/"); ok && !strings.Contains(id, "/") {
		return "clients/" + id + "/secret"
	}
	return v2
}

const configDir = "/private/config/"

// MoveSSMLayout copies every configuration secret under the v2 root to its
// v3 name under the new root. It writes only where the destination is absent
// or, with Overwrite, different; the source is left as it is, to be deleted by
// the operator once the installation runs on v3.
func MoveSSMLayout(ctx context.Context, api SSMAPI, o SSMLayoutOptions) (SSMLayoutReport, error) {
	report := SSMLayoutReport{From: o.From, To: o.To, Copied: []string{}, Unchanged: []string{}, DryRun: o.DryRun}
	for _, root := range []string{o.From, o.To} {
		if !strings.HasPrefix(root, "/") || strings.HasSuffix(root, "/") {
			return report, fmt.Errorf("ssm layout: root %q must begin with a slash and not end with one", root)
		}
	}
	if o.From == o.To {
		return report, errors.New("ssm layout: the v2 and the v3 root are the same")
	}
	if err := secrets.CheckRoot(o.To); err != nil {
		return report, fmt.Errorf("ssm layout: --to-root: %w", err)
	}
	source, err := readTree(ctx, api, o.From+configDir)
	if err != nil {
		return report, err
	}
	keys := map[string]string{}
	if o.KMSKeyID == "" {
		if keys, err = readKeys(ctx, api, o.From+configDir); err != nil {
			return report, err
		}
	}
	dest, err := readTree(ctx, api, o.To+configDir)
	if err != nil {
		return report, err
	}
	names := make([]string, 0, len(source))
	for name := range source {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		v3 := V3ConfigName(name)
		if have, ok := dest[v3]; ok {
			if have == source[name] {
				report.Unchanged = append(report.Unchanged, v3)
				continue
			}
			if !o.Overwrite {
				report.Conflicts = append(report.Conflicts, v3)
				continue
			}
		}
		report.Copied = append(report.Copied, v3)
		if o.DryRun {
			continue
		}
		_, existed := dest[v3]
		in := &awsssm.PutParameterInput{
			Name: aws.String(o.To + configDir + v3), Value: aws.String(source[name]),
			// Overwrite only what was there when read, and only when asked:
			// otherwise the write is a create, which SSM refuses atomically if
			// somebody wrote the parameter since.
			Type: types.ParameterTypeSecureString, Overwrite: aws.Bool(o.Overwrite && existed),
			Tier: types.ParameterTierIntelligentTiering,
		}
		switch key := keys[name]; {
		case o.KMSKeyID != "":
			in.KeyId = aws.String(o.KMSKeyID)
		case key != "" && key != "alias/aws/ssm":
			in.KeyId = aws.String(key)
		}
		if _, err = api.PutParameter(ctx, in); err != nil {
			return report, fmt.Errorf("ssm layout: writing %s: %w", v3, err)
		}
	}
	if len(report.Conflicts) > 0 {
		return report, fmt.Errorf("ssm layout: %d parameters already hold a different value (rerun with --overwrite to replace them): %s",
			len(report.Conflicts), strings.Join(report.Conflicts, ", "))
	}
	return report, nil
}

// readKeys is the KMS key of every parameter under prefix, by its name below it.
func readKeys(ctx context.Context, api SSMAPI, prefix string) (map[string]string, error) {
	out := map[string]string{}
	var token *string
	for {
		page, err := api.DescribeParameters(ctx, &awsssm.DescribeParametersInput{
			ParameterFilters: []types.ParameterStringFilter{{
				Key: aws.String("Path"), Option: aws.String("Recursive"), Values: []string{strings.TrimSuffix(prefix, "/")},
			}},
			NextToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("ssm layout: describing %s: %w", prefix, err)
		}
		for i := range page.Parameters {
			p := &page.Parameters[i]
			if name, ok := strings.CutPrefix(aws.ToString(p.Name), prefix); ok {
				out[name] = aws.ToString(p.KeyId)
			}
		}
		if page.NextToken == nil || *page.NextToken == "" {
			return out, nil
		}
		token = page.NextToken
	}
}

// readTree reads every parameter under prefix, decrypted, by its name below it.
func readTree(ctx context.Context, api SSMAPI, prefix string) (map[string]string, error) {
	out := map[string]string{}
	var token *string
	for {
		page, err := api.GetParametersByPath(ctx, &awsssm.GetParametersByPathInput{
			Path: aws.String(strings.TrimSuffix(prefix, "/")), Recursive: aws.Bool(true),
			WithDecryption: aws.Bool(true), NextToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("ssm layout: reading %s: %w", prefix, err)
		}
		for _, p := range page.Parameters {
			if name, ok := strings.CutPrefix(aws.ToString(p.Name), prefix); ok {
				out[name] = aws.ToString(p.Value)
			}
		}
		if page.NextToken == nil || *page.NextToken == "" {
			return out, nil
		}
		token = page.NextToken
	}
}
