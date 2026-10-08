package cloudflare

import (
	"fmt"
	"strings"
	"time"
)

// A token sluis mints is named
//
//	sluis/<instance>/<preset>/<RFC 3339 time>              stored for the preset
//	sluis/<instance>/<preset>/<caller>/<RFC 3339 time>     on demand
//
// The name is the whole of what makes a token sluis's to delete: a sweep only
// ever deletes a token whose name begins with [Prefix] of the preset it is
// sweeping, whatever else is in the account. The trailing slash keeps the
// prefix of "dns" apart from that of "dns-edge".

const namePrefix = "sluis/"

// Prefix is what every token minted for preset by instance begins with.
func Prefix(instance, preset string) string {
	return namePrefix + instance + "/" + preset + "/"
}

// StoredName is the name of the token minted for a preset's stored document.
func StoredName(instance, preset string, at time.Time) string {
	return Prefix(instance, preset) + at.UTC().Format(time.RFC3339)
}

// OnDemandName is the name of a token minted for one caller.
func OnDemandName(instance, preset, caller string, at time.Time) string {
	return Prefix(instance, preset) + Caller(caller) + "/" + at.UTC().Format(time.RFC3339)
}

// IsOwn reports whether a token name is one sluis minted for preset.
func IsOwn(name, instance, preset string) bool {
	return strings.HasPrefix(name, Prefix(instance, preset)) && len(name) > len(Prefix(instance, preset))
}

// IsStored reports whether the name is a stored-variant one: nothing but a
// time follows the prefix.
func IsStored(name, instance, preset string) bool {
	if !IsOwn(name, instance, preset) {
		return false
	}
	rest := strings.TrimPrefix(name, Prefix(instance, preset))
	_, err := time.Parse(time.RFC3339, rest)
	return err == nil
}

// maxCaller bounds the caller's part of a name: Cloudflare bounds a token's
// name, and a long caller would crowd out the time.
const maxCaller = 64

// Caller makes who asked fit one segment of a token name: letters, digits and
// `@ . _ : + -` are kept, anything else (a slash included, which would read
// as another segment) becomes `_`.
func Caller(who string) string {
	var b strings.Builder
	for _, r := range who {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', strings.ContainsRune("@._:+-", r):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= maxCaller {
			break
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

// ValidateInstance refuses an instance name that cannot be part of a token name.
func ValidateInstance(instance string) error {
	if instance == "" || strings.ContainsAny(instance, "/ ") {
		return fmt.Errorf("cloudflare: instance %q cannot name tokens (want a name without / or spaces)", instance)
	}
	return nil
}
