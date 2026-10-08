package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Rev identifies one version of a key. It is opaque: compare it for
// equality, never for order. The empty Rev means "no version".
type Rev string

// Item is one version of a key as a backend holds it.
type Item struct {
	// Value is the stored JSON object.
	Value []byte
	// Rev is this version.
	Rev Rev
	// Modified is when this version was written, as the backend reports it.
	Modified time.Time
	// Previous is the version this one replaced, or empty for the first
	// version of a key. [Value.Rotating] reads it.
	Previous Rev
}

var (
	// ErrNotFound: the key, or the requested version of it, does not exist.
	ErrNotFound = errors.New("state: not found")
	// ErrConflict: a conditional Put saw a version other than ifRev.
	ErrConflict = errors.New("state: revision conflict")
	// ErrNotObject: a value to store is not a JSON object.
	ErrNotObject = errors.New("state: value is not a JSON object")
	// ErrInvalidKey: a key or prefix is empty, absolute or has an empty,
	// "." or ".." segment.
	ErrInvalidKey = errors.New("state: invalid key")
)

// Store is a versioned key-value store under a prefix. Implementations are
// safe for concurrent use.
type Store interface {
	// Get returns the latest version of key, or ErrNotFound.
	Get(ctx context.Context, key string) (Item, error)
	// GetRev returns the version rev of key, or ErrNotFound.
	GetRev(ctx context.Context, key string, rev Rev) (Item, error)
	// Put writes value as a new version of key and returns its Rev. ifRev
	// is the Rev the caller last saw; "" means the key must not exist.
	// Any other current version is ErrConflict. value must be a JSON
	// object (ErrNotObject).
	Put(ctx context.Context, key string, value []byte, ifRev Rev) (Rev, error)
	// Delete removes key and all its versions. A missing key is
	// ErrNotFound.
	Delete(ctx context.Context, key string) error
	// List returns the names of the keys directly under this store's
	// prefix, sorted. Keys nested deeper are not listed.
	List(ctx context.Context) ([]string, error)
	// Child returns the store for prefix below this one. Options
	// configure the backend for that sub-store; a backend ignores the
	// ones it has no use for.
	Child(prefix string, opts ...Option) Store
}

// Options is what the [Option]s given to [Store.Child] resolve to. A backend
// calls [ResolveOptions]; no other code needs it.
type Options struct {
	// KeyAlias names the encryption key a backend uses at rest (for
	// example a KMS key alias). Empty means the backend default.
	KeyAlias string
	// Region is the backend's region. Empty means its default.
	Region string
	// Endpoint is the backend's endpoint URL, for an S3-compatible
	// service or a local emulator. Empty means its default.
	Endpoint string
}

// Option configures a Child store.
type Option func(*Options)

// WithKeyAlias sets [Options.KeyAlias].
func WithKeyAlias(alias string) Option { return func(o *Options) { o.KeyAlias = alias } }

// WithRegion sets [Options.Region].
func WithRegion(region string) Option { return func(o *Options) { o.Region = region } }

// WithEndpoint sets [Options.Endpoint].
func WithEndpoint(endpoint string) Option { return func(o *Options) { o.Endpoint = endpoint } }

// ResolveOptions applies opts on top of base.
func ResolveOptions(base Options, opts ...Option) Options {
	for _, opt := range opts {
		if opt != nil {
			opt(&base)
		}
	}
	return base
}

// ValidateObject returns ErrNotObject unless value is one JSON object.
func ValidateObject(value []byte) error {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return ErrNotObject
	}
	return nil
}

// ValidateKey returns ErrInvalidKey unless key is a relative,
// slash-separated path of non-empty segments, none of them "." or "..".
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: empty", ErrInvalidKey)
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("%w: %q", ErrInvalidKey, key)
		}
	}
	return nil
}

// Codec turns a T into the JSON object a Store keeps and back.
type Codec[T any] interface {
	Marshal(T) ([]byte, error)
	Unmarshal([]byte) (T, error)
}

type jsonCodec[T any] struct{}

// JSON returns the codec that stores T as itself. T must marshal to a JSON
// object (a struct or a map), or Marshal returns ErrNotObject.
func JSON[T any]() Codec[T] { return jsonCodec[T]{} }

func (jsonCodec[T]) Marshal(x T) ([]byte, error) {
	b, err := json.Marshal(x)
	if err != nil {
		return nil, err
	}
	if err := ValidateObject(b); err != nil {
		return nil, err
	}
	return b, nil
}

func (jsonCodec[T]) Unmarshal(b []byte) (T, error) {
	var x T
	err := json.Unmarshal(b, &x)
	return x, err
}

type rawCodec struct{}

type rawDoc struct {
	Value []byte `json:"value"`
}

// Raw returns the codec that stores opaque bytes as {"value": "<base64>"}.
func Raw() Codec[[]byte] { return rawCodec{} }

func (rawCodec) Marshal(b []byte) ([]byte, error) { return json.Marshal(rawDoc{Value: b}) }

func (rawCodec) Unmarshal(b []byte) ([]byte, error) {
	var d rawDoc
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	return d.Value, nil
}

// Value is a typed handle on one key of a Store.
type Value[T any] struct {
	store Store
	key   string
	codec Codec[T]
	now   func() time.Time
}

// NewValue returns the handle on key of s, encoded by c.
func NewValue[T any](s Store, key string, c Codec[T]) Value[T] {
	return Value[T]{store: s, key: key, codec: c, now: time.Now}
}

// WithClock returns v reading the time from now, which [Value.Rotating]
// compares to Item.Modified. It is for tests.
func (v Value[T]) WithClock(now func() time.Time) Value[T] {
	v.now = now
	return v
}

// Get returns the latest value and its Rev, or ErrNotFound.
func (v Value[T]) Get(ctx context.Context) (T, Rev, error) {
	var zero T
	it, err := v.store.Get(ctx, v.key)
	if err != nil {
		return zero, "", err
	}
	x, err := v.codec.Unmarshal(it.Value)
	if err != nil {
		return zero, "", fmt.Errorf("state: decode %q: %w", v.key, err)
	}
	return x, it.Rev, nil
}

// Put stores x as a new version, conditional on ifRev (see [Store.Put]).
func (v Value[T]) Put(ctx context.Context, x T, ifRev Rev) (Rev, error) {
	b, err := v.codec.Marshal(x)
	if err != nil {
		return "", fmt.Errorf("state: encode %q: %w", v.key, err)
	}
	return v.store.Put(ctx, v.key, b, ifRev)
}

// Rotating returns the current value and, when the current version was
// written less than grace ago and replaced an earlier one, that earlier
// value. After the grace period previous is nil. A key with a single
// version has no previous.
func (v Value[T]) Rotating(ctx context.Context, grace time.Duration) (current T, previous *T, err error) {
	it, err := v.store.Get(ctx, v.key)
	if err != nil {
		return current, nil, err
	}
	current, err = v.codec.Unmarshal(it.Value)
	if err != nil {
		return current, nil, fmt.Errorf("state: decode %q: %w", v.key, err)
	}
	if it.Previous == "" || v.now().Sub(it.Modified) >= grace {
		return current, nil, nil
	}
	old, err := v.store.GetRev(ctx, v.key, it.Previous)
	if errors.Is(err, ErrNotFound) {
		return current, nil, nil // the old version was pruned: nothing to fall back to
	}
	if err != nil {
		return current, nil, err
	}
	p, err := v.codec.Unmarshal(old.Value)
	if err != nil {
		return current, nil, fmt.Errorf("state: decode %q@%s: %w", v.key, it.Previous, err)
	}
	return current, &p, nil
}
