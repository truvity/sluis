package compile

import (
	"fmt"

	"github.com/truvity/sluis/policy"
)

// AudienceClient is what the policy's own client validation reads of one
// relying party.
type AudienceClient struct {
	Name string
	// Kind is [policy.KindPublic], [policy.KindConfidential] or
	// [policy.KindExchange].
	Kind            string
	Requires        []string
	SigningAlg      string
	GroupsDelimiter string
	// PKCE is [policy.PKCERequired], [policy.PKCEOptional] or empty.
	PKCE string
	// SecretName is the name a confidential client's secret is delivered
	// under.
	SecretName string
}

// ValidateAudiences runs the policy's own client, client-document, resource,
// people and Slack rules against the derived policy, so a render refuses
// what the issuer would refuse at load: a signing algorithm it does not
// know, an unsafe or colliding groups delimiter, an address claimed by two
// people, a strict public channel.
//
// It builds the smallest policy that exercises those rules: every derived
// group, so the delimiter collision check has a real set of names, and
// every group any client, the client documents or a resource requires,
// declared too, so a `requires` naming a group another layer adds does not
// trip this narrower check. Whether `requires` names the right groups is the
// caller's to check, with a message that names its row.
func ValidateAudiences(p *Policy, clients []AudienceClient, docs *policy.ClientDocuments, resources map[string]policy.Resource) error {
	if p == nil {
		return nil
	}

	groups := make(map[string]policy.Group, len(p.Groups))
	for i := range p.Groups {
		groups[p.Groups[i].Name] = policy.Group{}
	}

	declare := func(names []string) {
		for _, name := range names {
			if _, ok := groups[name]; !ok {
				groups[name] = policy.Group{}
			}
		}
	}

	policyClients := make(map[string]policy.Client, len(clients))

	for i := range clients {
		c := &clients[i]

		declare(c.Requires)

		pc := policy.Client{
			Kind:            c.Kind,
			Requires:        c.Requires,
			SigningAlg:      c.SigningAlg,
			GroupsDelimiter: c.GroupsDelimiter,
			PKCE:            c.PKCE,
		}

		if c.Kind == policy.KindConfidential {
			pc.Secret = policy.ClientSecret{Name: c.SecretName}
		}

		policyClients[c.Name] = pc
	}

	out := policy.Policy{Version: 1, Groups: groups, Clients: policyClients, Resources: resources}

	if len(p.People) > 0 {
		out.People = make(map[string][]string, len(p.People))
		for _, person := range p.People {
			out.People[person.Name] = person.Addresses
		}
	}

	for _, workspace := range p.Slack {
		if out.Slack.Workspaces == nil {
			out.Slack.Workspaces = map[string]policy.SlackWorkspace{}
		}

		ws := policy.SlackWorkspace{}

		for _, channel := range workspace.Channels {
			if ws.Channels == nil {
				ws.Channels = map[string]policy.SlackChannel{}
			}

			ws.Channels[channel.Name] = policy.SlackChannel{
				Private: channel.Private,
				Mode:    channel.Mode,
				Ignore:  channel.Ignore,
				From:    channel.From,
				Adopt:   channel.Adopt,
			}
		}

		out.Slack.Workspaces[workspace.Workspace] = ws
	}

	for id := range resources {
		declare(resources[id].Requires)
	}

	if docs != nil {
		declare(docs.Requires)

		out.ClientDocuments = *docs
	}

	return out.Validate() //nolint:wrapcheck // the caller names the table
}

// ValidateClaims hands the claims table to the policy's own validation
// against the derived groups, so a render refuses a fragment for a group
// nobody declared. Nil or empty is no check.
func ValidateClaims(p *Policy, claims map[string]policy.Fragment) error {
	if p == nil || len(claims) == 0 {
		return nil
	}

	groups := make(map[string]policy.Group, len(p.Groups))
	for i := range p.Groups {
		groups[p.Groups[i].Name] = policy.Group{}
	}

	if err := (&policy.Policy{Version: 1, Groups: groups, Claims: claims}).Validate(); err != nil {
		return fmt.Errorf("claims: %w", err)
	}

	return nil
}
