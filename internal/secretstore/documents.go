package secretstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/truvity/sluis/storage/state"
)

// The schema names of the external documents. A breaking change is a new
// version, written at <kind>.v2/<id> beside the old one (ADR 0041, decision 2).
const (
	SchemaOIDCv1   = "oidc/v1"
	SchemaGitHubv1 = "github/v1"
	SchemaSlackv1  = "slack/v1"
)

// SchemaS3Credentialsv1 names the internal document of an S3-compatible
// store's static credentials (schemas/internal/s3-credentials.v1.schema.json).
// It is internal, so it is not a contract with anyone outside sluis; the schema
// is pinned for the operator who seeds it.
const SchemaS3Credentialsv1 = "s3-credentials/v1"

// OIDCv1 is the document of a confidential client's secret, at
// external/oidc/<client>. Every field is a JSON string.
type OIDCv1 struct {
	Schema       string `json:"schema"`
	ClientID     string `json:"client-id"`
	ClientSecret string `json:"client-secret"`
}

// GitHubv1 is the document of an installed GitHub App, at
// external/github/<app>. Every field is a JSON string (an App id is "12345").
type GitHubv1 struct {
	Schema         string `json:"schema"`
	AppID          string `json:"app_id"`
	InstallationID string `json:"installation_id"`
	PrivateKey     string `json:"private_key"`
}

// Slackv1 is the document of a Slack App's bot token, at external/slack/<app>.
type Slackv1 struct {
	Schema   string `json:"schema"`
	BotToken string `json:"bot_token"`
}

// S3Credentialsv1 is the document of an S3-compatible store's static
// credentials, at an internal address (ports.blob.s3.credentialsRef). Every
// field is a JSON string.
type S3Credentialsv1 struct {
	Schema          string `json:"schema"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

// ErrSchema is a document that is not the schema the address names, or that
// lacks a field the schema requires.
var ErrSchema = errors.New("secretstore: document does not match its schema")

// documentCodec stores a document of kind schema: the encoder sets the schema
// field, writes the keys sorted and without HTML escaping, and refuses a
// document with a required field empty; the decoder refuses another schema
// and the same lack. A field this version does not know is ignored, because
// adding a field is not breaking.
type documentCodec[T any] struct {
	schema   string
	schemaOf func(*T) *string
	required map[string]func(T) string
}

func (c documentCodec[T]) Marshal(x T) ([]byte, error) {
	*c.schemaOf(&x) = c.schema
	if err := c.check(x); err != nil {
		return nil, err
	}
	b, err := json.Marshal(x)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil { // encoding a map sorts its keys
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func (c documentCodec[T]) Unmarshal(b []byte) (T, error) {
	var x T
	if err := json.Unmarshal(b, &x); err != nil {
		return x, err
	}
	if got := *c.schemaOf(&x); got != c.schema {
		return x, fmt.Errorf("%w: schema %q, want %q", ErrSchema, got, c.schema)
	}
	return x, c.check(x)
}

func (c documentCodec[T]) check(x T) error {
	for name, get := range c.required {
		if get(x) == "" {
			return fmt.Errorf("%w: %s: %s is empty", ErrSchema, c.schema, name)
		}
	}
	return nil
}

var (
	oidcCodec = documentCodec[OIDCv1]{
		schema:   SchemaOIDCv1,
		schemaOf: func(d *OIDCv1) *string { return &d.Schema },
		required: map[string]func(OIDCv1) string{
			"client-id":     func(d OIDCv1) string { return d.ClientID },
			"client-secret": func(d OIDCv1) string { return d.ClientSecret },
		},
	}
	githubCodec = documentCodec[GitHubv1]{
		schema:   SchemaGitHubv1,
		schemaOf: func(d *GitHubv1) *string { return &d.Schema },
		required: map[string]func(GitHubv1) string{
			"app_id":          func(d GitHubv1) string { return d.AppID },
			"installation_id": func(d GitHubv1) string { return d.InstallationID },
			"private_key":     func(d GitHubv1) string { return d.PrivateKey },
		},
	}
	slackCodec = documentCodec[Slackv1]{
		schema:   SchemaSlackv1,
		schemaOf: func(d *Slackv1) *string { return &d.Schema },
		required: map[string]func(Slackv1) string{
			"bot_token": func(d Slackv1) string { return d.BotToken },
		},
	}
)

var s3CredentialsCodec = documentCodec[S3Credentialsv1]{
	schema:   SchemaS3Credentialsv1,
	schemaOf: func(d *S3Credentialsv1) *string { return &d.Schema },
	required: map[string]func(S3Credentialsv1) string{
		"access_key_id":     func(d S3Credentialsv1) string { return d.AccessKeyID },
		"secret_access_key": func(d S3Credentialsv1) string { return d.SecretAccessKey },
	},
}

var _ state.Codec[OIDCv1] = oidcCodec
