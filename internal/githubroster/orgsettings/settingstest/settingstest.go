// Package settingstest is an organisation's own account in memory: the
// document and the SecureString parameters the controller reads with the
// role it assumes. For tests only.
package settingstest

import (
	"context"
	"errors"
	"sync"

	"github.com/truvity/sluis/internal/githubroster/orgsettings"
)

// Parameter is one parameter of the account.
type Parameter struct {
	Value   string
	Version int64
}

// Source is one organisation's account.
type Source struct {
	mu sync.Mutex
	// Doc is the document's text; DocErr, when set, is why it cannot be read.
	Doc    string
	DocErr error
	// Parameters are the account's SecureString parameters, by name.
	Parameters map[string]Parameter
	// Reads counts the values decrypted, by parameter name: a secret that
	// did not change is not decrypted again.
	Reads map[string]int
}

// New is an account holding the document.
func New(doc string) *Source {
	return &Source{Doc: doc, Parameters: map[string]Parameter{}, Reads: map[string]int{}}
}

// SetDocument replaces the document.
func (s *Source) SetDocument(doc string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Doc = doc
}

// Put writes a parameter, which is a new version when it exists.
func (s *Source) Put(name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Parameters[name] = Parameter{Value: value, Version: s.Parameters[name].Version + 1}
}

// Document implements [orgsettings.Source].
func (s *Source) Document(context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.DocErr != nil {
		return nil, s.DocErr
	}

	return []byte(s.Doc), nil
}

// Version implements [orgsettings.Secrets].
func (s *Source) Version(_ context.Context, name string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.Parameters[name]
	if !ok {
		return 0, errors.New("ParameterNotFound: " + name)
	}

	return p.Version, nil
}

// Value implements [orgsettings.Secrets].
func (s *Source) Value(_ context.Context, name string) (string, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.Parameters[name]
	if !ok {
		return "", 0, errors.New("ParameterNotFound: " + name)
	}

	s.Reads[name]++

	return p.Value, p.Version, nil
}

// Reader is the sources of several organisations.
type Reader map[string]*Source

// For implements the controller's SettingsReader.
func (r Reader) For(org string) (orgsettings.Source, bool) {
	s, ok := r[org]
	if !ok {
		return nil, false
	}

	return s, true
}
