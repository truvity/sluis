//nolint:lll // messages are prose
package config

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	policyconfig "github.com/truvity/policy/config"
)

// SluisGoogle is the Google module's service document (apiVersion
// sluis.truvity.github.io/sluis-google/v1, schemas/config/sluis-google.schema.json).
// It holds what the module reads and nothing of the issuer, the console, the
// signer or a policy: the storage it keeps its workspaces and the snapshots in
// (`ports`, `secrets`), and the workspaces it reads (`google`).
type SluisGoogle struct {
	APIVersion string `json:"apiVersion"`
	// Instance names the installation, as in every module's document.
	Instance string   `json:"instance,omitempty"`
	Log      *Log     `json:"log,omitempty"`
	Probes   *Address `json:"probes,omitempty"`

	Secrets *Secrets `json:"secrets,omitempty"`
	// Audit is the audit installation the module records to: the connections
	// it makes and the changes it makes to them, as its own workload. Unset
	// only logs what it did.
	Audit    *RosterAudit             `json:"audit,omitempty"`
	Ports    *Ports                   `json:"ports,omitempty"`
	Platform *Platform                `json:"platform,omitempty"`
	Preset   string                   `json:"preset,omitempty"`
	Adapters map[string]AdapterChoice `json:"adapters,omitempty"`
	Google   Google                   `json:"google"`
}

// Google is `google`.
type Google struct {
	// Workspaces are the workspaces the deployment declares.
	Workspaces []DirectoryWorkspace `json:"workspaces,omitempty"`
	// OAuthClient is the client that reopens a workspace connected by consent,
	// and the one the console's consent and the issuer's sign-in use.
	OAuthClient *GoogleOAuthClient `json:"oauthClient,omitempty"`
	// PublicURL is where a browser reaches the issuer, including its mount:
	// the consent and sign-in redirect URIs are built from it. The same value
	// as the issuer's `publicURL`.
	PublicURL string `json:"publicURL,omitempty"`
	// RefreshInterval, FreshnessWindow and ProbeInterval are the hub's knobs.
	RefreshInterval *Duration `json:"refreshInterval,omitempty"`
	FreshnessWindow *Duration `json:"freshnessWindow,omitempty"`
	ProbeInterval   *Duration `json:"probeInterval,omitempty"`
}

// GoogleOAuthClient is `google.oauthClient`: the id (not a secret) or the
// provider whose secrets hold it, and the provider whose client-secret it is.
type GoogleOAuthClient struct {
	ID       string `json:"id,omitempty"`
	Provider string `json:"provider,omitempty"`
	// HostedDomains are the Google Workspace domains a person may sign in
	// from: the sign-in's `hd` claim must be one of them. Unset asks nothing
	// of `hd`.
	HostedDomains []string `json:"hostedDomains,omitempty"`
}

// GoogleAPIVersion is the apiVersion of the Google module's document.
const GoogleAPIVersion = Group + "/sluis-google/v1"

// Name is the installation's name: `instance`, else "sluis".
func (s *SluisGoogle) Name() string {
	if s.Instance != "" {
		return s.Instance
	}
	return "sluis"
}

// Intervals are the hub's, with the defaults the unset ones take (the issuer's).
func (g Google) Intervals() (refresh, freshness, probe time.Duration) {
	pick := func(d *Duration) time.Duration {
		if d == nil {
			return 0
		}
		return d.D()
	}
	return pick(g.RefreshInterval), pick(g.FreshnessWindow), pick(g.ProbeInterval)
}

