package secretstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/truvity/sluis/storage/state"
)

// LayoutV5 is the secrets layout whose addresses begin with the module:
// internal/<module>/<name> and external/<module>/<name>. There is no config or
// credentials level below the module. Layout v4 stays readable ([Stores],
// [Secrets]) as the source of the migration and for a rollback.
const LayoutV5 = "v5"

// Module names the part of sluis that owns a secret. A module's view can form
// the module's own addresses only; the type of each view has no method for
// another module's names.
type Module string

// The modules that own secrets.
const (
	ModuleOIDC       Module = "oidc"
	ModuleGitHub     Module = "github"
	ModuleSlack      Module = "slack"
	ModuleCloudflare Module = "cloudflare"
	ModuleGoogle     Module = "google"
	ModuleBackup     Module = "backup"
)

// Modules are all of them, in a fixed order.
func Modules() []Module {
	return []Module{ModuleOIDC, ModuleGitHub, ModuleSlack, ModuleCloudflare, ModuleGoogle, ModuleBackup}
}

// ModuleExternal reports whether m has an external namespace: the secrets it
// hands to consumers outside sluis.
func ModuleExternal(m Module) bool {
	switch m {
	case ModuleOIDC, ModuleGitHub, ModuleSlack, ModuleCloudflare:
		return true
	}
	return false
}

// ParseModule returns the module named s.
func ParseModule(s string) (Module, error) {
	if m := Module(s); slices.Contains(Modules(), m) {
		return m, nil
	}
	return "", fmt.Errorf("secretstore: %q is not a module", s)
}

// StoresV5 are the secrets of one installation on layout v5: a module-scoped
// internal view and, where the module has one, a module-scoped external view.
type StoresV5 struct {
	internal state.Store // <root>/internal
	external state.Store // <root>/external
}

// FromStoreV5 splits a store rooted at <root> into the internal and external
// namespaces. keyAlias, when set, is the encryption key both use.
func FromStoreV5(root state.Store, keyAlias string) *StoresV5 {
	var opts []state.Option
	if keyAlias != "" {
		opts = append(opts, state.WithKeyAlias(keyAlias))
	}
	return &StoresV5{internal: root.Child("internal", opts...), external: root.Child("external", opts...)}
}

// InternalStore is the store rooted at internal/<module>, for a tool that reads
// a module's names it does not know in advance (the migration's plan). A
// module's own code uses its typed view.
func (s *StoresV5) InternalStore(m Module) state.Store { return s.in(m) }

// ExternalStore is the store rooted at external/<module>.
func (s *StoresV5) ExternalStore(m Module) state.Store { return s.out(m) }

func (s *StoresV5) in(m Module) state.Store  { return s.internal.Child(string(m)) }
func (s *StoresV5) out(m Module) state.Store { return s.external.Child(string(m)) }

// OIDC is the issuer's internal view: internal/oidc/....
func (s *StoresV5) OIDC() OIDCInternal { return OIDCInternal{view{ModuleOIDC, s.in(ModuleOIDC)}} }

// GitHub is the GitHub module's internal view: internal/github/....
func (s *StoresV5) GitHub() GitHubInternal {
	return GitHubInternal{view{ModuleGitHub, s.in(ModuleGitHub)}}
}

// Slack is the Slack module's internal view: internal/slack/....
func (s *StoresV5) Slack() SlackInternal { return SlackInternal{view{ModuleSlack, s.in(ModuleSlack)}} }

// Cloudflare is the Cloudflare module's internal view: internal/cloudflare/....
func (s *StoresV5) Cloudflare() CloudflareInternal {
	return CloudflareInternal{view{ModuleCloudflare, s.in(ModuleCloudflare)}}
}

// Google is the Google module's internal view: internal/google/....
func (s *StoresV5) Google() GoogleInternal {
	return GoogleInternal{view{ModuleGoogle, s.in(ModuleGoogle)}}
}

// Backup is the backup module's internal view: internal/backup/....
func (s *StoresV5) Backup() BackupInternal {
	return BackupInternal{view{ModuleBackup, s.in(ModuleBackup)}}
}

