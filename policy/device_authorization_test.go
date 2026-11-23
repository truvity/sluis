package policy_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/policy"
)

// `device_authorization` is off unless declared, and only a public client
// with a redirect and an interactive session may declare it.
func TestDeviceAuthorizationIsOptInForAPublicClient(t *testing.T) {
	t.Parallel()

	declared, err := parseSessionPolicy(t, "clients:\n"+
		"  cli: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a], device_authorization: true }\n"+
		"  web: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a] }\n")
	if err != nil {
		t.Fatalf("a public client with device_authorization: %v, want accepted", err)
	}
	if !declared.Clients["cli"].DeviceAuthorization || declared.Clients["web"].DeviceAuthorization {
		t.Errorf("device_authorization read back as cli=%v web=%v", declared.Clients["cli"].DeviceAuthorization,
			declared.Clients["web"].DeviceAuthorization)
	}

	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := set.DeviceClients(); !slices.Equal(got, []string{"cli"}) {
		t.Errorf("DeviceClients = %v, want [cli]", got)
	}
}

func TestDeviceAuthorizationRefusals(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ rest, want string }{
		"on a confidential client": {
			"clients:\n  c: { kind: confidential, secret: s, redirects: ['https://app.example/cb'], requires: [a], device_authorization: true }\n",
			"allows device_authorization but is confidential",
		},
		"on an exchange client": {
			"clients:\n  c: { kind: exchange, requires: [a], device_authorization: true }\n",
			"allows device_authorization but is exchange",
		},
		"without redirects": {
			"clients:\n  c: { kind: public, requires: [a], device_authorization: true }\n",
			"device_authorization and declares no redirects",
		},
		"with an agent session": {
			"clients:\n  c: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a], device_authorization: true, session: agent }\n",
			"session: agent and device_authorization: true",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := parseSessionPolicy(t, tc.rest)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

// The access document's spelling carries the flag into the layer.
func TestTheAccessDocumentCarriesDeviceAuthorization(t *testing.T) {
	t.Parallel()

	declared, err := policy.ParseAccess([]byte("version: 1\n" +
		"access:\n" +
		"  groups: [{ name: a, members: [g@h.example] }]\n" +
		"  clients:\n" +
		"    - { name: cli, kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a], deviceAuthorization: true }\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !declared.Clients["cli"].DeviceAuthorization {
		t.Error("deviceAuthorization did not reach the layer")
	}
}
