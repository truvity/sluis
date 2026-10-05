// Command sluisctl is what a person on a laptop runs.
//
// It exists for one reason that survived the design: the cloud CLI has
// no interactive login. kubectl has kubelogin for the same job; AWS has
// nothing that will open a browser, so somebody has to run the flow once
// and then answer as a credential process. Everything else here shares
// that login's cache and the issuer's address, which is why it is one
// binary with subcommands rather than several tools.
//
// Machines do not run this. A job's exchange is one call to the token
// endpoint that any `curl` can make, and the GitHub Action does it in
// shell.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Exit codes, which are a contract: a script reading them should be able
// to tell "you are not signed in" from "the issuer is down" without
// parsing English.
const (
	exitOK          = 0
	exitUsage       = 2
	exitNotSignedIn = 3
	exitNotGranted  = 4
	exitUnreachable = 5
)

func main() {
	if invokedAsDeprecatedName(os.Args[0]) {
		_, _ = fmt.Fprintln(os.Stderr, deprecationNotice)
	}
	err := run(os.Args[1:])
	if err == nil {
		return
	}
	// An exitCodeError is somebody else's exit code, passed through --
	// bao's, from `sluisctl bao` -- and whatever ran already wrote its
	// own error to stderr. Adding "sluisctl: " in front of nothing would
	// be a blank line of sluisctl's own that nobody asked for.
	if _, ok := codeForExit(err); !ok {
		_, _ = fmt.Fprintln(os.Stderr, "sluisctl: "+err.Error())
	}
	os.Exit(codeFor(err))
}

// deprecationNotice is what the `accessctl` alias says on stderr. The alias is
// this same binary under its old name (a second archive entry, or a symlink):
// the product was renamed to sluis, and the old name is kept for one or two
// releases so that a kubeconfig or an AWS profile that runs it keeps working
// while it is rewritten. stdout is untouched, because a credential helper's
// stdout is what the caller parses.
const deprecationNotice = "accessctl: this command is now called sluisctl; the accessctl name is " +
	"deprecated and will be removed in a later release (run `sluisctl kubeconfig` and " +
	"`sluisctl aws-config` to rewrite what points at it)"

// invokedAsDeprecatedName reports whether the binary was started under its old
// name, however it was reached: a path, a symlink's name, a Windows .exe.
func invokedAsDeprecatedName(arg0 string) bool {
	name := strings.ToLower(filepath.Base(arg0))
	name = strings.TrimSuffix(name, ".exe")
	return name == "accessctl"
}

// usageError is a mistake in what was typed, rather than a failure of
// what was asked for.
type usageError struct{ error }