// OIDCExternal is external/oidc/<client>, unchanged from layout v4.
func (s *StoresV5) OIDCExternal() OIDCExternal { return OIDCExternal{s.out(ModuleOIDC)} }

// GitHubExternal is external/github/<app id>.
func (s *StoresV5) GitHubExternal() GitHubExternal { return GitHubExternal{s.out(ModuleGitHub)} }

// SlackExternal is external/slack/<app>.
func (s *StoresV5) SlackExternal() SlackExternal { return SlackExternal{s.out(ModuleSlack)} }

// CloudflareExternal is external/cloudflare/<preset>.
func (s *StoresV5) CloudflareExternal() CloudflareExternal {
	return CloudflareExternal{s.out(ModuleCloudflare)}
}

// S3Credentials is the static credential document of an S3-compatible store at
// the internal address ref, `internal/<module>/<name>`: the module is the
// first segment, and it must be one of [Modules].
func (s *StoresV5) S3Credentials(ref string) (state.Value[S3Credentialsv1], error) {
	rest, ok := strings.CutPrefix(ref, "internal/")
	mod, _, _ := strings.Cut(rest, "/")
	m, err := ParseModule(mod)
	if !ok || err != nil {
		return state.Value[S3Credentialsv1]{}, fmt.Errorf("%w: %q is not below internal/<module>/", ErrRef, ref)
	}
	return view{m, s.in(m)}.S3Credentials(ref)
}

// view is the part every internal view shares: the module and the store rooted
// at internal/<module>.
type view struct {
	module Module
	s      state.Store
}

// Module is the module the view belongs to.
func (v view) Module() Module { return v.module }

// Store is the store the view is over, rooted at internal/<module>.
func (v view) Store() state.Store { return v.s }

// Prefix is the module's address below the installation root.
func (v view) Prefix() string { return "internal/" + string(v.module) }

// S3Credentials is the static credential document of an S3-compatible store at
// the internal address ref, which must be in this module (`internal/<module>/<name>`).
// A ref in another module, or an external one, is refused.
func (v view) S3Credentials(ref string) (state.Value[S3Credentialsv1], error) {
	name, err := CheckModuleRef(v.module, ref)
	if err != nil {
		return state.Value[S3Credentialsv1]{}, err
	}
	return state.NewValue(v.s, name, state.Codec[S3Credentialsv1](s3CredentialsCodec)), nil
}

// CheckModuleRef refuses an address that is not `internal/<module>/<name...>`
// of m: another module's, an external one, a path that climbs, or a segment
// that is not a plain name. It returns the address below the module, `<name...>`.
func CheckModuleRef(m Module, ref string) (string, error) {
	rest, ok := strings.CutPrefix(ref, "internal/"+string(m)+"/")
	if !ok || rest == "" || strings.Contains(rest, "..") {
		return "", fmt.Errorf("%w: %q is not below internal/%s/", ErrRef, ref, m)
	}
	for _, seg := range strings.Split(rest, "/") {
		if seg == "" || seg != segment(seg) {
			return "", fmt.Errorf("%w: %q has the segment %q", ErrRef, ref, seg)
		}
	}
	return rest, nil
}

// OIDCInternal is internal/oidc: what only the issuer and console read.
type OIDCInternal struct{ view }

// StateSecret is the issuer's state secret: oidc/state-secret.
func (o OIDCInternal) StateSecret() state.Value[[]byte] {
	return state.NewValue(o.s, "state-secret", state.Raw())
}

// RecoveryPassword is the recovery password: oidc/recovery-password.
func (o OIDCInternal) RecoveryPassword() state.Value[[]byte] {
	return state.NewValue(o.s, "recovery-password", state.Raw())
}

// ConsoleSessionKey is the key the console signs its sessions with:
// oidc/console-session-key.
func (o OIDCInternal) ConsoleSessionKey() state.Value[[]byte] {
	return state.NewValue(o.s, "console-session-key", state.Raw())
}

// SignInClientID is the sign-in provider's client id:
// oidc/signin/<provider>/client-id.
func (o OIDCInternal) SignInClientID(provider string) state.Value[[]byte] {
	return state.NewValue(o.s, "signin/"+segment(provider)+"/client-id", state.Raw())
}

