package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// kubeconfig writes a context per cluster this identity is granted.
//
// It writes through `kubectl config`, not by editing the file: a
// kubeconfig is somebody's own, with contexts this tool knows nothing
// about, and rewriting it wholesale is how a person loses the cluster
// they were using this morning.
func kubeconfig(args []string) error {
	cfg, token, err := signedIn(args, "kubeconfig")
	if err != nil {
		return err
	}
	grants, err := grantsOf(context.Background(), cfg, token.AccessToken)
	if err != nil {
		return err
	}

	binary, err := os.Executable()
	if err != nil {
		// Falling back to the name on PATH is right: a person who ran
		// `sluisctl` has one, and an absolute path is only nicer.
		binary = "sluisctl"
	}

	// A user left by a release that still called itself accessctl is
	// renamed first, so that its contexts end up on the new name.
	if err = renameLegacyKubeUsers(binary, cfg.Issuer); err != nil {
		return err
	}

	written := 0
	for _, one := range grants {
		name, ok := strings.CutPrefix(one.Audience, "k8s:")
		if !ok {
			continue
		}
		user := kubeUserPrefix + name
		// The exec plugin, which kubectl runs when it needs a token. The
		// audience is the cluster's client id, so one plugin serves
		// every cluster and nothing is duplicated per context.
		if err = kubectl(append([]string{"config", "set-credentials", user},
			credentialArgs(binary, one.Audience, cfg.Issuer)...)...); err != nil {
			return err
		}
		if err = kubectl("config", "set-context", name, "--user="+user, "--cluster="+name); err != nil {
			return err
		}
		written++
		_, _ = fmt.Fprintf(stdout, "context %s\n", name)
	}

	if written == 0 {
		_, _ = fmt.Fprintln(stdout, "No clusters are granted to you.")
		return nil
	}
	// The cluster entry itself — the API server's address and its CA —
	// is not ours to write: it comes from the platform's own kubeconfig
	// or from `aws eks update-kubeconfig`, and inventing one would be
	// inventing an address to trust.
	_, _ = fmt.Fprintln(stdout, "\nEach context expects a cluster entry of the same name.")
	return nil
}

// execAPIVersion is what the contexts declare. kubectl passes its own
// choice at run time and the plugin answers that; this is only what the
// entry is created with.
const execAPIVersion = "client.authentication.k8s.io/v1"

// credentialArgs is the `kubectl config set-credentials` tail for one
// cluster, separated from the call so it can be asserted on: every
// context this tool writes is only as good as this list, and the cost of
// one wrong entry is a context that cannot authenticate at all.
func credentialArgs(binary, audience, issuer string) []string {
	return []string{
		"--exec-api-version=" + execAPIVersion,
		"--exec-command=" + binary,
		"--exec-arg=kube-token",
		"--exec-arg=--audience=" + audience,
		"--exec-arg=--issuer=" + issuer,
		// REQUIRED, and its absence is why every context this tool wrote
		// was unusable. `interactiveMode` is optional in
		// client.authentication.k8s.io/v1beta1, which defaults it, and
		// MANDATORY in v1 -- which is what execAPIVersion above declares.
		// kubectl refuses the entry outright:
		//
		//   error: interactiveMode must be specified for <user> to use
		//   exec authentication plugin
		//
		// and refuses it before running the plugin, so the failure looks
		// like a broken tool rather than a missing field.
		//
		// `Never` rather than IfAvailable because it is the truth:
		// kube-token loads a cached sign-in, exchanges it and prints the
		// credential. It never prompts and never opens a browser -- an
		// expired sign-in is an error telling the person to run
		// `sluisctl login`. Never also means kubectl runs the plugin
		// whether or not stdin exists, which is what makes the same
		// kubeconfig work unchanged in CI.
		"--exec-interactive-mode=Never",
	}
}

// kubeUserPrefix starts the kubeconfig user name of a cluster, and
// legacyKubeUserPrefix is what it started with before the rename (ADR 0035).
const (
	kubeUserPrefix       = "sluisctl:"
	legacyKubeUserPrefix = "accessctl:"
)

// runKubectl runs kubectl and returns its stdout. A variable so that a test
// can stand a kubeconfig in memory in its place.
var runKubectl = func(args ...string) ([]byte, error) {
	command := exec.Command("kubectl", args...) //nolint:gosec // arguments this tool built
	command.Stderr = os.Stderr
	return command.Output()
}

