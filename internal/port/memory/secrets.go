package memory

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/truvity/sluis/internal/port"
)

// Secrets is the in-memory [port.Secrets]: values in process memory, a
// version per write. It is not a secret store (nothing is encrypted or
// audited) and is for tests and the demonstration.
type Secrets struct {
	mu      sync.Mutex
	version uint64
	items   map[string]secret
}

type secret struct {
	value   []byte
	version string
}

var _ port.Secrets = (*Secrets)(nil)

// NewSecrets returns an empty store.
func NewSecrets() *Secrets { return &Secrets{items: map[string]secret{}} }

func (s *Secrets) put(path string, value []byte) string {
	s.version++
	v := strconv.FormatUint(s.version, 10)
	s.items[path] = secret{value: slices.Clone(value), version: v}
	return v
}

// Get implements [port.Secrets].
func (s *Secrets) Get(_ context.Context, path string) (port.Secret, error) {
	if err := port.CheckSecretPath(path); err != nil {
		return port.Secret{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[path]
	if !ok {
		return port.Secret{}, port.ErrNotFound
	}
	return port.Secret{Value: slices.Clone(it.value), Version: it.version}, nil
}

// Put implements [port.Secrets].
func (s *Secrets) Put(_ context.Context, path string, value []byte) (string, error) {
	if err := port.CheckSecretWrite(path, value); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.put(path, value), nil
}

// PutIfVersion implements [port.Secrets].
func (s *Secrets) PutIfVersion(_ context.Context, path string, value []byte, version string) (string, error) {
	if err := port.CheckSecretWrite(path, value); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[path]
	switch {
	case version == "" && ok:
		return "", port.ErrConflict
	case version != "" && !ok:
		return "", port.ErrNotFound
	case version != "" && it.version != version:
		return "", port.ErrConflict
	}
	return s.put(path, value), nil
}

// Delete implements [port.Secrets].
func (s *Secrets) Delete(_ context.Context, path string) error {
	if err := port.CheckSecretPath(path); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, path)
	return nil
}

// List implements [port.Secrets].
func (s *Secrets) List(_ context.Context, prefix string) ([]string, error) {
	p, err := port.SecretPrefix(prefix)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for k := range s.items {
		if strings.HasPrefix(k, p) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out, nil
}