// SignInClientSecret is the sign-in provider's client secret:
// oidc/signin/<provider>/client-secret.
func (o OIDCInternal) SignInClientSecret(provider string) state.Value[[]byte] {
	return state.NewValue(o.s, "signin/"+segment(provider)+"/client-secret", state.Raw())
}

// Client is the internal copy of a client's secret: oidc/clients/<id>. The
// document a relying party reads is [OIDCExternal.Client], not this.
func (o OIDCInternal) Client(id string) state.Value[[]byte] {
	return state.NewValue(o.s, "clients/"+segment(id), state.Raw())
}

// GitHubInternal is internal/github.
type GitHubInternal struct{ view }

// AppCredential is one write of a GitHub App's credential:
// github/apps/<id>/<ref>. A fresh random ref per write keeps the previous
// credential until the new one is in use.
func (g GitHubInternal) AppCredential(id, ref string) state.Value[[]byte] {
	return state.NewValue(g.s, "apps/"+segment(id)+"/"+segment(ref), state.Raw())
}

// DeleteAppCredential removes one write of a GitHub App's credential. An
// absent one is not an error.
func (g GitHubInternal) DeleteAppCredential(ctx context.Context, id, ref string) error {
	return deleteName(ctx, g.s, "apps/"+segment(id)+"/"+segment(ref))
}

// DeleteLinkCredential removes one write of a person's GitHub link tokens. An
// absent one is not an error.
func (g GitHubInternal) DeleteLinkCredential(ctx context.Context, person, ref string) error {
	return deleteName(ctx, g.s, "links/"+segment(person)+"/"+segment(ref))
}

// deleteName removes name from s; an absent one is not an error.
func deleteName(ctx context.Context, s state.Store, name string) error {
	if err := s.Delete(ctx, name); err != nil && !errors.Is(err, state.ErrNotFound) {
		return err
	}
	return nil
}

// LinkCredential is one write of a person's GitHub link tokens:
// github/links/<person>/<ref>. A fresh random ref per write keeps a refresh
// token, which GitHub honours once, from being replaced by a stale writer.
func (g GitHubInternal) LinkCredential(person, ref string) state.Value[[]byte] {
	return state.NewValue(g.s, "links/"+segment(person)+"/"+segment(ref), state.Raw())
}

// PersonToken is a person's token pair for a linked GitHub account:
// github/links/<person>, replaced in place under the store's version history.
func (g GitHubInternal) PersonToken(person string) state.Value[PersonToken] {
	return state.NewValue(g.s, "links/"+segment(person), state.JSON[PersonToken]())
}

// SlackInternal is internal/slack.
type SlackInternal struct{ view }

// WorkspaceCredential is one write of a workspace's credential:
// slack/workspaces/<team>/<ref>.
func (s SlackInternal) WorkspaceCredential(team, ref string) state.Value[[]byte] {
	return state.NewValue(s.s, "workspaces/"+segment(team)+"/"+segment(ref), state.Raw())
}

// AppCredential is one write of a Slack App's credential:
// slack/apps/<id>/<ref>.
func (s SlackInternal) AppCredential(id, ref string) state.Value[[]byte] {
	return state.NewValue(s.s, "apps/"+segment(id)+"/"+segment(ref), state.Raw())
}

// DeleteWorkspaceCredential removes one write of a workspace's credential. An
// absent one is not an error.
func (s SlackInternal) DeleteWorkspaceCredential(ctx context.Context, team, ref string) error {
	return deleteName(ctx, s.s, "workspaces/"+segment(team)+"/"+segment(ref))
}

// DeleteAppCredential removes one write of a Slack App's credential. An absent
// one is not an error.
func (s SlackInternal) DeleteAppCredential(ctx context.Context, id, ref string) error {
	return deleteName(ctx, s.s, "apps/"+segment(id)+"/"+segment(ref))
}

// CloudflareInternal is internal/cloudflare.
type CloudflareInternal struct{ view }

// minterRecordsDir holds the minted-token records, so it cannot be an account.
const minterRecordsDir = "minted"