func kubectl(args ...string) error {
	if _, err := runKubectl(args...); err != nil {
		return fmt.Errorf("kubectl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// kubeView is the part of `kubectl config view --raw -o json` the rename reads.
type kubeView struct {
	Contexts []struct {
		Name    string `json:"name"`
		Context struct {
			User string `json:"user"`
		} `json:"context"`
	} `json:"contexts"`
	Users []struct {
		Name string `json:"name"`
		User struct {
			Exec struct {
				Args []string `json:"args"`
			} `json:"exec"`
		} `json:"user"`
	} `json:"users"`
}

// renameLegacyKubeUsers renames every `accessctl:<cluster>` user in the target
// kubeconfig (the one kubectl would write to) to `sluisctl:<cluster>`.
//
// kubectl cannot rename a user, so the new one is written first, with the
// current binary and issuer, then each context that named the old user is
// pointed at it, and only then is the old user removed: a failure part way
// leaves every context on a user that exists. Only users that run `kube-token`
// are touched. With none left to rename it does nothing, so it is safe to run
// every time.
func renameLegacyKubeUsers(binary, issuer string) error {
	out, err := runKubectl("config", "view", "--raw", "-o", "json")
	if err != nil {
		return fmt.Errorf("kubectl config view: %w", err)
	}
	var view kubeView
	if err = json.Unmarshal(out, &view); err != nil {
		return fmt.Errorf("parse the kubeconfig: %w", err)
	}
	for _, user := range view.Users {
		cluster, ok := strings.CutPrefix(user.Name, legacyKubeUserPrefix)
		if !ok || cluster == "" || !slices.Contains(user.User.Exec.Args, "kube-token") {
			continue
		}
		renamed := kubeUserPrefix + cluster
		// What the user was set up for is kept; the command is the running one.
		audience, userIssuer := "k8s:"+cluster, issuer
		for _, arg := range user.User.Exec.Args {
			if v, ok := strings.CutPrefix(arg, "--audience="); ok && v != "" {
				audience = v
			}
			if v, ok := strings.CutPrefix(arg, "--issuer="); ok && v != "" {
				userIssuer = v
			}
		}
		if err = kubectl(append([]string{"config", "set-credentials", renamed},
			credentialArgs(binary, audience, userIssuer)...)...); err != nil {
			return err
		}
		for _, one := range view.Contexts {
			if one.Context.User != user.Name {
				continue
			}
			if err = kubectl("config", "set-context", one.Name, "--user="+renamed); err != nil {
				return err
			}
		}
		if err = kubectl("config", "unset", "users."+user.Name); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(os.Stderr, "sluisctl: renamed the kubeconfig user %s to %s\n", user.Name, renamed)
	}
	return nil
}

// awsConfig writes a profile per cloud role this identity is granted.
//
// Appended to `~/.aws/config` rather than written over it, for the same
// reason kubeconfig goes through kubectl: that file is the person's own.
// A profile this tool already wrote is replaced; anything else is left
// exactly as it was.
func awsConfig(args []string) error {
	cfg, token, err := signedIn(args, "aws-config")
	if err != nil {
		return err
	}
	access, err := accessOf(context.Background(), cfg, token.AccessToken)
	if err != nil {
		return err
	}
	grants := access.Grants

	binary, err := os.Executable()
	if err != nil {
		binary = "sluisctl"
	}

	var profiles strings.Builder
	written := 0
	for _, one := range grants {
		rest, ok := strings.CutPrefix(one.Audience, "aws:")
		if !ok {
			continue
		}
		account, role, split := strings.Cut(rest, ":")
		if !split || account == "" || role == "" {
			continue
		}
		// `<role>@<account>`, which reads the way a person says it and
		// sorts by role rather than by a number nobody remembers.
		name := role + "@" + account
		_, _ = fmt.Fprintf(&profiles, "\n[profile %s]\n", name)
		_, _ = fmt.Fprintf(&profiles, "credential_process = %s aws --audience %s --issuer %s\n",
			binary, one.Audience, cfg.Issuer)
		written++
		_, _ = fmt.Fprintf(stdout, "profile %s\n", name)
	}

	r2Profiles, r2Names := r2AWSProfiles(binary, cfg.Issuer, access.Cloudflare)
	profiles.WriteString(r2Profiles)
	for _, name := range r2Names {
		_, _ = fmt.Fprintf(stdout, "profile %s\n", name)
	}
	written += len(r2Names)

	if written == 0 {
		_, _ = fmt.Fprintln(stdout, "No cloud roles are granted to you.")
		return nil
	}

	path, err := awsConfigPath()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	existing, err := os.ReadFile(path) //nolint:gosec // the caller's own AWS configuration
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	body := strip(string(existing)) + marker + profiles.String() + endMarker + "\n"
	if err = os.WriteFile(path, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	_, _ = fmt.Fprintf(stdout, "\nWritten to %s.\n", path)
	return nil
}

// The block this tool owns. Everything between the two markers is
// rewritten each time; everything outside them is somebody else's and is
// never touched.
const (
	marker    = "# >>> sluisctl >>>"
	endMarker = "# <<< sluisctl <<<"
)

// strip removes a block this tool wrote before, so profiles for roles
// somebody no longer holds do not survive as entries that fail only when
// used.
func strip(body string) string { return stripBlock(body, marker, endMarker) }

// stripBlock is the same for any pair of markers: the AWS configuration
// has one block, and a libpq service file has one per service
// (credential_pki.go), which is the same rewriting done twice.
func stripBlock(body, opens, closes string) string {
	start := strings.Index(body, opens)
	if start >= 0 {
		if end := strings.Index(body[start:], closes); end >= 0 {
			body = body[:start] + body[start+end+len(closes):]
		} else {
			// A half-written block: interrupted, or edited by hand. What
			// follows it is ours too, so it goes with it rather than
			// being left to merge into the next one.
			body = body[:start]
		}
	}
	// Exactly one trailing newline, whatever was there. Without this
	// every run adds a blank line to a file people read, which is a
	// small wrongness that accumulates into an obvious one.
	body = strings.TrimRight(body, "\n\t ")
	if body == "" {
		return ""
	}
	return body + "\n"
}

func awsConfigPath() (string, error) {
	if named := strings.TrimSpace(os.Getenv("AWS_CONFIG_FILE")); named != "" {
		return named, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find the home directory: %w", err)
	}
	return filepath.Join(home, ".aws", "config"), nil
}

// setup is both of the above, for somebody who has just signed in.
func setup(args []string) error {
	if err := kubeconfig(args); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout)
	return awsConfig(args)
}
