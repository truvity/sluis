package githubapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// HookConfig is an App's webhook as GitHub holds it. The secret is never
// returned: GitHub masks it, so it cannot be compared, only replaced.
type HookConfig struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	// InsecureSSL is "0" (verify) or "1" (do not), a string on the wire.
	InsecureSSL string `json:"insecure_ssl"`
	// Secret is "********" when one is set, empty when none is.
	Secret string `json:"secret,omitempty"`
}

// HookUpdate is what [PatchHookConfig] sets. Every field is sent: a rotation
// changes the secret and, for a Kargo receiver, the URL in the one call, so
// the two never disagree on GitHub's side.
type HookUpdate struct {
	URL    string
	Secret string
}

// ContentTypeJSON is the only content type this service configures.
const ContentTypeJSON = "json"

// GetHookConfig reads the webhook configuration of the App the token is
// for (an App JWT).
func GetHookConfig(ctx context.Context, client *http.Client, appToken string) (HookConfig, error) {
	var out HookConfig
	if err := call(ctx, client, http.MethodGet, APIBase+"/app/hook/config", appToken, http.StatusOK, &out); err != nil {
		return HookConfig{}, fmt.Errorf("github: read the App's webhook: %w", err)
	}
	return out, nil
}

// PatchHookConfig sets the App's webhook URL, secret and content type
// (`PATCH /app/hook/config`, an App JWT). It does not switch the webhook on:
// that is decided when the App is created.
func PatchHookConfig(ctx context.Context, client *http.Client, appToken string, update HookUpdate) (HookConfig, error) {
	if update.URL == "" || update.Secret == "" {
		return HookConfig{}, errors.New("github: a webhook needs a URL and a secret")
	}
	body := map[string]string{
		"url": update.URL, "secret": update.Secret, "content_type": ContentTypeJSON, "insecure_ssl": "0",
	}
	var out HookConfig
	if err := send(ctx, client, http.MethodPatch, APIBase+"/app/hook/config", appToken, body, http.StatusOK, &out); err != nil {
		return HookConfig{}, fmt.Errorf("github: set the App's webhook: %w", err)
	}
	return out, nil
}

// Delivery is one attempt GitHub made, or is making, to deliver an event.
type Delivery struct {
	ID          int64     `json:"id"`
	GUID        string    `json:"guid"`
	DeliveredAt time.Time `json:"delivered_at"`
	Redelivery  bool      `json:"redelivery"`
	Status      string    `json:"status"`
	StatusCode  int       `json:"status_code"`
	Event       string    `json:"event"`
	Action      string    `json:"action"`
}

// ListDeliveries returns the App's most recent webhook deliveries, newest
// first, at most limit of them (`GET /app/hook/deliveries`, an App JWT).
func ListDeliveries(ctx context.Context, client *http.Client, appToken string, limit int) ([]Delivery, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	var out []Delivery
	endpoint := APIBase + "/app/hook/deliveries?per_page=" + strconv.Itoa(limit)
	if _, err := callPage(ctx, client, endpoint, appToken, &out); err != nil {
		return nil, fmt.Errorf("github: list the App's webhook deliveries: %w", err)
	}
	return out, nil
}

// Redeliver asks GitHub to send a delivery again
// (`POST /app/hook/deliveries/{id}/attempts`, an App JWT). GitHub answers 202
// and delivers asynchronously, to the URL and with the secret the App holds
// now.
func Redeliver(ctx context.Context, client *http.Client, appToken string, delivery int64) error {
	endpoint := APIBase + "/app/hook/deliveries/" + strconv.FormatInt(delivery, 10) + "/attempts"
	if err := call(ctx, client, http.MethodPost, endpoint, appToken, http.StatusAccepted, nil); err != nil {
		return fmt.Errorf("github: redeliver %d: %w", delivery, err)
	}
	return nil
}

// Sign is the `X-Hub-Signature-256` value GitHub sends for a body.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Ping POSTs a signed `ping` to a webhook target, as GitHub does when a
// webhook is created, and returns nil only for a 2xx answer. It is how a
// rotation learns that a consumer already holds the new secret before GitHub
// is told to sign with it: a consumer that does not answers 401 or 403, and
// the error says what it answered.
func Ping(ctx context.Context, client *http.Client, target, secret string) error {
	var guid [16]byte
	if _, err := rand.Read(guid[:]); err != nil {
		return fmt.Errorf("github: no randomness for a ping: %w", err)
	}
	body := []byte(`{"zen":"Keep it logically awesome.","hook_id":0,"hook":{"type":"App","events":[],"active":true}}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("github: ping: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "GitHub-Hookshot/sluis")
	request.Header.Set("X-GitHub-Event", "ping")
	request.Header.Set("X-GitHub-Delivery", hex.EncodeToString(guid[:]))
	request.Header.Set("X-Hub-Signature-256", Sign(secret, body))
	response, err := client.Do(request)
	if err != nil {
		// The target is not in the message: a Kargo receiver's path is
		// derived from the secret.
		var failed *url.Error
		if errors.As(err, &failed) {
			err = failed.Err
		}
		return fmt.Errorf("github: ping: %w", err)
	}
	defer response.Body.Close() //nolint:errcheck // a read body's close has nothing to report
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("github: the webhook target answered %s", response.Status)
	}
	return nil
}
