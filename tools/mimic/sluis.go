package mimic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// verifiable are the algorithms a config may accept from the stable sluis.
var verifiable = []string{"ES256", "ES384", "ES512", "RS256", "PS256", "EdDSA"}

// leeway is the clock skew allowed on exp, nbf and iat.
const leeway = 30 * time.Second

// subject is who signed in at the stable sluis.
type subject struct {
	Sub    string
	Groups []string
	AMR    []string
}

// stable is mimic's side of the stable sluis: its discovery, its key set, the code exchange and the token checks.
type stable struct {
	cfg    Sluis
	secret string // the client secret, empty for a public client
	http   *http.Client
	now    func() time.Time

	mu      sync.Mutex
	meta    *sluisMeta
	keys    jose.JSONWebKeySet
	fetched time.Time // when the key set was last fetched
}

type sluisMeta struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

// metadata reads the discovery document once; a failure is retried on the next call, so mimic starts while the
// stable sluis is down.
func (s *stable) metadata(ctx context.Context) (*sluisMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.meta != nil {
		return s.meta, nil
	}
	var m sluisMeta
	if err := s.getJSON(ctx, s.cfg.Issuer+"/.well-known/openid-configuration", &m); err != nil {
		return nil, fmt.Errorf("the stable sluis's discovery: %w", err)
	}
	if m.Issuer != s.cfg.Issuer {
		return nil, errors.New("the stable sluis's discovery names another issuer")
	}
	for _, e := range []string{m.AuthorizationEndpoint, m.TokenEndpoint, m.JWKSURI} {
		if checkRedirect(e) != nil {
			return nil, errors.New("the stable sluis's discovery has an endpoint that is not https")
		}
	}
	s.meta = &m
	return s.meta, nil
}

// key finds the key of kid, fetching the key set again at most every 30 seconds when kid is not in it.
func (s *stable) key(ctx context.Context, kid string) (*jose.JSONWebKey, error) {
	m, err := s.metadata(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if k := s.keys.Key(kid); len(k) == 1 {
		return &k[0], nil
	}
	if !s.fetched.IsZero() && s.now().Sub(s.fetched) < 30*time.Second {
		return nil, errors.New("no key of the token's kid")
	}
	var set jose.JSONWebKeySet
	if err := s.getJSON(ctx, m.JWKSURI, &set); err != nil {
		return nil, fmt.Errorf("the stable sluis's key set: %w", err)
	}
	s.keys, s.fetched = set, s.now()
	if k := s.keys.Key(kid); len(k) == 1 {
		return &k[0], nil
	}
	return nil, errors.New("no key of the token's kid")
}

func (s *stable) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	res, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(v)
}

// verify checks a token of the stable sluis for the audience (RFC 8725): one signature by a key of its key set, an
// algorithm of the config, a type of types, the issuer, the audience and the time window. nonce, when set, must match.
func (s *stable) verify(ctx context.Context, raw, audience, nonce string, types ...string) (*subject, error) {
	algs := make([]jose.SignatureAlgorithm, len(s.cfg.Algorithms))
	for i, a := range s.cfg.Algorithms {
		algs[i] = jose.SignatureAlgorithm(a)
	}
	tok, err := jose.ParseSignedCompact(raw, algs)
	if err != nil {
		return nil, errors.New("not a JWS of an accepted algorithm")
	}
	if len(tok.Signatures) != 1 {
		return nil, errors.New("not one signature")
	}
	h := tok.Signatures[0].Protected
	if refusedHeader(raw) {
		return nil, errors.New("a header this verifier refuses")
	}
	typ, _ := h.ExtraHeaders[jose.HeaderType].(string)
	if !slices.Contains(types, strings.ToLower(typ)) {
		return nil, errors.New("a type this verifier refuses")
	}
	k, err := s.key(ctx, h.KeyID)
	if err != nil {
		return nil, err
	}
	if k.Algorithm != "" && k.Algorithm != h.Algorithm {
		return nil, errors.New("the key is for another algorithm")
	}
	payload, err := tok.Verify(k.Key)
	if err != nil {
		return nil, errors.New("the signature does not verify")
	}
	var c struct {
		Iss    string          `json:"iss"`
		Sub    string          `json:"sub"`
		Aud    json.RawMessage `json:"aud"`
		Exp    *float64        `json:"exp"`
		Nbf    *float64        `json:"nbf"`
		Iat    *float64        `json:"iat"`
		Nonce  string          `json:"nonce"`
		Groups []string        `json:"groups"`
		AMR    []string        `json:"amr"`
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, errors.New("the claims are not a JSON object of the expected types")
	}
	now := s.now()
	switch {
	case c.Iss != s.cfg.Issuer:
		return nil, errors.New("another issuer")
	case !audienceHas(c.Aud, audience):
		return nil, errors.New("another audience")
	case c.Exp == nil || now.After(unix(*c.Exp).Add(leeway)):
		return nil, errors.New("expired, or no exp")
	case c.Nbf != nil && now.Add(leeway).Before(unix(*c.Nbf)):
		return nil, errors.New("not yet valid")
	case c.Iat != nil && now.Add(leeway).Before(unix(*c.Iat)):
		return nil, errors.New("issued in the future")
	case c.Sub == "":
		return nil, errors.New("no sub")
	case nonce != "" && c.Nonce != nonce:
		return nil, errors.New("another nonce")
	}
	return &subject{Sub: c.Sub, Groups: c.Groups, AMR: c.AMR}, nil
}

