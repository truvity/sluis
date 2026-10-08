package issuer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	httphelper "github.com/zitadel/oidc/v3/pkg/http"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/tokens"
)

// CloudflareMinter is what the exchange needs of the Cloudflare minter
// (internal/cloudflare/minter.Minter): the grants decide, and the minter audits
// the mint itself, whether it succeeds or not.
type CloudflareMinter interface {
	MintFor(ctx context.Context, preset string, caller minter.Caller, lifetime time.Duration) (*minter.Minted, error)
	Granted(c minter.Caller) []minter.PresetInfo
}

// UseCloudflare gives the issuer the minter of Cloudflare credentials. Without
// it every such request is refused as naming a preset nobody declared.
func (i *Issuer) UseCloudflare(m CloudflareMinter) { i.cloudflare.Store(&m) }

func (i *Issuer) cloudflareMinter() CloudflareMinter {
	if i == nil {
		return nil
	}
	if p := i.cloudflare.Load(); p != nil {
		return *p
	}
	return nil
}

// CloudflareCaller is who a proof is, as the policy's `cloudflare.grants` read
// it: the groups it holds. A CI job is a group like any other, declared in the
// policy with `github` matchers on what the verified token says (repository,
// ref, event, job_workflow_ref); the exchange does not look at a workflow's
// display name, which anyone who can push a branch can choose.
func CloudflareCaller(proof Proof, groups []string) minter.Caller {
	return minter.Caller{Actor: proof.actor(), Groups: groups}
}

// cloudflareTokens serves the exchange for a Cloudflare credential: requested_token_type
// [tokens.TypeCloudflareToken] and an audience that starts `cloudflare:`. It is
// githubTokens' sibling: the subject token is verified by the same chain and
// the groups come from the same evaluation; only the last step differs, the
// policy's cloudflare.grants instead of a client's `requires`.
func cloudflareTokens(iss *Issuer, storage *Storage, provider *op.Provider, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != tokenPath ||
			!strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			next.ServeHTTP(w, r)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxTokenForm))
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(raw), r.Body), r.Body}
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		form, err := url.ParseQuery(string(raw))
		if err != nil || !claimsCloudflareToken(form) {
			next.ServeHTTP(w, r)
			return
		}
		serveCloudflareToken(w, r, iss, storage, provider, form)
	})
}

func claimsCloudflareToken(form url.Values) bool {
	if form.Get("grant_type") != string(oidc.GrantTypeTokenExchange) ||
		form.Get("requested_token_type") != tokens.TypeCloudflareToken {
		return false
	}
	for _, audience := range form["audience"] {
		if strings.HasPrefix(audience, tokens.CloudflareAudiencePrefix) {
			return true
		}
	}
	return false
}

// cloudflareTokenResponse is RFC 8693's response, with the credential beside
// it. access_token is the API token, or for R2 the access key id.
type cloudflareTokenResponse struct {
	AccessToken     string `json:"access_token"`
	IssuedTokenType string `json:"issued_token_type"`
	TokenType       string `json:"token_type"`
	ExpiresIn       int64  `json:"expires_in,omitempty"`

	Token           string `json:"token,omitempty"`
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	Endpoint        string `json:"endpoint,omitempty"`
	ExpiresOn       string `json:"expires_on"`
}

