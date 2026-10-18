package issuer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/truvity/sluis/internal/egress"
	"github.com/truvity/sluis/internal/signer"
	"github.com/truvity/sluis/storage/logattr"
)

// Back-Channel Logout tells a relying party, server to server, that a
// session it holds has ended.
//
// WHY THIS ONE, of the three optional logout mechanisms. Session
// Management and Front-Channel Logout both work by loading something
// from this origin inside the application's page -- a polled iframe, or
// one hidden iframe per client at sign-out. Browsers block third-party
// cookies by default now, so both fail quietly in exactly the case they
// exist for. This is an HTTP POST between two servers and does not care
// what a browser allows. It is also the only one that can reach a PROXY,
// which is what actually holds the session for a console with no OpenID
// flow of its own.
//
// WHAT IT BUYS. Revoking a session is immediate here and invisible
// there: a relying party holding a valid access token keeps serving
// until it next refreshes, which is up to the client's `ttl_cap`. A
// logout token closes that window instead of bounding it.
//
// OPT-IN, per client. A client that declares no `backchannel_logout_uri`
// is never contacted, so serving this changes nothing for one that has
// not asked for it.
const backChannelEvent = "http://schemas.openid.net/event/backchannel-logout"

// logoutToken is the JWT a client is sent. The shape is fixed by the
// specification and every field here is required by it, except `sub` and
// `sid` of which at least one must appear -- both do, because a client
// may key its session store on either.
type logoutToken struct {
	Issuer    string         `json:"iss"`
	Audience  string         `json:"aud"`
	IssuedAt  int64          `json:"iat"`
	Expires   int64          `json:"exp"`
	JWTID     string         `json:"jti"`
	Subject   string         `json:"sub,omitempty"`
	SessionID string         `json:"sid,omitempty"`
	Events    map[string]any `json:"events"`
	// NOTE: there is deliberately no `nonce`. The specification forbids
	// it, because a logout token that carries one can be mistaken for an
	// ID token by a relying party that checks too little.
}

// AnnounceEnded tells the clients of sessions that were ended outside a browser's
// sign-out (the sweep after recovery is turned off) by back-channel logout, to
// those that declared one. It waits for the posts and logs what it could not
// deliver.
func (s *Storage) AnnounceEnded(ctx context.Context, log *slog.Logger, ended []Session) {
	if log == nil {
		log = slog.Default()
	}

	s.announceLogout(ctx, log, ended)
}

// announceLogout tells every client that asked to be told. Errors are
// logged and not returned: the sign-out has already happened, and a
// relying party that cannot be reached must not turn a completed
// sign-out into a failed one.
//
// The posts run concurrently, at most [announceInFlight] at a time, and all
// are waited for before returning: a relying party that cannot answer costs
// its own timeout once, not once per client and sign-in, and nothing is left
// running after the response (a function platform may freeze it).
func (s *Storage) announceLogout(ctx context.Context, log *slog.Logger, ended []Session) {
	var (
		wg   sync.WaitGroup
		slot = make(chan struct{}, announceInFlight)
	)

	for i := range ended {
		one := &ended[i]

		declared, ok := s.iss.Policy().Client(one.ClientID)
		if !ok || strings.TrimSpace(declared.BackChannelLogout) == "" {
			continue
		}

		slot <- struct{}{}

		wg.Add(1)

		go func() {
			defer wg.Done()
			defer func() { <-slot }()

			if err := s.postLogoutToken(ctx, declared.BackChannelLogout, one); err != nil {
				log.WarnContext(ctx, "a client could not be told its session ended",
					logattr.SafeString("client_id", one.ClientID), logattr.SafeError("error", err))

				return
			}

			log.InfoContext(ctx, "told a client its session ended",
				logattr.SafeString("client_id", one.ClientID))
		}()
	}

	wg.Wait()
}

// backChannelTimeout is short: delivery runs while somebody is waiting for a
// sign-out page, and a relying party that cannot answer in a few seconds is one
// whose session will die at its next refresh anyway.
const backChannelTimeout = 5 * time.Second

