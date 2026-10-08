package secretstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/truvity/sluis/internal/githubapp/catalogue"
	"github.com/truvity/sluis/storage/state"
)

// External is the public contract: the store rooted at <root>/external. An
// address holds one typed document; being at an external address grants
// nobody anything, the consumer's own side does.
type External struct{ s state.Store }

// NewExternal returns the view of s, which is rooted at <root>/external.
func NewExternal(s state.Store) External { return External{s: s} }

// Store is the store the view is over.
func (e External) Store() state.Store { return e.s }

// OIDC is a confidential client's secret at oidc/<client>: generated, or
// operator-seeded. The token check reads it with Rotating.
func (e External) OIDC(client string) state.Value[OIDCv1] {
	return state.NewValue(e.s, "oidc/"+segment(client), state.Codec[OIDCv1](oidcCodec))
}

// GitHubApp is an installed catalogue GitHub App at github/<name>. A name that
// begins "runner-" is a runner App's and is refused: the Value it returns
// then fails every call with [ErrReservedName], because the runner Apps have
// [External.GitHubRunnerApp].
func (e External) GitHubApp(name string) state.Value[GitHubv1] {
	if err := CheckAppName(name); err != nil {
		return state.NewValue[GitHubv1](refusedStore{err}, "github/refused", githubCodec)
	}
	return e.githubApp(name)
}

// GitHubRunnerApp is a runner App at github/runner-<tier>-<org>.
func (e External) GitHubRunnerApp(tier, org string) state.Value[GitHubv1] {
	return e.githubApp(catalogue.RunnerPrefix + tier + "-" + org)
}

func (e External) githubApp(name string) state.Value[GitHubv1] {
	return state.NewValue(e.s, "github/"+segment(name), state.Codec[GitHubv1](githubCodec))
}

// SlackApp is a catalogue Slack App's bot token at slack/<name>.
func (e External) SlackApp(name string) state.Value[Slackv1] {
	return state.NewValue(e.s, "slack/"+segment(name), state.Codec[Slackv1](slackCodec))
}

// Cloudflare is a preset's current credential at cloudflare/<preset>: a token,
// or for an R2 preset an access key and secret.
func (e External) Cloudflare(preset string) state.Value[Cloudflarev1] {
	return state.NewValue(e.s, "cloudflare/"+segment(preset), state.Codec[Cloudflarev1](cloudflareCodec))
}

// ErrReservedName is a catalogue App named like a runner App.
var ErrReservedName = fmt.Errorf("secretstore: a GitHub App name may not begin %q", catalogue.RunnerPrefix)

// CheckAppName refuses a catalogue GitHub App name that begins "runner-".
func CheckAppName(name string) error {
	if strings.HasPrefix(name, catalogue.RunnerPrefix) {
		return fmt.Errorf("%w: %q", ErrReservedName, name)
	}
	return nil
}

// refusedStore is the store behind a Value whose address was refused: every
// call returns the reason.
type refusedStore struct{ err error }

func (r refusedStore) Get(context.Context, string) (state.Item, error) { return state.Item{}, r.err }
func (r refusedStore) GetRev(context.Context, string, state.Rev) (state.Item, error) {
	return state.Item{}, r.err
}
func (r refusedStore) Put(context.Context, string, []byte, state.Rev) (state.Rev, error) {
	return "", r.err
}
func (r refusedStore) Delete(context.Context, string) error      { return r.err }
func (r refusedStore) List(context.Context) ([]string, error)    { return nil, r.err }
func (r refusedStore) Child(string, ...state.Option) state.Store { return r }