// Validate refuses what the schema cannot say.
func (s *SluisGoogle) Validate() error {
	r, f, _ := s.Google.Intervals()
	for name, d := range map[string]*Duration{"refreshInterval": s.Google.RefreshInterval, "freshnessWindow": s.Google.FreshnessWindow, "probeInterval": s.Google.ProbeInterval} {
		if d != nil && d.D() <= 0 {
			return fmt.Errorf("google.%s: %q is not a positive duration", name, d.D())
		}
	}
	if r > 0 && f > 0 && f < r {
		return fmt.Errorf("google.freshnessWindow %s is shorter than google.refreshInterval %s: every snapshot would be provisional before the next refresh", f, r)
	}
	if o := s.Google.OAuthClient; o != nil {
		switch {
		case o.Provider == "":
			return errors.New("google.oauthClient.provider: required (it names the client's secret)")
		case strings.TrimSpace(o.ID) != o.ID:
			return errors.New("google.oauthClient.id: has surrounding space")
		}
		for _, d := range o.HostedDomains {
			if d == "" || strings.TrimSpace(d) != d || strings.ContainsAny(d, "@/ ") {
				return fmt.Errorf("google.oauthClient.hostedDomains: %q is not a domain", d)
			}
		}
	}
	seen := map[string]bool{}
	for i := range s.Google.Workspaces {
		w := &s.Google.Workspaces[i]
		key := w.Backend + "/" + strings.ToLower(w.Admin)
		if seen[key] {
			return fmt.Errorf("google.workspaces: %s is declared twice", key)
		}
		seen[key] = true
	}
	return nil
}

// Serve is the part of the document the storage ports are opened from, in the
// shape the shared builders (internal/store, internal/secrets) read.
func (s *SluisGoogle) Serve() *Serve {
	return &Serve{
		APIVersion: APIVersion("serve"), Instance: s.Instance, Log: s.Log, Probes: s.Probes,
		Secrets: s.Secrets, Ports: s.Ports, Platform: s.Platform, Preset: s.Preset, Adapters: s.Adapters,
		Audit: s.auditOf(),
	}
}

// auditOf is the audit installation as the shared builders read it.
func (s *SluisGoogle) auditOf() *Audit {
	if s.Audit == nil {
		return nil
	}
	return &Audit{Writer: s.Audit.Writer, TokenFile: s.Audit.TokenFile}
}

// IsGoogle reports whether the file is the Google module's document, by its
// apiVersion alone.
func IsGoogle(file string) bool {
	doc, err := read(file)
	return err == nil && doc != nil && doc[policyconfig.APIVersionKey] == GoogleAPIVersion
}

// LoadGoogle reads the Google module's document, holds it to its schema and to
// [SluisGoogle.Validate].
func LoadGoogle(file string) (*SluisGoogle, error) {
	var out SluisGoogle
	kind := policyconfig.Kind{Name: Group + "/sluis-google", Version: 1, Schema: schemaFor("sluis-google")}
	if err := policyconfig.LoadKind(file, kind, &out); err != nil {
		return nil, err
	}
	if err := out.Validate(); err != nil {
		return nil, &policyconfig.Error{File: file, Err: err}
	}
	return &out, nil
}

// DeclaredSecrets are the secrets the document declares the module reads: the
// key of every declared workspace and the OAuth client that reopens one
// connected by consent (its id too, unless the document gives it). Every one is
// of a kind the Google module owns; the policy's client secrets and the
// issuer's keys are not named here because the module has no policy and no
// signer.
func (s *SluisGoogle) DeclaredSecrets() []DeclaredSecret {
	var out []DeclaredSecret
	for i := range s.Google.Workspaces {
		w := &s.Google.Workspaces[i]
		out = append(out, DeclaredSecret{Kind: SecretWorkspaceKey, Subject: w.ID, Name: w.KeySecret})
	}
	if o := s.Google.OAuthClient; o != nil && o.Provider != "" {
		if o.ID == "" {
			out = append(out, DeclaredSecret{Kind: SecretSignInClientID, Subject: o.Provider, Name: "providers/google/" + o.Provider + "/client-id"})
		}
		out = append(out, DeclaredSecret{Kind: SecretSignInClientSecret, Subject: o.Provider, Name: "providers/google/" + o.Provider + "/client-secret"})
	}
	slices.SortFunc(out, func(a, b DeclaredSecret) int {
		return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.Subject, b.Subject), strings.Compare(a.Name, b.Name))
	})
	return slices.Compact(out)
}