// errBackChannelRedirected is a delivery that was answered with a redirect.
var errBackChannelRedirected = errors.New("the back-channel logout address redirected; it is posted to as declared and nowhere else")

// refuseRedirect makes a redirect a failure. A logout token is a signed
// statement about a person, posted to the address the policy declares for a
// client; a redirect would carry it, and the form it is in, to an address the
// policy did not declare, and a POST that is redirected is sent again as a GET
// by some clients and as a POST by others.
func refuseRedirect(*http.Request, []*http.Request) error { return errBackChannelRedirected }

// newAnnounceClient is the client that delivers logout tokens, connecting under
// network.
func newAnnounceClient(network egress.Policy) *http.Client {
	return &http.Client{Timeout: backChannelTimeout, Transport: network.Transport(), CheckRedirect: refuseRedirect}
}

// egressFor is the network policy a delivery or a fetch connects under: base,
// or none at all in a development installation, which talks to its own
// loopback. That is `allowInsecure` AND an issuer URL on the loopback
// interface: `allowInsecure` alone is reachable from a production document and
// must not lift a guard against the metadata service.
//
// A client document is fetched from a URL the client chose, so it may reach
// public addresses only. A back-channel address is one the policy declares, and
// is as likely to be a service in the cluster as a public one, so private
// ranges are admitted for it; loopback, link-local (the instance metadata
// service) and the other reserved ranges never are.
func egressFor(iss *Issuer, base egress.Policy) egress.Policy {
	if iss != nil && iss.Config().AllowInsecure && LoopbackURL(iss.Config().URL) {
		return egress.Development
	}
	return base
}

// LoopbackURL reports whether raw is a URL whose host is the loopback interface
// by name or address.
func LoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// announceInFlight bounds how many logout tokens are being delivered at once.
const announceInFlight = 8

// postLogoutToken mints one token and delivers it.
func (s *Storage) postLogoutToken(ctx context.Context, where string, session *Session) error {
	token, err := s.mintLogoutToken(session)
	if err != nil {
		return err
	}

	form := url.Values{"logout_token": {token}}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, where,
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}

	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := s.announce
	if client == nil {
		client = newAnnounceClient(egress.Declared)
	}

	response, err := client.Do(request)
	if err != nil {
		return err
	}

	defer func() { _ = response.Body.Close() }()

	// The specification asks for 200, and says a 2xx is acceptable.
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("answered %d", response.StatusCode)
	}

	return nil
}

// mintLogoutToken signs one logout token for one session.
func (s *Storage) mintLogoutToken(session *Session) (string, error) {
	// The receiving client is this token's whole audience -- a logout
	// token is never minted for a resource -- so, like [Storage.MintFor],
	// this resolves its algorithm directly rather than through the
	// context carrier the library's own mint paths need.
	issued := time.Now()
	claims := logoutToken{
		Issuer:   s.iss.Config().URL,
		Audience: session.ClientID,
		IssuedAt: issued.Unix(),
		// Required by Back-Channel Logout 1.0 2.4, and short: the token is
		// posted now and only has to survive its delivery.
		Expires: issued.Add(signer.LogoutTokenLifetime).Unix(),
		JWTID:   uuid.NewString(),
		Subject: session.Identity,
		// The SAME `sid` the relying party's ID token carried, which is
		// this per-client session and not the browser sign-in
		// it hangs off. A relying party matches a logout token to its
		// session by that value; naming the sign-in instead was a token
		// that verified and matched nothing. Empty for a client whose ID
		// token carried none -- `openid` alone opens no session here --
		// and then `sub` carries it alone, which the specification
		// allows.
		SessionID: session.ID,
		Events:    map[string]any{backChannelEvent: map[string]any{}},
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	// `typ: logout+jwt` is required, and it is the one thing that
	// stops a relying party mistaking this for an ID token: the signer
	// sets it from the purpose.
	signed, err := s.signer.Sign(context.Background(), signer.Request{
		Purpose: signer.PurposeLogout, Algorithm: s.signingAlgorithmFor(session.ClientID), Payload: payload,
	})
	if err != nil {
		return "", err
	}

	return signed.Token, nil
}
