package issuer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	httphelper "github.com/zitadel/oidc/v3/pkg/http"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/tokens"
)

// tokenPath is the token endpoint, as [Provider] mounts it.
const tokenPath = "/token"

// maxTokenForm is how much of a token request is read to decide whether
// it is ours. A token request is a grant, a few identifiers and a proof of
// a few KiB; 1 MiB is generous for the largest and a tenth of what
// net/http would read of a form, and it equals what the issuer's
// maintenance gate reads of the same request.
const maxTokenForm = 1 << 20

// githubTokens serves the one exchange the OpenID library cannot: an
// installation token for a catalogue App, which is GitHub's token and not
// one this issuer signs.
//
// It claims a request only when it is a token exchange whose
// `requested_token_type` is [tokens.TypeGitHubInstallationToken] AND whose
// audience starts `github-app:`. Everything else, including a request with
// only one of the two, goes to the library untouched, body and all.
//
// It does not verify or decide anything of its own: the subject token is
// read by the library's own subject verification and this issuer's
// verifier chain, a sign-in by the same rules [Storage.proofOf] applies to
// every exchange, and the groups come from the same evaluation. Only the
// last step differs -- a catalogue grant instead of a client's `requires`.
func githubTokens(iss *Issuer, storage *Storage, provider *op.Provider, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != tokenPath ||
			!strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			next.ServeHTTP(w, r)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxTokenForm))
		// Put back what was read in front of what was not, so the library
		// reads exactly the body the caller sent.
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(raw), r.Body), r.Body}
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		form, err := url.ParseQuery(string(raw))
		if err != nil || !claimsGitHubToken(form) {
			next.ServeHTTP(w, r)
			return
		}
		serveGitHubToken(w, r, iss, storage, provider, form)
	})
}

// claimsGitHubToken reports whether a token request is an installation
// token's.
func claimsGitHubToken(form url.Values) bool {
	if form.Get("grant_type") != string(oidc.GrantTypeTokenExchange) ||
		!tokens.IsGitHubInstallationToken(form.Get("requested_token_type")) {
		return false
	}
	for _, audience := range form["audience"] {
		if strings.HasPrefix(audience, tokens.GitHubAppAudiencePrefix) {
			return true
		}
	}
	return false
}

// githubTokenResponse is RFC 8693's response, with what GitHub says the
// token carries beside it.
type githubTokenResponse struct {
	AccessToken     string            `json:"access_token"`
	IssuedTokenType string            `json:"issued_token_type"`
	TokenType       string            `json:"token_type"`
	ExpiresIn       int64             `json:"expires_in,omitempty"`
	Repositories    []string          `json:"repositories,omitempty"`
	Permissions     map[string]string `json:"permissions,omitempty"`
}

