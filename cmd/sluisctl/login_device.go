package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/version"
)

// The device authorization grant (RFC 8628): `sluisctl login --device`, and
// `sluisctl login` on a machine with no browser to open. The issuer serves it
// only for a client that declares `device_authorization: true`.

const (
	// deviceGrantType is RFC 8628 3.4's grant_type.
	deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	// deviceDefaultInterval is RFC 8628 3.2's default when the issuer names
	// none, and deviceSlowDown what each `slow_down` adds (3.5).
	deviceDefaultInterval = 5 * time.Second
	deviceSlowDown        = 5 * time.Second
)

// errDeviceNotServed is an issuer that answers 404 at /device_authorization:
// none of its clients, or not this one, declares the grant.
var errDeviceNotServed = errors.New("the issuer does not offer device sign-in")

// noBrowser reports whether there is no browser this process could open: a
// session over SSH, wherever it runs, or a Linux or other Unix desktop-less
// host (no X11 `DISPLAY` and no `WAYLAND_DISPLAY`). macOS and Windows always
// have one to open outside SSH.
func noBrowser(getenv func(string) string, goos string) bool {
	for _, name := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		if getenv(name) != "" {
			return true
		}
	}
	switch goos {
	case "darwin", "windows":
		return false
	}
	return getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == ""
}

// deviceAuthorization is RFC 8628 3.2's answer.
type deviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// deviceLogin asks the issuer for a code, tells the person where to approve
// it, and polls until it is approved, denied or expires. It honours the
// interval the issuer gives and every `slow_down`, and stops at `expires_in`.
func deviceLogin(ctx context.Context, cfg Config, out io.Writer, sleep func(context.Context, time.Duration) error) (Session, error) {
	asked, err := askDeviceCode(ctx, cfg)
	if err != nil {
		return Session{}, err
	}

	_, _ = fmt.Fprintln(out, "To sign in, open this page in a browser on any device:")
	_, _ = fmt.Fprintln(out, "  "+asked.VerificationURI)
	_, _ = fmt.Fprintln(out, "and enter the code:")
	_, _ = fmt.Fprintln(out, "  "+asked.UserCode)
	if asked.VerificationURIComplete != "" {
		_, _ = fmt.Fprintln(out, "Or open this page, which carries the code:")
		_, _ = fmt.Fprintln(out, "  "+asked.VerificationURIComplete)
	}
	_, _ = fmt.Fprintln(out, "Check that the page shows the same code before you approve. Waiting for approval...")

	interval := time.Duration(asked.Interval) * time.Second
	if interval <= 0 {
		interval = deviceDefaultInterval
	}
	lifetime := time.Duration(asked.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, lifetime)
	defer cancel()

	for {
		if err = sleep(ctx, interval); err != nil {
			return Session{}, errors.New("the code expired before it was approved; run `sluisctl login --device` again")
		}
		session, answer, err := pollDeviceCode(ctx, cfg, asked.DeviceCode)
		switch {
		case err != nil && ctx.Err() != nil:
			return Session{}, errors.New("the code expired before it was approved; run `sluisctl login --device` again")
		case err != nil:
			// The issuer could not be reached, or answered 5xx (a
			// maintenance window answers 503): ask again at the interval.
			continue
		case answer == "":
			return session, nil
		case answer == "authorization_pending":
		case answer == "slow_down":
			interval += deviceSlowDown
		case answer == "access_denied":
			return Session{}, errors.New("the sign-in was denied in the browser")
		case answer == "expired_token":
			return Session{}, errors.New("the code expired before it was approved; run `sluisctl login --device` again")
		default:
			return Session{}, fmt.Errorf("the issuer refused the device sign-in: %s", answer)
		}
	}
}

// askDeviceCode is RFC 8628 3.1.
func askDeviceCode(ctx context.Context, cfg Config) (deviceAuthorization, error) {
	form := url.Values{"client_id": {cfg.ClientID}, "scope": {loginScope}}
	status, body, err := postForm(ctx, cfg.Issuer+"/device_authorization", form)
	if err != nil {
		return deviceAuthorization{}, fmt.Errorf("%w: %w", errUnreachable, err)
	}
	switch {
	case status == http.StatusNotFound:
		return deviceAuthorization{}, fmt.Errorf("%w: %s, client %q: the client needs `device_authorization: true`, "+
			"or sign in with a browser on this machine (`sluisctl login --device=false`)", errDeviceNotServed, cfg.Issuer, cfg.ClientID)
	case status != http.StatusOK:
		return deviceAuthorization{}, fmt.Errorf("the issuer refused a device code: %s", strings.TrimSpace(string(body)))
	}
	var asked deviceAuthorization
	if err = json.Unmarshal(body, &asked); err != nil {
		return deviceAuthorization{}, fmt.Errorf("parse the device authorization response: %w", err)
	}
	if asked.DeviceCode == "" || asked.UserCode == "" || asked.VerificationURI == "" {
		return deviceAuthorization{}, errors.New("the issuer's device authorization response is missing a code or the page to approve it on")
	}
	return asked, nil
}

// pollDeviceCode is RFC 8628 3.4 once: the session when the code was
// approved, the OAuth error code when the issuer answered one, or an error
// when it could not be asked (or answered 5xx).
func pollDeviceCode(ctx context.Context, cfg Config, deviceCode string) (Session, string, error) {
	form := url.Values{"grant_type": {deviceGrantType}, "device_code": {deviceCode}, "client_id": {cfg.ClientID}}
	status, body, err := postForm(ctx, cfg.Issuer+"/token", form)
	if err != nil {
		return Session{}, "", err
	}
	if status >= http.StatusInternalServerError {
		return Session{}, "", fmt.Errorf("the issuer answered %d", status)
	}
	if status == http.StatusOK {
		session, err := sessionFrom(body)
		if err != nil {
			return Session{}, "", err
		}
		return session, "", nil
	}
	var refused struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &refused) != nil || refused.Error == "" {
		return Session{}, fmt.Sprintf("%d %s", status, strings.TrimSpace(string(body))), nil
	}
	return Session{}, refused.Error, nil
}

func postForm(ctx context.Context, where string, form url.Values) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, where, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", "sluisctl/"+version.String())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return response.StatusCode, body, nil
}
