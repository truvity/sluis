// Package deploycheck verifies, at deploy time, that every secret a service
// document and its policy declare is in the installation's SSM, so that a
// missing or mistyped one is found by the deploy and not by the first person
// to sign in (secrets are read when first used, not at start).
//
// [config.DeclaredSecrets] says WHAT is declared; this package says WHERE each
// is on the layout the installation uses (v4 or v5), reads it, and judges it:
// present, not empty, and for the state secret of the right format. The
// report holds names, versions and fixed reasons. It never holds a value, and
// no error it returns wraps one.
//
// What sluis makes at run time is not checked: a name no document gives cannot
// be declared before it exists (the console's session key, an App's
// credential).
package deploycheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/secretstore"
)

// Entry is one parameter as the reader returns it.
type Entry struct {
	// Value is the parameter's content. It is judged and dropped.
	Value []byte
	// Version is the parameter's version.
	Version int64
}

// Reader reads one parameter by its absolute name. An absent one is
// (Entry{}, false, nil).
type Reader interface {
	Read(ctx context.Context, name string) (Entry, bool, error)
}

// Result is one declared secret's verdict.
type Result struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject,omitempty"`
	// Address is the parameter's absolute name.
	Address string `json:"address"`
	Version int64  `json:"version,omitempty"`
	OK      bool   `json:"ok"`
	// Problem is one of a fixed set of phrases; never a value.
	Problem string `json:"problem,omitempty"`
}

// Report is the verdict on every declared secret.
type Report struct {
	Layout  string   `json:"layout"`
	Root    string   `json:"root"`
	Results []Result `json:"results"`
	OK      bool     `json:"ok"`
}

// Failed is the results that are not OK.
func (r Report) Failed() []Result {
	var out []Result
	for _, res := range r.Results {
		if !res.OK {
			out = append(out, res)
		}
	}
	return out
}

// Err is nil for a report that is OK, and otherwise names every failed address
// and why.
func (r Report) Err() error {
	failed := r.Failed()
	if len(failed) == 0 {
		return nil
	}
	parts := make([]string, len(failed))
	for i, f := range failed {
		parts[i] = f.Address + ": " + f.Problem
	}
	return fmt.Errorf("%d of %d declared secrets are missing or invalid: %s", len(failed), len(r.Results), strings.Join(parts, "; "))
}

// Write prints the report for a person: one line per secret, with its address
// and version.
func (r Report) Write(w io.Writer) {
	_, _ = fmt.Fprintf(w, "declared secrets under %s (layout %s)\n", r.Root, r.Layout)
	for _, res := range r.Results {
		if res.OK {
			_, _ = fmt.Fprintf(w, "  ok       %s  version %d\n", res.Address, res.Version)
		} else {
			_, _ = fmt.Fprintf(w, "  FAILED   %s  %s\n", res.Address, res.Problem)
		}
	}
	if r.OK {
		_, _ = fmt.Fprintf(w, "%d declared secrets present\n", len(r.Results))
	} else {
		_, _ = fmt.Fprintf(w, "%d of %d declared secrets missing or invalid\n", len(r.Failed()), len(r.Results))
	}
}

// The problems a result can have. Fixed phrases: a reason never quotes content.
const (
	problemMissing   = "missing"
	problemEmpty     = "empty"
	problemMalformed = "not the stored shape (expected a layout v5 value document)"
	problemNoAddress = "the document does not say where it is"
)

// Layout checks the layout a `secrets` section names; empty is v4.
func Layout(s *config.Secrets) (string, error) {
	switch {
	case s == nil || s.Source != secrets.KindSSM:
		return "", errors.New("secrets.source is not ssm: the check reads SSM parameters, and this installation keeps its secrets elsewhere")
	case s.Layout == "" || s.Layout == config.SecretsLayoutV4:
		return config.SecretsLayoutV4, nil
	case s.Layout == config.SecretsLayoutV5:
		return config.SecretsLayoutV5, nil
	}
	return "", fmt.Errorf("secrets.layout: %q is neither %s nor %s", s.Layout, config.SecretsLayoutV4, config.SecretsLayoutV5)
}

