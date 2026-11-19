package pinrelease

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// verifies: REQ-KEYS-115
// The card driver reads the slot map of the KeyPlan payload as key@ and store@ do: the shared vectors
// (tests/testdata/signed-config/keyplan-payloads.json). Every case that reads gives the same slot map, and every case
// refused in its slot map is refused here too, at the same path.
func TestSlotMapOfTheKeyPlanPayloadVectors(t *testing.T) {
	raw, err := os.ReadFile("../../../tests/testdata/signed-config/keyplan-payloads.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []struct {
			Name        string `json:"name"`
			Payload     string `json:"payload"`
			RefusedPath string `json:"refused_path"`
			Read        *struct {
				SlotMap []struct {
					Node  string `json:"node"`
					Slots []struct {
						Slot              string `json:"slot"`
						Purpose           string `json:"purpose"`
						PublicKey         string `json:"public_key"`
						Attestation       string `json:"attestation"`
						AttestationSigner string `json:"attestation_signer"`
					} `json:"slots"`
				} `json:"slot_map"`
			} `json:"read"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	var read, refused int
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := SlotMapOf([]byte(c.Payload))
			if c.Read == nil {
				if !strings.Contains(c.RefusedPath, "slot_map") {
					return // a refusal outside the slot map is key@'s reading of the rest of the item
				}
				refused++
				if err == nil {
					t.Fatal("want a refusal, got a slot map")
				}
				if !strings.Contains(err.Error(), c.RefusedPath) {
					t.Fatalf("refused at %q, want %q", err.Error(), c.RefusedPath)
				}
				return
			}
			read++
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if len(got) != len(c.Read.SlotMap) {
				t.Fatalf("%d nodes, want %d", len(got), len(c.Read.SlotMap))
			}
			for i, n := range c.Read.SlotMap {
				g := got[i]
				if g.GetNode() != n.Node || len(g.GetSlots()) != len(n.Slots) {
					t.Fatalf("node %d: %v", i, g)
				}
				for j, s := range n.Slots {
					k := g.GetSlots()[j]
					if k.GetSlot() != s.Slot || k.GetPurpose() != s.Purpose || enc.EncodeToString(k.GetPublicKey()) != s.PublicKey ||
						enc.EncodeToString(k.GetAttestation()) != s.Attestation || enc.EncodeToString(k.GetAttestationSigner()) != s.AttestationSigner {
						t.Fatalf("node %d slot %d: %v", i, j, k)
					}
				}
			}
		})
	}
	if read < 5 || refused < 10 {
		t.Fatalf("%d read and %d refused slot map cases: a sweep over nothing proves nothing", read, refused)
	}
}

func TestSignerKeyIsSlot87OfTheNode(t *testing.T) {
	m, err := SlotMapOf([]byte("default:\n  cache_ttl: 15m\n  slot_map:\n" +
		"    - { node: board-a, slots: [{ slot: \"87\", purpose: pin-release, public_key: AQID }] }\n" +
		"    - { node: board-b, slots: [{ slot: \"86\", purpose: pin-release, public_key: AQID }] }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := SignerKey(m, "board-a"); !ok || k.GetSlot() != "87" {
		t.Fatal("board-a has its key in slot 87")
	}
	if _, ok := SignerKey(m, "board-b"); ok {
		t.Fatal("a pin-release key outside slot 87 is taken")
	}
	if _, ok := SignerKey(m, "board-c"); ok {
		t.Fatal("an unlisted node has a key")
	}
}

// An installation may override the map in its signed KeyPlan, and the card driver reads that slot map itself (it does
// not trust key@ to have read it). A map that puts two purposes on one slot, or one purpose on two slots, is refused
// with the path of the second; the same slot on two different nodes is two cards and is fine.
//
// verifies: REQ-KEYS-046
func TestAnInstallationSlotMapCannotShareASlotBetweenKeys(t *testing.T) {
	plan := func(a, b string) []byte {
		return []byte("default:\n  cache_ttl: 15m\n  slot_map:\n    - { node: board-a, slots: [" + a + "] }\n    - { node: board-b, slots: [" + b + "] }\n")
	}
	key := func(slot, purpose string) string {
		return "{ slot: \"" + slot + "\", purpose: " + purpose + ", public_key: AQID }"
	}
	ok := plan(key("9d", "wrapping")+", "+key("9a", "token-signing"), key("9d", "wrapping"))
	m, err := SlotMapOf(ok)
	if err != nil || len(m) != 2 || len(m[0].GetSlots()) != 2 {
		t.Fatalf("a map with a slot of its own for each key was refused or misread: %v, %v", m, err)
	}
	for name, c := range map[string]struct {
		doc  []byte
		path string
	}{
		"two purposes on one slot":       {plan(key("9d", "wrapping")+", "+key("9d", "token-signing"), ""), "slot_map[0].slots[1].slot"},
		"one purpose on two slots":       {plan(key("9d", "wrapping")+", "+key("9a", "wrapping"), ""), "slot_map[0].slots[1].purpose"},
		"a collision on the second node": {plan("", key("85", "release-signing")+", "+key("85", "workload-issuer")), "slot_map[1].slots[1].slot"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := SlotMapOf(c.doc)
			if err == nil || !strings.Contains(err.Error(), c.path) || !strings.Contains(err.Error(), "listed twice") {
				t.Fatalf("error = %v, want %q listed twice", err, c.path)
			}
		})
	}
}