func serveCloudflareToken(
	w http.ResponseWriter, r *http.Request, iss *Issuer, storage *Storage, provider *op.Provider, form url.Values,
) {
	ctx := op.ContextWithIssuer(r.Context(), iss.Config().URL)
	audience := form.Get("audience")
	preset := strings.TrimPrefix(audience, tokens.CloudflareAudiencePrefix)
	refuse := func(err *oidc.Error) { op.RequestError(w, r, err, nil) }

	cf := iss.cloudflareMinter()
	switch {
	case form.Get("actor_token") != "" || form.Get("actor_token_type") != "":
		refuse(oidc.ErrInvalidRequest().WithDescription("actor_token: acting for another party is not served by this issuer"))
		return
	case len(form["audience"]) != 1:
		refuse(oidc.ErrInvalidTarget().WithDescription("name exactly one audience: cloudflare:<preset>"))
		return
	case form.Get("subject_token") == "" || form.Get("subject_token_type") == "":
		refuse(oidc.ErrInvalidRequest().WithDescription("subject_token and subject_token_type are required"))
		return
	case !oidc.TokenType(form.Get("subject_token_type")).IsSupported():
		refuse(oidc.ErrInvalidRequest().WithDescription("subject_token_type is not supported"))
		return
	case cf == nil:
		refuse(oidc.ErrInvalidTarget().WithDescription("this service mints no Cloudflare credentials"))
		return
	}

	var lifetime time.Duration
	if s := strings.TrimSpace(form.Get("lifetime")); s != "" {
		seconds, err := strconv.ParseInt(s, 10, 64)
		if err != nil || seconds < 1 {
			refuse(oidc.ErrInvalidRequest().WithDescription("lifetime: want a number of seconds"))
			return
		}
		lifetime = time.Duration(seconds) * time.Second
	}

	// The client, as githubTokens reads it: a job presents the audience
	// itself, a person the client they signed in as.
	clientID, secret, _ := r.BasicAuth()
	var err error
	if clientID, err = url.QueryUnescape(clientID); err == nil {
		secret, err = url.QueryUnescape(secret)
	}
	if err != nil {
		refuse(oidc.ErrInvalidClient().WithDescription("invalid basic auth header"))
		return
	}
	if clientID != "" && clientID != audience {
		if err = storage.AuthorizeClientIDSecret(ctx, clientID, secret); err != nil {
			refuse(oidc.ErrInvalidClient().WithDescription("invalid client_id / client_secret"))
			return
		}
	}

	subjectType := oidc.TokenType(form.Get("subject_token_type"))
	id, _, claims, ok := op.GetTokenIDAndSubjectFromToken(ctx, provider, form.Get("subject_token"), subjectType, false)
	if !ok {
		refuse(oidc.ErrInvalidGrant().WithDescription("subject_token is invalid"))
		return
	}
	proof, err := storage.proofOf(ctx, verifiedSubject{id: id, kind: subjectType, claims: claims, client: clientID})
	if err != nil {
		refuse(asOIDCError(err, oidc.ErrInvalidGrant))
		return
	}
	result, _, err := iss.evaluate(ctx, proof)
	if err != nil {
		refuse(oidc.ErrInvalidGrant().WithDescription("%s", err))
		return
	}

	minted, err := cf.MintFor(ctx, preset, CloudflareCaller(proof, result.Groups), lifetime)
	if err != nil {
		refuse(cloudflareTokenError(err))
		return
	}

	response := cloudflareTokenResponse{
		IssuedTokenType: tokens.TypeCloudflareToken,
		TokenType:       "N_A",
		ExpiresIn:       max(int64(time.Until(minted.ExpiresOn).Seconds()), 0),
		ExpiresOn:       minted.ExpiresOn.UTC().Format(time.RFC3339),
	}
	if minted.R2 {
		response.AccessToken = minted.AccessKeyID
		response.AccessKeyID, response.SecretAccessKey, response.Endpoint = minted.AccessKeyID, minted.SecretAccessKey, minted.Endpoint
	} else {
		response.AccessToken, response.Token = minted.Token, minted.Token
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	httphelper.MarshalJSONWithStatus(w, response, http.StatusOK)
}

// cloudflareTokenError is a minting refusal as RFC 6749 spells it. What went
// wrong at Cloudflare is the audit trail's and the log's, not the caller's.
func cloudflareTokenError(err error) *oidc.Error {
	switch {
	case errors.Is(err, minter.ErrUnknownPreset), errors.Is(err, minter.ErrNotGranted):
		// The same answer for both: whether a preset exists is not for
		// someone it is not granted to to learn.
		return oidc.ErrInvalidTarget().WithDescription("no Cloudflare preset of that name is granted to this proof")
	case errors.Is(err, minter.ErrLifetime):
		return oidc.ErrInvalidRequest().WithDescription("%s", err)
	default:
		return oidc.ErrServerError().WithDescription("the Cloudflare credential could not be minted; see the audit trail")
	}
}

// CloudflareGrant is a preset the caller's groups open, as `/.access/grants`
// lists it.
type CloudflareGrant struct {
	Preset      string `json:"preset"`
	Description string `json:"description,omitempty"`
	// R2 presets hand out S3 credentials for Endpoint.
	R2       bool   `json:"r2"`
	Endpoint string `json:"endpoint,omitempty"`
	// Lifetime is the longest a credential of the preset lives, in seconds.
	Lifetime int64 `json:"lifetime_seconds"`
}

// cloudflareGrantsOf are the presets a holder of groups may ask for, sorted.
func cloudflareGrantsOf(cf CloudflareMinter, groups []string) []CloudflareGrant {
	if cf == nil {
		return nil
	}
	var out []CloudflareGrant
	for _, p := range cf.Granted(minter.Caller{Groups: groups}) {
		out = append(out, CloudflareGrant{
			Preset: p.Name, Description: p.Description, R2: p.R2, Endpoint: p.Endpoint,
			Lifetime: int64(p.Lifetime.Seconds()),
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Preset < out[b].Preset })
	return out
}