// refusedHeader says whether the protected header carries a key or a key's address (jwk, jku, x5c, x5u), which a
// verifier never trusts (RFC 8725 section 3.10), or an extension it does not understand (crit, b64).
func refusedHeader(raw string) bool {
	first, _, _ := strings.Cut(raw, ".")
	b, err := base64.RawURLEncoding.DecodeString(first)
	var h map[string]json.RawMessage
	if err != nil || json.Unmarshal(b, &h) != nil {
		return true
	}
	for _, k := range []string{"jwk", "jku", "x5c", "x5u", "crit", "b64"} {
		if _, ok := h[k]; ok {
			return true
		}
	}
	return false
}

func unix(f float64) time.Time { return time.Unix(int64(f), 0) }

// audienceHas reads aud as a string or an array of strings.
func audienceHas(raw json.RawMessage, want string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == want
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return slices.Contains(many, want)
	}
	return false
}

// authorizeURL is where a person signs in at the stable sluis.
func (s *stable) authorizeURL(ctx context.Context, redirect, state, nonce, challenge string) (string, error) {
	m, err := s.metadata(ctx)
	if err != nil {
		return "", err
	}
	q := url.Values{
		"response_type": {"code"}, "client_id": {s.cfg.ClientID}, "redirect_uri": {redirect},
		"scope": {strings.Join(s.cfg.Scopes, " ")}, "state": {state}, "nonce": {nonce},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
	sep := "?"
	if strings.Contains(m.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return m.AuthorizationEndpoint + sep + q.Encode(), nil
}

// exchange trades the code of a person's sign-in for the ID token and checks it.
func (s *stable) exchange(ctx context.Context, code, redirect, verifier, nonce string) (*subject, error) {
	m, err := s.metadata(ctx)
	if err != nil {
		return nil, err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}}
	if s.secret == "" {
		form.Set("client_id", s.cfg.ClientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if s.secret != "" {
		req.SetBasicAuth(url.QueryEscape(s.cfg.ClientID), url.QueryEscape(s.secret))
	}
	res, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the stable sluis's token endpoint: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	var body struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil || res.StatusCode != http.StatusOK || body.IDToken == "" {
		return nil, fmt.Errorf("the stable sluis refused the code (status %d)", res.StatusCode)
	}
	sub, err := s.verify(ctx, body.IDToken, s.cfg.ClientID, nonce, "", "jwt")
	if err != nil {
		return nil, fmt.Errorf("the ID token: %w", err)
	}
	if len(s.cfg.RequireAMR) > 0 && !slices.ContainsFunc(sub.AMR, func(a string) bool { return slices.Contains(s.cfg.RequireAMR, a) }) {
		return nil, errors.New("the sign-in has none of the amr values the config requires")
	}
	return sub, nil
}
