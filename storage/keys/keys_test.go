package keys_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/truvity/sluis/storage/keys"
	"github.com/truvity/sluis/storage/keys/local"
)

func parse(t *testing.T, s string) (keys.Config, error) {
	t.Helper()
	var c keys.Config
	err := json.Unmarshal([]byte(s), &c)
	return c, err
}

func TestConfigForms(t *testing.T) {
	c, err := parse(t, `{"adapter":"kms","sign":"alias/a","seal":{"key":"alias/b","context":"off"},
		"conceal":{"key":"alias/c","context":{"app":"x"}},"archive":{"key":"alias/d"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Keys[keys.Sign].Key != "alias/a" || c.Keys[keys.Sign].Context.Mode != "" {
		t.Fatalf("short form: %+v", c.Keys[keys.Sign])
	}
	if c.Keys[keys.Seal].Context.Mode != keys.ContextOff {
		t.Fatal("off")
	}
	if m := c.Keys[keys.Conceal].Context; m.Mode != keys.ContextMap || m.Map["app"] != "x" {
		t.Fatalf("map: %+v", m)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var again keys.Config
	if err := json.Unmarshal(out, &again); err != nil {
		t.Fatal(err)
	}
	if again.Keys[keys.Seal].Context.Mode != keys.ContextOff || again.Keys[keys.Sign].Key != "alias/a" {
		t.Fatalf("round trip: %s", out)
	}
}

func TestConfigRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"unknown purpose": {`{"adapter":"kms","encrypt":"alias/a"}`, "unknown purpose"},
		"bad context":     {`{"adapter":"kms","sign":{"key":"alias/a","context":"sometimes"}}`, "default"},
		"empty map":       {`{"adapter":"kms","sign":{"key":"alias/a","context":{}}}`, `"off"`},
		"unknown field":   {`{"adapter":"kms","sign":{"key":"alias/a","ctx":"off"}}`, "unknown field"},
	} {
		if _, err := parse(t, tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, tc := range map[string]struct{ in, want string }{
		"no adapter":  {`{"sign":"alias/a"}`, "adapter is required"},
		"bad adapter": {`{"adapter":"hsm","sign":"alias/a"}`, "not one of"},
		"no keys":     {`{"adapter":"kms"}`, "no purpose"},
		"arn":         {`{"adapter":"kms","sign":"arn:aws:kms:eu-west-1:111122223333:key/abc"}`, "ARN"},
		"key id":      {`{"adapter":"kms","sign":"1234abcd-12ab-34cd-56ef-1234567890ab"}`, "key id"},
		"mrk id":      {`{"adapter":"kms","sign":"mrk-1234567890abcdef1234567890abcdef"}`, "key id"},
		"empty key":   {`{"adapter":"kms","sign":""}`, "empty"},
	} {
		c, err := parse(t, tc.in)
		if err == nil {
			err = c.Validate()
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestOpen(t *testing.T) {
	b, _ := local.New(local.RandomRoot())
	cfg := keys.Config{Adapter: "local", Keys: map[keys.Purpose]keys.Entry{keys.Seal: {Key: "seal-key"}}}

	if _, err := keys.Open(cfg, keys.Options{Backend: b}); err == nil || !strings.Contains(err.Error(), "Instance") {
		t.Fatalf("default context without an instance: %v", err)
	}
	ks, err := keys.Open(cfg, keys.Options{Backend: b, Instance: "prod-1"})
	if err != nil {
		t.Fatal(err)
	}
	k, _ := ks.For(keys.Seal)
	if got := k.Context(); len(got) != 2 || got["instance"] != "prod-1" || got["purpose"] != "seal" {
		t.Fatalf("default context: %v", got)
	}
	k.Context()["instance"] = "tampered"
	if k.Context()["instance"] != "prod-1" {
		t.Fatal("Context exposes internal state")
	}

	off := keys.Config{Adapter: "local", Keys: map[keys.Purpose]keys.Entry{
		keys.Seal: {Key: "seal-key", Context: keys.ContextSpec{Mode: keys.ContextOff}}}}
	if _, err := keys.Open(off, keys.Options{Backend: b}); err != nil {
		t.Fatalf("off needs no instance: %v", err)
	}

	cfg.Adapter = "kms"
	if _, err := keys.Open(cfg, keys.Options{Backend: b, Instance: "i"}); err == nil || !strings.Contains(err.Error(), "adapter") {
		t.Fatalf("adapter mismatch: %v", err)
	}
	bad := keys.Config{Adapter: "local", Keys: map[keys.Purpose]keys.Entry{keys.Seal: {Key: "alias/x"}}}
	if _, err := keys.Open(bad, keys.Options{Backend: b, Instance: "i"}); err == nil || !strings.Contains(err.Error(), "keys.seal") {
		t.Fatalf("backend name check: %v", err)
	}
	if _, err := keys.Open(cfg, keys.Options{Instance: "i"}); err == nil {
		t.Fatal("no backend accepted")
	}
}

func TestMACNeedsBackendSupport(t *testing.T) {
	b, _ := local.New(local.RandomRoot())
	ks, _ := keys.Open(keys.Config{Adapter: "local", Keys: map[keys.Purpose]keys.Entry{keys.Pseudonym: {Key: "p"}}},
		keys.Options{Backend: noMAC{b}, Instance: "i"})
	k, _ := ks.For(keys.Pseudonym)
	if _, err := k.MAC(t.Context(), "t", []byte("x")); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatal(err)
	}
}

// noMAC hides the MAC method of the embedded backend.
type noMAC struct{ keys.Backend }

func TestToJOSE(t *testing.T) {
	if _, err := keys.ToJOSE([]byte{1, 2, 3}, 48); err == nil {
		t.Fatal("garbage accepted")
	}
}