// Minter is the minter credential of a Cloudflare account:
// cloudflare/<account>/minter, the address layout v4 already used.
func (c CloudflareInternal) Minter(account string) (state.Value[CloudflareMinterv1], error) {
	if account == minterRecordsDir {
		return state.Value[CloudflareMinterv1]{}, fmt.Errorf("%w: %q is not an account name", ErrRef, account)
	}
	return state.NewValue(c.s, segment(account)+"/minter", state.Codec[CloudflareMinterv1](cloudflareMinterCodec)), nil
}

// MinterAt is the minter credential at the address ref names, which the
// service document gives (`internal/cloudflare/<account>/minter`) and which
// must be below internal/cloudflare/.
func (c CloudflareInternal) MinterAt(ref string) (state.Value[CloudflareMinterv1], error) {
	name, err := CheckModuleRef(ModuleCloudflare, ref)
	if err != nil {
		return state.Value[CloudflareMinterv1]{}, err
	}
	return state.NewValue(c.s, name, state.Codec[CloudflareMinterv1](cloudflareMinterCodec)), nil
}

// Minted is the record of the tokens minted for a preset: cloudflare/minted/<preset>.
func (c CloudflareInternal) Minted(preset string) state.Value[CloudflareMinted] {
	return state.NewValue(c.s, minterRecordsDir+"/"+segment(preset), state.JSON[CloudflareMinted]())
}

// GoogleInternal is internal/google.
type GoogleInternal struct{ view }

// WorkspaceKey is a Google Workspace's service-account key:
// google/workspaces/<id>/key.
func (g GoogleInternal) WorkspaceKey(id string) state.Value[[]byte] {
	return state.NewValue(g.s, "workspaces/"+segment(id)+"/key", state.Raw())
}

// DeleteWorkspaceKey removes a Google Workspace's key. An absent one is not an
// error.
func (g GoogleInternal) DeleteWorkspaceKey(ctx context.Context, id string) error {
	return deleteName(ctx, g.s, "workspaces/"+segment(id)+"/key")
}

// BackupInternal is internal/backup.
type BackupInternal struct{ view }

// KeyRef is the reference to the key backups are sealed with, when it is a
// secret and not a KMS alias: backup/keys/<name>.
func (b BackupInternal) KeyRef(name string) state.Value[[]byte] {
	return state.NewValue(b.s, "keys/"+segment(name), state.Raw())
}

// OIDCExternal is external/oidc.
type OIDCExternal struct{ s state.Store }

// Client is a confidential client's secret document at external/oidc/<client>,
// byte-identical to layout v4.
func (e OIDCExternal) Client(client string) state.Value[OIDCv1] {
	return state.NewValue(e.s, segment(client), state.Codec[OIDCv1](oidcCodec))
}

// GitHubExternal is external/github.
type GitHubExternal struct{ s state.Store }

// App is a GitHub App's exported key at external/github/<app id>. One kind:
// a catalogue, link or runner App is the same address shape.
func (e GitHubExternal) App(id string) state.Value[GitHubv1] {
	return state.NewValue(e.s, segment(id), state.Codec[GitHubv1](githubCodec))
}

// DeleteApp removes a GitHub App's exported key. An absent one is not an error.
func (e GitHubExternal) DeleteApp(ctx context.Context, id string) error {
	err := e.s.Delete(ctx, segment(id))
	if err != nil && !errors.Is(err, state.ErrNotFound) {
		return err
	}
	return nil
}

// SlackExternal is external/slack.
type SlackExternal struct{ s state.Store }

// App is a Slack App's bot token at external/slack/<name>.
func (e SlackExternal) App(name string) state.Value[Slackv1] {
	return state.NewValue(e.s, segment(name), state.Codec[Slackv1](slackCodec))
}

// DeleteApp removes a Slack App's exported bot token. An absent one is not an
// error.
func (e SlackExternal) DeleteApp(ctx context.Context, name string) error {
	return deleteName(ctx, e.s, segment(name))
}

// CloudflareExternal is external/cloudflare.
type CloudflareExternal struct{ s state.Store }

// Preset is a preset's current credential at external/cloudflare/<preset>.
func (e CloudflareExternal) Preset(preset string) state.Value[Cloudflarev1] {
	return state.NewValue(e.s, segment(preset), state.Codec[Cloudflarev1](cloudflareCodec))
}
