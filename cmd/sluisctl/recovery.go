package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/truvity/sluis/srp"
	"github.com/truvity/sluis/storage/awsotel"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/smithy-go"
)

// `sluisctl recovery password` reads the recovery password of a Lambda
// installation from SSM with the caller's own AWS credentials and prints it
// decoded, and nothing else. An operator in an outage has no directory to
// sign in through, no issuer to ask and, with the console down, no one to
// tell them that layout v5 stores the password as the secret store's byte
// codec, `{"value":"<base64>"}`, at
// `/sluis/<instance>/internal/oidc/recovery-password`
// (docs/guides/sluis/operate/recover-on-lambda.md).

const (
	recoveryTimeout = 30 * time.Second

	// recoveryPasswordV5 and recoveryPasswordV4 are where the password is on
	// layout v5 (a document) and on layout v4 (the text itself).
	recoveryPasswordV5 = "/sluis/%s/internal/oidc/recovery-password"
	recoveryPasswordV4 = "/sluis/%s/internal/config/recovery/password"
)

// The two accounts that sign in by password (docs/decisions/0076).
const (
	accountRecovery    = "recovery"
	accountConformance = "conformance"
)

// instanceName is an installation's name as the SSM paths spell it.
var instanceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// The seams of a test: the SSM client and whether standard output is a terminal.
var (
	newSSMClient     = realSSMClient
	stdoutIsTerminal = func() bool {
		info, err := os.Stdout.Stat()
		return err == nil && info.Mode()&os.ModeCharDevice != 0
	}
)

// parameterAPI is what the command uses of SSM.
type parameterAPI interface {
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, opts ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

func realSSMClient(ctx context.Context, profile, region string) (parameterAPI, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load the AWS configuration: %w", err)
	}
	return ssm.NewFromConfig(cfg, func(o *ssm.Options) { o.TracerProvider, o.MeterProvider = awsotel.Providers() }), nil
}

const recoveryUsage = `Usage: sluisctl recovery password --instance <name> [--account recovery|conformance] [--region R] [--profile P] [--show]
       sluisctl recovery verifier [--account recovery|conformance]

Reads the recovery password of a Lambda installation with your own AWS
credentials (the SDK's default chain) and prints it, decoded, and nothing else.
The parameter is /sluis/<instance>/internal/oidc/recovery-password (layout v5),
a document {"value":"<base64>"}; an installation still on layout v4 keeps the
text at /sluis/<instance>/internal/config/recovery/password, which is read when
the v5 parameter does not exist.

--account conformance reads the conformance test account's password instead,
/sluis/<instance>/internal/oidc/conformance-password (layout v5 only), which a
stack makes when conformance.enabled is set.

"recovery verifier" is for an installation that is not a Pulumi stack: it reads
a password from standard input and prints the account's SRP credential, the
salt and the verifier the service holds in place of the password
(docs/decisions/0076), to store as the secret conformance.verifierSecret or
recovery.verifierSecret names.

Standard output must not be a terminal: pipe it (| pbcopy) or pass --show.
  --instance  the installation's name (else $SLUISCTL_INSTANCE, else backup.instance in config.yaml)
  --account   recovery (the default) or conformance
  --region    the AWS region (else the configuration's, else backup.region in config.yaml)
  --profile   the AWS profile
  --show      print to a terminal as well
`

func recoveryCommand(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		_, _ = fmt.Fprint(os.Stderr, recoveryUsage)
		return badUsage("recovery needs password")
	}
	switch args[0] {
	case "password":
		return recoveryPassword(args[1:], stdout)
	case "verifier":
		return recoveryVerifier(args[1:], os.Stdin, stdout)
	}
	return badUsage("recovery %q is not password or verifier", args[0])
}

func recoveryPassword(args []string, out io.Writer) error {
	flags := newFlags("recovery password", recoveryUsage)
	instance := flags.String("instance", "", "the installation's name")
	region := flags.String("region", "", "the AWS region")
	profile := flags.String("profile", "", "the AWS profile")
	show := flags.Bool("show", false, "print to a terminal as well")
	account := flags.String("account", accountRecovery, "recovery or conformance")
	rest, err := parseInterleaved(flags, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return badUsage("recovery password takes no argument, not %q", rest[0])
	}
	if *account != accountRecovery && *account != accountConformance {
		return badUsage("--account %q is neither recovery nor conformance", *account)
	}
	// Refuse before anything is read: a password on a screen is in scrollback,
	// a shared session and a screen recording.
	if !*show && stdoutIsTerminal() {
		return badUsage("refusing to print the recovery password to a terminal: pipe it (sluisctl recovery password --instance <name> | pbcopy) or pass --show")
	}
	file, err := readConfigFile()
	if err != nil {
		return err
	}
	name := firstNonEmpty(*instance, settingEnv(envInstance), file.Backup.Instance)
	if name == "" {
		return badUsage("no instance: pass --instance, set $%s, or set backup.instance in %s", envInstance, configFileHint())
	}
	if !instanceName.MatchString(name) {
		return badUsage("--instance %q is not an installation name", name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), recoveryTimeout)
	defer cancel()
	client, err := newSSMClient(ctx, *profile, firstNonEmpty(*region, file.Backup.Region))
	if err != nil {
		return err
	}
	var password string
	if *account == accountConformance {
		password, err = readConformancePassword(ctx, client, name)
	} else {
		password, err = readRecoveryPassword(ctx, client, name)
	}
	if err != nil {
		return explain(err)
	}
	_, err = fmt.Fprintln(out, password)
	return err
}

