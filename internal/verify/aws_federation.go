package verify

import (
	"net/http"
	"slices"
	"time"
)

// AWSFederation is the set of AWS accounts whose roles may exchange their
// outbound identity federation token for one of this installation's. Like
// [Federation] it holds no secret: an account, a name and a URL per row. It is
// the policy document's exchange.aws (internal/config), which holds the rows to
// their rules when the document is loaded.
type AWSFederation struct {
	// Audience the role must request from `sts:GetWebIdentityToken`.
	// Required whenever an account is listed; it is the trust boundary.
	Audience string `yaml:"audience"`
	// MaxAge rejects a token whose `iat` is older, whatever its `exp`
	// allows. Default 5m, at most 1h.
	MaxAge time.Duration `yaml:"maxAge,omitempty"`
	// Accounts are the trusted accounts. Empty is an installation with no
	// AWS proof, which is a real posture and not a failure.
	Accounts []AWSAccountRow `yaml:"accounts"`
}

// AWSAccountRow is one account's row.
type AWSAccountRow struct {
	// Account is the 12-digit AWS account id.
	Account string `yaml:"account"`
	// Name is the estate's own word for the account, for logs.
	Name string `yaml:"name"`
	// Issuer is the account's `iss`, from
	// `aws iam get-outbound-web-identity-federation-info`.
	Issuer string `yaml:"issuer"`
	// JWKSURI is where its keys are. Empty means
	// `{issuer}/.well-known/jwks.json`.
	JWKSURI string `yaml:"jwksUri,omitempty"`
	// OrgID, when set, must equal the token's `org_id` claim.
	OrgID string `yaml:"orgId,omitempty"`
	// Algs narrows the accepted algorithms. Default `[ES384, RS256]`.
	Algs []string `yaml:"algs,omitempty"`
}

// Verifiers turns the rows into verifiers, one per account, each
// answering only for tokens carrying its own issuer.
func (f AWSFederation) Verifiers(httpClient *http.Client) []*AWSAccount {
	out := make([]*AWSAccount, 0, len(f.Accounts))
	for _, row := range f.Accounts {
		out = append(out, &AWSAccount{
			Account:  row.Account,
			Name:     row.Name,
			Issuer:   row.Issuer,
			JWKSURI:  row.JWKSURI,
			OrgID:    row.OrgID,
			Algs:     slices.Clone(row.Algs),
			Audience: f.Audience,
			MaxAge:   f.MaxAge,
			Client:   httpClient,
		})
	}
	return out
}

// Names lists the accounts, for the line an operator reads at start.
func (f AWSFederation) Names() []string {
	names := make([]string, 0, len(f.Accounts))
	for _, row := range f.Accounts {
		names = append(names, row.Name+" ("+row.Account+")")
	}
	return names
}