func badUsage(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

func codeFor(err error) int {
	if code, ok := codeForExit(err); ok {
		return code
	}
	var usage usageError
	switch {
	case errors.As(err, &usage):
		return exitUsage
	case errors.Is(err, errNotSignedIn):
		return exitNotSignedIn
	case errors.Is(err, errNotGranted):
		return exitNotGranted
	case errors.Is(err, errUnreachable):
		return exitUnreachable
	default:
		return 1
	}
}

var (
	// errNotSignedIn is no cached login, or one that has expired. The
	// answer is always `sluisctl login`, so it is worth a code of its
	// own: a wrapper script can run that and retry.
	errNotSignedIn = errors.New("not signed in — run `sluisctl login`")
	// errNotGranted is a refusal by the issuer: the groups this identity
	// holds do not admit it to what it asked for. Retrying will not help
	// and a script should stop.
	errNotGranted = errors.New("that audience is not granted to you")
	// errUnreachable is the issuer being down or unresolvable, which is
	// the one a script SHOULD retry.
	errUnreachable = errors.New("the issuer could not be reached")
)

func run(args []string) error {
	if len(args) == 0 {
		usage(os.Stderr)
		return badUsage("no command")
	}

	switch args[0] {
	case "version", "--version", "-version":
		return versionCommand(args[1:])
	case "login":
		return login(args[1:])
	case "whoami":
		return whoami(args[1:])
	case "exchange":
		return exchange(args[1:])
	case "token":
		return token(args[1:])
	case "github-token":
		return githubTokenCommand(args[1:])
	case "kube-token":
		return kubeToken(args[1:])
	case "aws":
		return awsCredentials(args[1:])
	case "credential":
		return credentialRemoved(args[1:])
	case "bao":
		return bao(args[1:])
	case "r2":
		return r2(args[1:])
	case "psql":
		return psql(args[1:])
	case "pg":
		return pg(args[1:])
	case "ssh":
		return sshCommand(args[1:])
	case "secrets":
		return secretsRemoved()
	case "kubeconfig":
		return kubeconfig(args[1:])
	case "aws-config":
		return awsConfig(args[1:])
	case "setup":
		return setup(args[1:])
	case "policy":
		return policyCommand(args[1:])
	case "help", "-h", "--help":
		usage(os.Stdout)
		return nil
	default:
		usage(os.Stderr)
		return badUsage("%q is not a command", args[0])
	}
}

func usage(to *os.File) {
	_, _ = fmt.Fprint(to, `sluisctl — access for a person on a laptop

  version       this build's own version (also --version, -version)
  login         sign in at the issuer, once, in a browser
  whoami        who you are, and what your groups open
  setup         kubeconfig and aws-config together

  kubeconfig    a context per cluster you are granted
  aws-config    a profile per cloud role you are granted

  token         a token for one audience, on stdout (OpenBAO's login, scripts)
  github-token  a GitHub App installation token, under the catalogue's grants
  kube-token    a Kubernetes exec credential      (run by kubectl)
  aws           an AWS credential process answer  (run by the AWS SDKs)
  bao           authenticate, then run the real bao CLI unchanged (OpenBAO)
  r2            authenticate, then run the real r2broker CLI unchanged (R2)
  psql          authenticate, mint a Postgres client certificate, run psql
  pg            the same, running any command instead of psql
  ssh           known-hosts: trust SSH host CAs before the first connect
  exchange      the raw exchange: a token in, a token for an audience out

  policy render the one policy document an installation reads, from its layers

sluisctl's own flags on bao go BEFORE the bao subcommand; bao's own
(including -namespace) go after it, exactly where bao has always
accepted them.

Exit codes: 0 ok, 2 usage, 3 not signed in, 4 audience or App not granted,
5 issuer unreachable.
`)
}

// secretsRemoved returns the error message for the removed secrets command.
func secretsRemoved() error {
	return fmt.Errorf("sluisctl secrets was removed in v1.30.0: read the values with " +
		"sluisctl bao instead — e.g. `sluisctl bao kv get -ns=<ns> -mount=<mount> -format=env <path> > .env` " +
		"(sluisctl authenticates; bao's own client does everything else), " +
		"or with External Secrets in a cluster — see docs/decisions/0002-mission-boundary-tokens-and-memberships.md")
}

// credentialRemoved returns the error message for the removed
// `sluisctl credential` command, naming the one kind asked for when
// there was one -- ssh, db or client -- and every replacement otherwise.
// docs/decisions/0013-openbao-access-through-the-bao-cli.md.
func credentialRemoved(args []string) error {
	kind := ""
	if len(args) > 0 {
		kind = args[0]
	}
	switch kind {
	case "ssh":
		return fmt.Errorf("sluisctl credential ssh was removed in v1.34.0: use " +
			"`sluisctl bao ssh -mode=ca ...` for an interactive session, or " +
			"`sluisctl bao write -field=signed_key <mount>/sign/<role> public_key=@key.pub > key-cert.pub` " +
			"for scp, git, CI and Ansible — see docs/connect/ssh.md")
	case "db":
		return fmt.Errorf("sluisctl credential db was removed in v1.34.0: use " +
			"`sluisctl psql` or `sluisctl pg -- <command>` instead — see docs/connect/postgresql.md")
	case "client":
		return fmt.Errorf("sluisctl credential client was removed in v1.34.0: use " +
			"`sluisctl bao write <pki mount>/sign/<role> csr=@your.csr` instead " +
			"(openssl req -new -key key.pem -out your.csr for the CSR) — see docs/connect/openbao.md")
	default:
		return fmt.Errorf("sluisctl credential was removed in v1.34.0: ssh → `sluisctl bao ssh -mode=ca ...`; " +
			"db → `sluisctl psql` / `sluisctl pg --`; client → `sluisctl bao write <pki mount>/sign/<role> csr=@...` " +
			"— see docs/decisions/0013-openbao-access-through-the-bao-cli.md")
	}
}