// Address is the address below the installation's root where layout puts the
// secret, or "" when the document does not give enough to say (a v5 workspace
// key needs the workspace's id, a v4 name needs the name).
func Address(s config.DeclaredSecret, layout string) string {
	if s.Ref {
		return s.Name
	}
	if layout != config.SecretsLayoutV5 {
		if s.Name == "" {
			return ""
		}
		return "internal/config/" + s.Name
	}
	seg := secretstore.Segment
	switch s.Kind {
	case config.SecretRecoveryPassword:
		return "internal/oidc/recovery-password"
	case config.SecretStateSecret:
		return "internal/oidc/state-secret"
	case config.SecretSignInClientID:
		return "internal/oidc/signin/" + seg(s.Subject) + "/client-id"
	case config.SecretSignInClientSecret:
		return "internal/oidc/signin/" + seg(s.Subject) + "/client-secret"
	case config.SecretClient:
		return "internal/oidc/clients/" + seg(s.Subject)
	case config.SecretWorkspaceKey:
		if s.Subject == "" {
			return ""
		}
		return "internal/google/workspaces/" + seg(s.Subject) + "/key"
	}
	return ""
}

// Run reads every declared secret under root and judges it. It makes one read
// per secret and stops for none: the report is the whole picture, so one deploy
// shows every name to fix.
func Run(ctx context.Context, rd Reader, root, layout string, declared []config.DeclaredSecret) Report {
	root = strings.TrimSuffix(root, "/")
	rep := Report{Layout: layout, Root: root, OK: true}
	for _, d := range declared {
		res := Result{Kind: d.Kind, Subject: d.Subject}
		if addr := Address(d, layout); addr == "" {
			res.Problem = problemNoAddress
		} else {
			res.Address = root + "/" + addr
			judge(ctx, rd, d, layout, &res)
		}
		if !res.OK {
			rep.OK = false
		}
		rep.Results = append(rep.Results, res)
	}
	return rep
}

// judge reads the secret and fills in the verdict.
func judge(ctx context.Context, rd Reader, d config.DeclaredSecret, layout string, res *Result) {
	e, found, err := rd.Read(ctx, res.Address)
	switch {
	case err != nil:
		// The reader's errors name the parameter and the platform's refusal;
		// they hold no content.
		res.Problem = "not readable: " + oneLine(err.Error())
		return
	case !found:
		res.Problem = problemMissing
		return
	}
	res.Version = e.Version
	value, ok := content(d, layout, e.Value)
	switch {
	case !ok:
		res.Problem = problemMalformed
		return
	case len(bytes.TrimSpace(value)) == 0:
		res.Problem = problemEmpty
		return
	}
	if d.Kind == config.SecretStateSecret {
		if _, err := secrets.ParseStateSecret(value); err != nil {
			// The reasons ParseStateSecret gives are fixed phrases.
			res.Problem = err.Error()
			return
		}
	}
	res.OK = true
}

// content is the secret's own bytes. A layout v4 name below internal/config is
// plain text. Every other parameter is a JSON document: the raw kinds of layout
// v5 keep {"value": "<base64>"}, and a credential document (a Cloudflare minter,
// S3 credentials) is itself the object.
func content(d config.DeclaredSecret, layout string, stored []byte) ([]byte, bool) {
	if !d.Ref && layout != config.SecretsLayoutV5 {
		return stored, true
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(stored, &obj); err != nil {
		return nil, false
	}
	if d.Ref {
		// A document with no field is an empty one.
		if len(obj) == 0 {
			return nil, true
		}
		return stored, true
	}
	var raw struct {
		Value []byte `json:"value"`
	}
	if err := json.Unmarshal(stored, &raw); err != nil {
		return nil, false
	}
	return raw.Value, true
}

func oneLine(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
}

// Describe is a one-line summary for a log: the counts and no names.
func (r Report) Describe() string {
	return strconv.Itoa(len(r.Results)-len(r.Failed())) + " of " + strconv.Itoa(len(r.Results)) + " declared secrets present"
}
