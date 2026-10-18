package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/srpauth"
	"github.com/truvity/sluis/internal/throttle"
	"github.com/truvity/sluis/srp"
)

// Recovery is the way in when the ordinary one is broken: nobody is in the
// operators group, the group was renamed, the directory will not answer.
//
// It has two shapes because the honest answer depends on where the hub
// runs. In a cluster there is already an authority that says who is
// trusted — the API server — so recovery proves access to it and the hub
// stores no credential at all. Anywhere else there is nothing to prove
// access to, so a generated password is the only thing left.
type Recovery interface {
	// Kind is "token" or "password": what the recovery page asks for.
	Kind() string
	// Prompt is how the sign-in page explains this shape. Each shape
	// carries its own words, because the two are not variations on one
	// sentence: one is a command to run, the other a secret to have kept.
	Prompt() Prompt
}

// ProofRecovery is a recovery whose proof is presented whole: a token. A
// password is not: it is proved by an exchange that never carries it
// ([PasswordRecovery], docs/decisions/0076), so there is no form field, JSON
// body or bearer that takes one.
type ProofRecovery interface {
	Recovery
	// Verify returns the identity the proof establishes.
	Verify(ctx context.Context, proof string) (string, error)
}

// Prompt is what the sign-in page shows above the recovery field.
type Prompt struct {
	// Label names the field.
	Label string
	// Intro is the sentence before it.
	Intro string
	// Command, when set, is shown as a command to run. It carries this
	// installation's own names rather than placeholders.
	Command string
	// Caution is the sentence after it, and is the reason showing any of
	// this is safe to do on a page anyone may load.
	Caution string
}

// ErrRecoveryRefused is returned for a proof that does not check out. It
// is one error for every reason: the caller is unauthenticated, and
// telling it which would help it guess.
var ErrRecoveryRefused = errors.New("server: recovery refused")

// ErrRecoveryThrottled is returned when too many proofs have been refused
// recently. It is distinct because it is not a statement about the proof.
var ErrRecoveryThrottled = errors.New("server: too many attempts")

// recoveryEnabled reports whether a deployment has a recovery path at all.
func recoveryEnabled(r Recovery) bool { return r != nil }

// recoveryLocationOf is where a password recovery's password is kept, as the
// secrets source describes it; empty for any other recovery, or for one whose
// password was generated at start.
func recoveryLocationOf(r Recovery) string {
	if p, ok := r.(*PasswordRecovery); ok && p != nil {
		return p.Where
	}
	return ""
}

// recoveryKindOf is Kind for a possibly absent recovery.
func recoveryKindOf(r Recovery) string {
	if r == nil {
		return ""
	}
	return r.Kind()
}

// ------------------------------------------------------------- by token

// TokenRecovery proves access to the cluster the hub runs in.
//
// Nothing is stored: no password, no digest, no Secret to rotate or to
// find in a backup. What may be presented is a ServiceAccount token
// minted for one audience and a few minutes, so the authority is the
// cluster's own RBAC — who may create a token for that account — which is
// where cluster privilege is supposed to be visible, is revocable by
// removing a binding, and lands in the cluster's audit log.
//
// It also names who recovered. A shared password makes every recovery
// look like the same person.
type TokenRecovery struct {
	// Review is [kube.Client.ReviewToken].
	Review func(ctx context.Context, token string, audiences []string) (string, error)
	// Namespace and Account are what the sign-in page tells a person to
	// mint a token for. They are object names, not secrets: they are
	// visible to anyone who may read the namespace, the chart that
	// creates them is public, and none of it helps without the RBAC to
	// create a token — which is itself enough to reach the hub by other
	// means. Printing the real ones beats making somebody guess a release
	// name during an outage.
	Namespace string
	Account   string
	// Audience the token must have been minted for.
	Audience string
	// Subjects that may recover, as the API server spells them.
	Subjects []string
}

var _ ProofRecovery = (*TokenRecovery)(nil)

// Kind implements [Recovery].
func (t *TokenRecovery) Kind() string { return "token" }

// Prompt implements [Recovery]: the command that mints a proof, with this
// installation's own names in it.
func (t *TokenRecovery) Prompt() Prompt {
	return Prompt{
		Label:   "Recovery token",
		Intro:   "Mint a short-lived token proving access to this cluster:",
		Command: t.command(),
		// The command is an instruction to produce a credential that
		// grants operator here, printed on a page anyone may load. The
		// names in it are not secrets and are useless without the RBAC to
		// mint the token — but a person who *has* that RBAC could be
		// talked into running it on somebody else's behalf, so the page
		// says so plainly.
		Caution: "This mints a credential that grants operator on this hub. " +
			"Never run it because someone asked you to.",
	}
}

func (t *TokenRecovery) command() string {
	namespace, account := t.Namespace, t.Account
	if namespace == "" {
		namespace = "<namespace>"
	}
	if account == "" {
		account = "<service-account>"
		if len(t.Subjects) == 1 {
			if _, name, found := strings.Cut(strings.TrimPrefix(t.Subjects[0], "system:serviceaccount:"), ":"); found {
				account = name
			}
		}
	}
	return fmt.Sprintf("kubectl -n %s create token %s \\\n  --audience %s --duration 10m",
		namespace, account, t.Audience)
}

