package slotplan

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type vectorFile struct {
	Cases []struct {
		Name  string `json:"name"`
		Check []struct {
			Purpose   string `json:"purpose"`
			Algorithm string `json:"algorithm"`
			Slot      string `json:"slot"`
		} `json:"check"`
		Error string `json:"error"`
		Card  string `json:"card"`
	} `json:"cases"`
}

// verifies: REQ-PKI-035, REQ-PKI-037, REQ-PKI-038, REQ-PKI-042, REQ-KEYS-268, REQ-KEYS-279
// The vectors of the slot map check (testdata/vectors.json, shared with the other programs that take a slot map): two
// purposes in one slot (by name or by default) are refused, so are an enabled purpose with no slot, a slot that is no board
// slot, a purpose listed twice and the token-signing key in the intermediate's slot; a map without the slots of purposes that
// are not enabled is accepted. The card check adds the card driver's limit: no override of a slot, no purpose outside the
// map. The RS256 purpose is a card-key purpose like the others: it needs a slot and shares none. The purposes of version 2 take
// their own slots, 89 to 8f, by default and are refused on a card, whose driver has no row for them yet (an algorithm the
// check does not list skips every one of these).
func TestTheSlotMapVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var f vectorFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) < 28 {
		t.Fatalf("%d vectors", len(f.Cases))
	}
	want := func(name, got string, err error) {
		t.Helper()
		switch {
		case got == "" && err != nil:
			t.Errorf("%s: refused: %v", name, err)
		case got != "" && (err == nil || !strings.Contains(err.Error(), got)):
			t.Errorf("%s: err = %v, want %q", name, err, got)
		}
	}
	for _, c := range f.Cases {
		var ps []Purpose
		for _, x := range c.Check {
			ps = append(ps, Purpose{x.Purpose, x.Algorithm, x.Slot})
		}
		want(c.Name+" (check)", c.Error, Check(ps))
		if c.Error == "" {
			want(c.Name+" (card)", c.Card, CheckCard(ps))
		}
	}
}

// The default map is the one in docs/architecture/keys.yaml, slot for slot, version 2: the slots 89 to 8f hold their purposes,
// the rotation spares 90 to 95 hold none, and the card driver serves a subset of the map (the ten rows of version 1).
//
// verifies: REQ-KEYS-046, REQ-KEYS-278
func TestTheDefaultIsTheRegistrys(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/architecture/keys.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var k struct {
		SlotPlan struct {
			Version int               `yaml:"version"`
			Slots   map[string]string `yaml:"slots"`
			Spare   struct {
				From  string `yaml:"from"`
				To    string `yaml:"to"`
				Count int    `yaml:"count"`
			} `yaml:"spare"`
		} `yaml:"slot_plan"`
	}
	if err := yaml.Unmarshal(raw, &k); err != nil {
		t.Fatal(err)
	}
	if len(k.SlotPlan.Slots) != len(Default) || len(Default) < 9 {
		t.Fatalf("keys.yaml has %d slots, the default %d", len(k.SlotPlan.Slots), len(Default))
	}
	for slot, purpose := range k.SlotPlan.Slots {
		if Default[purpose] != slot {
			t.Errorf("%s: keys.yaml slot %s, default %q", purpose, slot, Default[purpose])
		}
	}
	if k.SlotPlan.Version != 2 {
		t.Errorf("keys.yaml slot plan is version %d, the default map is version 2", k.SlotPlan.Version)
	}
	// Version 2 changed only slots that version 1 left spare: the ten rows of version 1 are where they were.
	v1 := map[string]string{"x509-intermediate": "9c", "token-signing": "9a", "wrapping": "9d", "ssh-user": "82", "ssh-host": "83",
		"audit-log": "84", "release-signing": "85", "workload-issuer": "86", "pin-release": "87", "token-signing-rs256": "88"}
	for purpose, slot := range v1 {
		if Default[purpose] != slot {
			t.Errorf("%s: version 1 had slot %s, the default has %q", purpose, slot, Default[purpose])
		}
	}
	// The map holds the pinned purposes in 89 to 8f and nothing in the rotation spares 90 to 95, which the registry names.
	if k.SlotPlan.Spare.From != "90" || k.SlotPlan.Spare.To != "95" || k.SlotPlan.Spare.Count != 6 {
		t.Errorf("keys.yaml spare range = %+v, want 90 to 95 (6)", k.SlotPlan.Spare)
	}
	for purpose, slot := range Default {
		if n, err := strconv.ParseUint(slot, 16, 8); err != nil || n >= 0x90 && n <= 0x95 {
			t.Errorf("%s: slot %s lies in the rotation spares", purpose, slot)
		}
		if !validSlot(slot) {
			t.Errorf("%s: slot %s is no board slot", purpose, slot)
		}
	}
	// What a card serves is part of the map.
	for purpose := range CardServed {
		if _, ok := Default[purpose]; !ok {
			t.Errorf("the card serves %s, which the default map lacks", purpose)
		}
	}
	if len(CardServed) != 10 {
		t.Errorf("the card driver serves %d purposes; this change serves none of the new ones (version 1's ten)", len(CardServed))
	}
}