// conformancePasswordV5 is where the conformance account's password is, on
// layout v5.
const conformancePasswordV5 = "/sluis/%s/internal/oidc/conformance-password"

// readConformancePassword reads the conformance account's password.
func readConformancePassword(ctx context.Context, client parameterAPI, instance string) (string, error) {
	name := fmt.Sprintf(conformancePasswordV5, instance)
	text, err := getParameter(ctx, client, name)
	if err != nil {
		if isParameterNotFound(err) {
			return "", fmt.Errorf("%s does not exist: wrong --instance or --region, or conformance.enabled is not set on this stack", name)
		}
		return "", fmt.Errorf("read %s: %w", name, err)
	}
	return decodeRecoveryPassword(text)
}

// recoveryVerifier prints the SRP credential of a password read from in: the
// document an installation that is not a Pulumi stack stores under
// recovery.verifierSecret or conformance.verifierSecret.
func recoveryVerifier(args []string, in io.Reader, out io.Writer) error {
	flags := newFlags("recovery verifier", recoveryUsage)
	account := flags.String("account", accountRecovery, "recovery or conformance")
	rest, err := parseInterleaved(flags, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return badUsage("recovery verifier takes no argument, not %q", rest[0])
	}
	if *account != accountRecovery && *account != accountConformance {
		return badUsage("--account %q is neither recovery nor conformance", *account)
	}
	raw, err := io.ReadAll(io.LimitReader(in, 4<<10))
	if err != nil {
		return fmt.Errorf("read the password: %w", err)
	}
	password := strings.TrimRight(string(raw), "\r\n")
	if password == "" {
		return badUsage("the password on standard input is empty")
	}
	_, err = fmt.Fprintln(out, string(srp.Default.NewCredential(*account, password).Document()))
	return err
}

// readRecoveryPassword reads the password from the layout v5 parameter and,
// when there is none, from layout v4's.
func readRecoveryPassword(ctx context.Context, client parameterAPI, instance string) (string, error) {
	v5 := fmt.Sprintf(recoveryPasswordV5, instance)
	text, err := getParameter(ctx, client, v5)
	if err != nil {
		if !isParameterNotFound(err) {
			return "", fmt.Errorf("read %s: %w", v5, err)
		}
		v4 := fmt.Sprintf(recoveryPasswordV4, instance)
		text, err = getParameter(ctx, client, v4)
		if err != nil {
			if isParameterNotFound(err) {
				return "", fmt.Errorf("neither %s nor %s exists: wrong --instance or --region, or recovery was never configured", v5, v4)
			}
			return "", fmt.Errorf("read %s: %w", v4, err)
		}
	}
	return decodeRecoveryPassword(text)
}

func getParameter(ctx context.Context, client parameterAPI, name string) (string, error) {
	out, err := client.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(name), WithDecryption: aws.Bool(true)})
	if err != nil {
		return "", err
	}
	if out.Parameter == nil || out.Parameter.Value == nil {
		return "", fmt.Errorf("%s has no value", name)
	}
	return *out.Parameter.Value, nil
}

func isParameterNotFound(err error) bool {
	var api smithy.APIError
	return errors.As(err, &api) && api.ErrorCode() == "ParameterNotFound"
}

// decodeRecoveryPassword is the password in a parameter's text: the secret
// store's document `{"value":"<base64>"}` of layout v5, or the text itself,
// which layout v4 keeps (a generated password is never a JSON object).
func decodeRecoveryPassword(text string) (string, error) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		if trimmed == "" {
			return "", errors.New("the recovery password parameter is empty")
		}
		return trimmed, nil
	}
	var doc struct {
		Value *string `json:"value"`
	}
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil || doc.Value == nil {
		return "", errors.New("the recovery password parameter is a document without a \"value\"")
	}
	raw, err := base64.StdEncoding.DecodeString(*doc.Value)
	if err != nil {
		return "", fmt.Errorf("the recovery password's \"value\" is not base64: %w", err)
	}
	if len(raw) == 0 {
		return "", errors.New("the recovery password parameter is empty")
	}
	return string(raw), nil
}