// serveGitHubToken is one installation token request, audited exactly
// once whatever it ends in.
func serveGitHubToken(
	w http.ResponseWriter, r *http.Request, iss *Issuer, storage *Storage, provider *op.Provider, form url.Values,
) {
	ctx := op.ContextWithIssuer(r.Context(), iss.Config().URL)
	audience := form.Get("audience")
	request := GitHubTokenRequest{App: strings.TrimPrefix(audience, tokens.GitHubAppAudiencePrefix)}
	var proof Proof

	refuse := func(failed bool, err *oidc.Error, minted GitHubToken) {
		// The audit record keeps the reason; the caller gets the fixed words.
		outcome := audit.Denied(reasonOf(err))
		if failed {
			outcome = audit.Failed(reasonOf(err))
		}
		iss.recordGitHubToken(ctx, proof, request, minted, outcome)
		writeOAuthError(w, r, err)
	}

	switch {
	case form.Get("actor_token") != "" || form.Get("actor_token_type") != "":
		// Delegation is a second principal in a request, and this design
		// has exactly one: the subject.
		refuse(false, malformed(ctx, errors.New("actor_token: acting for another party is not served by this issuer")), GitHubToken{})
		return
	case len(form["audience"]) != 1:
		refuse(false, deny(ctx, oidc.ErrInvalidTarget, errors.New("name exactly one audience: github-app:<id>")), GitHubToken{})
		return
	case form.Get("subject_token") == "" || form.Get("subject_token_type") == "":
		refuse(false, malformed(ctx, errors.New("subject_token and subject_token_type are required")), GitHubToken{})
		return
	case !oidc.TokenType(form.Get("subject_token_type")).IsSupported():
		refuse(false, malformed(ctx, errors.New("subject_token_type is not supported")), GitHubToken{})
		return
	}

	parsed, err := ParseGitHubTokenRequest(request.App, form.Get("repositories"), form.Get("scope"))
	if err != nil {
		refuse(false, malformed(ctx, err), GitHubToken{})
		return
	}
	request = parsed

	// The client. A job presents the audience itself, as it does for every
	// exchange, or nothing: its proof is the subject token, and a
	// `github-app:` audience is not a declared client to authenticate. Any
	// OTHER client is authenticated as the library would, because a
	// sign-in is exchanged only by the client it was issued to.
	clientID, secret, _ := r.BasicAuth()
	if clientID, err = url.QueryUnescape(clientID); err == nil {
		secret, err = url.QueryUnescape(secret)
	}
	if err != nil {
		refuse(false, deny(ctx, oidc.ErrInvalidClient, errors.New("invalid basic auth header")), GitHubToken{})
		return
	}
	if clientID != "" && clientID != audience {
		if err = storage.AuthorizeClientIDSecret(ctx, clientID, secret); err != nil {
			refuse(false, asOIDCError(ctx, err, oidc.ErrInvalidClient), GitHubToken{})
			return
		}
	}

	subjectType := oidc.TokenType(form.Get("subject_token_type"))
	id, _, claims, ok := op.GetTokenIDAndSubjectFromToken(ctx, provider, form.Get("subject_token"), subjectType, false)
	if !ok {
		refuse(false, deny(ctx, oidc.ErrInvalidGrant, errors.New("subject_token is invalid")), GitHubToken{})
		return
	}
	proof, err = storage.proofOf(ctx, verifiedSubject{id: id, kind: subjectType, claims: claims, client: clientID})
	if err != nil {
		refuse(false, asOIDCError(ctx, err, oidc.ErrInvalidGrant), GitHubToken{})
		return
	}

	result, _, err := iss.evaluate(ctx, proof)
	if err != nil {
		refuse(true, evaluationRefusal(ctx, err), GitHubToken{})
		return
	}

	minted, err := iss.MintGitHubToken(ctx, result.Groups, request)
	if err != nil {
		refuse(errors.Is(err, ErrGitHubUpstream), githubTokenError(ctx, err), minted)
		return
	}
	iss.recordGitHubToken(ctx, proof, request, minted, audit.Succeeded())

	response := githubTokenResponse{
		AccessToken:     minted.Token,
		IssuedTokenType: form.Get("requested_token_type"), // the spelling the caller used (both are accepted)
		// RFC 8693 2.2.1: the token is not an OAuth access token, so how it
		// is presented is not this response's to say.
		TokenType:    "N_A",
		Repositories: minted.Repositories,
		Permissions:  minted.Permissions,
	}
	if !minted.ExpiresAt.IsZero() {
		response.ExpiresIn = max(int64(time.Until(minted.ExpiresAt).Seconds()), 0)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	httphelper.MarshalJSONWithStatus(w, response, http.StatusOK)
}

// githubTokenError is a minting refusal as RFC 6749 spells it.
func githubTokenError(ctx context.Context, err error) *oidc.Error {
	switch {
	case errors.Is(err, ErrGitHubAppUnavailable), errors.Is(err, ErrNoGitHubGrant):
		// An App that does not exist and an App this proof may not use are
		// one answer.
		return deny(ctx, oidc.ErrInvalidTarget, err)
	case errors.Is(err, ErrGitHubScope):
		return deny(ctx, oidc.ErrInvalidScope, err)
	default:
		return fault(ctx, err)
	}
}

// asOIDCError keeps an error the proof rules already spelled, and spells
// any other with fallback.
func asOIDCError(ctx context.Context, err error, fallback func() *oidc.Error) *oidc.Error {
	var spelled *oidc.Error
	if errors.As(err, &spelled) {
		return spelled
	}
	return deny(ctx, fallback, err)
}

// evaluationRefusal is why a proof could not be resolved to groups: the
// directory refusing the person is a refusal, anything else is a fault of
// ours.
func evaluationRefusal(ctx context.Context, err error) *oidc.Error {
	if errors.As(err, new(*Refused)) {
		return deny(ctx, oidc.ErrInvalidGrant, err)
	}
	return fault(ctx, err)
}

// verifiedSubject is a subject token as the library verified it, read
// by [Storage.proofOf] exactly as an ordinary exchange's is.
type verifiedSubject struct {
	id     string
	kind   oidc.TokenType
	claims map[string]any
	client string
}

func (v verifiedSubject) GetExchangeSubjectTokenClaims() map[string]any { return v.claims }
func (v verifiedSubject) GetExchangeSubjectTokenType() oidc.TokenType   { return v.kind }
func (v verifiedSubject) GetExchangeSubjectTokenIDOrToken() string      { return v.id }
func (v verifiedSubject) GetClientID() string                           { return v.client }
