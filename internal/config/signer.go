package config

import (
	"errors"
	"fmt"
	"net/url"
)

// Signer is the signer module as this process reaches it (docs/decisions/0071).
type Signer struct {
	// Remote is where the signer is when it is not this process: the issuer
	// then signs and reads the key set through the signer module there, and
	// this process opens no signing key.
	Remote *SignerRemote `json:"remote,omitempty"`
}

// SignerRemote is the signer module in another process: a Lambda function or a
// Kubernetes Service.
type SignerRemote struct {
	// Function is the module's Lambda function, name or ARN; it is invoked
	// through its `live` alias.
	Function string `json:"function,omitempty"`
	// URL is the module's Service, called with the pod's projected token.
	URL string `json:"url,omitempty"`
	// Audience is the audience of that token; the module's name when unset.
	Audience string `json:"audience,omitempty"`
	// TokenFile is where the projected token is mounted.
	TokenFile string `json:"tokenFile,omitempty"`
}

// Validate holds the section to what its schema cannot say. A nil section is
// valid: the signer is in this process.
func (s *Signer) Validate() error {
	if s == nil || s.Remote == nil {
		return nil
	}
	r := s.Remote
	var errs []error
	switch {
	case (r.Function == "") == (r.URL == ""):
		errs = append(errs, errors.New("signer.remote: set exactly one of function and url"))
	case r.URL != "":
		if u, err := url.Parse(r.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			errs = append(errs, fmt.Errorf("signer.remote.url: %q is not an http(s) URL", r.URL))
		}
	}
	return errors.Join(errs...)
}