// Verify implements [Recovery].
func (t *TokenRecovery) Verify(ctx context.Context, proof string) (string, error) {
	subject, err := t.Review(ctx, proof, []string{t.Audience})
	if err != nil {
		// A review that could not run is not a refusal, and must not read
		// as one: an unreachable API server is an outage to report, not a
		// wrong token to try again.
		return "", err
	}
	if !slices.Contains(t.Subjects, subject) {
		return "", fmt.Errorf("%w: %s may not recover this hub", ErrRecoveryRefused, subject)
	}
	return subject, nil
}

// ---------------------------------------------------------- by password

// recoveryAttempts is how many failures one source is answered before the
// password stops answering it for recoveryWindow; recoveryCeiling is how many
// every source together are answered.
//
// The two are not one number. The ceiling protects the process from many
// callers and sits well above what one may do, because a single count for
// everybody is a lock anyone can hold shut: ten wrong proofs from anywhere
// stopped the one operator holding the right one, on the path that exists for
// when nothing else works.
const (
	recoveryAttempts = 10
	recoveryCeiling  = 100
	recoveryWindow   = time.Minute
)

// PasswordRecovery is the shape for a hub outside Kubernetes: the recovery
// account, signing in with a password that is proved and never sent
// (SRP-6a, docs/decisions/0076).
//
// The hub holds the account's salt and verifier and not the password, wherever
// it is deployed: on layout v5 only the verifier is in the store the hub may
// read, and elsewhere it is derived from the password at the moment it is read.
// The sign-in page's script does its half of the exchange; nothing here takes a
// password as an argument.
type PasswordRecovery struct {
	// Where is where the password is kept, as the secrets source describes it
	// ("ssm /sluis/<instance>/internal/oidc/recovery-password"): the console
	// shows the operator this installation's own location. Empty for a
	// generated password.
	Where string
	// Source reads the account's credential.
	Source srpauth.Source
	// Service runs the exchanges. It is set when the console is assembled,
	// beside the State it keeps them in.
	Service *srpauth.Service
}

var _ Recovery = (*PasswordRecovery)(nil)

// NewPasswordRecovery returns the password shape over a credential source.
func NewPasswordRecovery(source srpauth.Source, where string) *PasswordRecovery {
	return &PasswordRecovery{Where: where, Source: source}
}

// Open gives the recovery the State its exchanges are kept in and the key every
// container shares, which the made-up answers for unknown usernames come from.
// Refusals are counted as they always were: ten a source, a hundred in all, a
// minute.
func (p *PasswordRecovery) Open(state port.State, key func() ([]byte, error)) {
	p.Service = &srpauth.Service{
		Params:   srp.Default,
		Store:    srpauth.StateStore{State: state},
		DecoyKey: srpauth.DecoyKey(key),
		Refusals: throttle.New(recoveryAttempts, recoveryCeiling, recoveryWindow, 0, nil),
		Starts:   srpauth.NewStarts(nil),
	}
}

// Kind implements [Recovery].
func (p *PasswordRecovery) Kind() string { return "password" }

// Prompt implements [Recovery].
func (p *PasswordRecovery) Prompt() Prompt {
	return Prompt{
		Label:   "Recovery password",
		Intro:   "The recovery account's username is recovery. Its password is proved in this page and never sent.",
		Caution: "This grants operator on this hub. Never give it to anyone who asks for it.",
	}
}

// lookup is the one account the console offers: the recovery account.
func (p *PasswordRecovery) lookup() srpauth.Lookup {
	return func(ctx context.Context, username string) (srp.Credential, bool, error) {
		if username != srpauth.RecoveryUser || p.Source == nil {
			return srp.Credential{}, false, nil
		}
		cred, err := p.Source(ctx)
		if errors.Is(err, srpauth.ErrNoCredential) {
			return srp.Credential{}, false, nil
		}
		if err != nil {
			return srp.Credential{}, false, err
		}
		return cred, true, nil
	}
}

// cheapWindow is how often a refusal that costs nothing is recorded.
const cheapWindow = time.Minute

// cheapRefusals counts the refusals that do no work, per reason, and says when
// one is due a record: the first, then one per window carrying the count.
type cheapRefusals struct {
	mu      sync.Mutex
	last    map[string]time.Time
	pending map[string]int
}

// note counts an attempt and reports whether to record now, and how many
// attempts the record stands for.
func (c *cheapRefusals) note(reason string, now time.Time) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		c.last, c.pending = map[string]time.Time{}, map[string]int{}
	}
	c.pending[reason]++
	if at, seen := c.last[reason]; seen && now.Sub(at) < cheapWindow {
		return 0, false
	}
	n := c.pending[reason]
	c.last[reason], c.pending[reason] = now, 0
	return n, true
}
