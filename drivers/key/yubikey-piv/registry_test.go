package main

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/truvity/atoom/key/slotplan"
	"github.com/truvity/atoom/key/yubikeypiv"
)

// The slot plan is atoom's (github.com/truvity/atoom/key/slotplan is the check, yubikeypiv.SlotPlan is the card driver's table), and the
// registry of the purposes and their slots, docs/architecture/keys.yaml, is sluis's. These tests hold the two to each other: the tables
// agree with the registry, slot for slot and algorithm for algorithm.

var hexSlot = regexp.MustCompile(`^[0-9a-f]{2}$`)

func slotOf(s string) uint8 {
	n, err := strconv.ParseUint(s, 16, 8)
	if err != nil {
		panic(err)
	}
	return uint8(n)
}

func planFor(rows []yubikeypiv.Row, purpose string) (yubikeypiv.Row, bool) {
	for _, r := range rows {
		if r.Purpose == purpose {
			return r, true
		}
	}
	return yubikeypiv.Row{}, false
}

// keysYAML is the part of docs/architecture/keys.yaml that the slot plan is checked against.
type keysYAML struct {
	Purposes []struct {
		Name      string `yaml:"name"`
		Algorithm string `yaml:"algorithm"`
		Board     struct {
			Slot      string `yaml:"slot"`
			Algorithm string `yaml:"algorithm"`
			Runtimes  []string
		} `yaml:"board"`
	} `yaml:"purposes"`
	SlotPlan struct {
		Slots  map[string]string `yaml:"slots"`
		Spare  struct{ From, To string }
		Unused map[string]string `yaml:"unused"`
	} `yaml:"slot_plan"`
}

func readKeysYAML(t *testing.T) keysYAML {
	t.Helper()
	raw, err := os.ReadFile("../../../docs/architecture/keys.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var k keysYAML
	if err := yaml.Unmarshal(raw, &k); err != nil {
		t.Fatal(err)
	}
	return k
}

// The default slot plan of the card driver gives each key its own slot (ADR 0006, "The slot plan"). Three things are
// checked together: no slot holds two purposes and no purpose sits in two slots, so no key shares a card slot with
// another; the slot of each purpose is the slot that docs/architecture/keys.yaml gives it, with the algorithm that
// the card holds there; and a purpose of the registry's slot plan that the driver has no row for yet (the slots 89 to 8f of
// version 2) is not served from a slot of the default map. The installation's own slot map is checked where it is read, by
// DecodeSlotMap (kits/go/pinrelease).
//
// verifies: REQ-KEYS-046
func TestTheDefaultSlotPlanGivesEveryKeyASlotOfItsOwn(t *testing.T) {
	rows := yubikeypiv.SlotPlan()
	bySlot, byPurpose := map[uint8]string{}, map[string]bool{}
	for _, e := range rows {
		if other, dup := bySlot[slotOf(e.Slot)]; dup {
			t.Errorf("slot %02x holds both %s and %s", slotOf(e.Slot), other, e.Purpose)
		}
		bySlot[slotOf(e.Slot)] = e.Purpose
		if byPurpose[e.Purpose] {
			t.Errorf("purpose %s has two rows", e.Purpose)
		}
		byPurpose[e.Purpose] = true
	}
	if len(rows) < 9 {
		t.Fatalf("the plan has %d rows: the sweep checked nothing", len(rows))
	}

	k := readKeysYAML(t)
	if len(k.Purposes) == 0 || len(k.SlotPlan.Slots) == 0 {
		t.Fatal("keys.yaml gave no purposes or no slot plan")
	}
	// keys.yaml names the same slots and purposes as the driver, once each.
	seen := map[string]bool{}
	unserved := 0
	for slot, purpose := range k.SlotPlan.Slots {
		if seen[purpose] {
			t.Errorf("keys.yaml puts %s in two slots", purpose)
		}
		seen[purpose] = true
		e, ok := planFor(rows, purpose)
		if !ok {
			// The slots of version 2 that the driver does not serve yet: the map holds them, the card does not.
			if slotplan.CardServed[purpose] {
				t.Errorf("keys.yaml puts %s in slot %s, and the driver has no row for it, though the kit says the card serves it", purpose, slot)
			}
			unserved++
			continue
		}
		if got := e.Slot; got != slot {
			t.Errorf("%s: keys.yaml slot %s, driver slot %s", purpose, slot, got)
		}
	}
	for _, e := range rows {
		if !seen[e.Purpose] {
			t.Errorf("the driver has a row for %s that keys.yaml's slot plan does not", e.Purpose)
		}
	}
	if len(rows)+unserved != len(k.SlotPlan.Slots) {
		t.Errorf("%d rows in the driver and %d slots it does not serve, %d slots in keys.yaml", len(rows), unserved, len(k.SlotPlan.Slots))
	}
	if unserved == 0 {
		t.Fatal("the registry's slot plan holds no slot that the driver lacks: the sweep of version 2's new slots checked nothing")
	}
	// No row of the driver is a slot that a board does not use: the Yubico attestation slot f9 is never touched, and the
	// rotation spares 90 to 95 have no row (a rotation puts its key there by the installation's plan, not by the default map).
	for _, e := range rows {
		n := slotOf(e.Slot)
		if board := n == 0x9a || n == 0x9c || n == 0x9d || n >= 0x82 && n <= 0x8f; !board {
			t.Errorf("%s: the driver has a row in slot %s, which the default map does not use for a purpose", e.Purpose, e.Slot)
		}
	}

	norm := func(s string) string { return strings.ReplaceAll(s, "-", "") }
	pinned := 0
	for _, p := range k.Purposes {
		switch p.Board.Slot {
		case "none":
		case "spare":
			// Version 2 keeps no purpose in a spare slot: a purpose has a pinned slot or none (the key service's keys).
			t.Errorf("%s: keys.yaml gives it a spare slot, and version 2 pins every card purpose", p.Name)
		default:
			pinned++
			e, ok := planFor(rows, p.Name)
			if !ok {
				// A purpose of version 2 that the driver does not serve yet: its slot is the kit's, and no row is the driver's.
				if slotplan.CardServed[p.Name] {
					t.Errorf("%s: keys.yaml gives it slot %s, and the driver has no row", p.Name, p.Board.Slot)
				}
				if got := slotplan.Default[p.Name]; got != p.Board.Slot {
					t.Errorf("%s: keys.yaml slot %s, the kit's default %q", p.Name, p.Board.Slot, got)
				}
				continue
			}
			want := p.Algorithm
			if p.Board.Algorithm != "" {
				want = p.Board.Algorithm
			}
			if norm(e.Algorithm) != norm(want) {
				t.Errorf("%s: the card holds %s, keys.yaml says %s", p.Name, e.Algorithm, want)
			}
			if k.SlotPlan.Slots[p.Board.Slot] != p.Name {
				t.Errorf("%s: board.slot %s, but the slot plan puts %s there", p.Name, p.Board.Slot, k.SlotPlan.Slots[p.Board.Slot])
			}
		}
	}
	// One purpose and one algorithm per card key: no two purposes of the registry are the same name. The keys that live in the
	// key service (the NATS keys, the cloud federations) have no card slot.
	if pinned != len(k.SlotPlan.Slots) {
		t.Errorf("%d purposes have a card slot in keys.yaml, the slot plan holds %d", pinned, len(k.SlotPlan.Slots))
	}
	for _, p := range k.Purposes {
		if p.Name == "nats" && p.Board.Slot != "none" {
			t.Errorf("nats has slot %q: the NATS keys live in the key service, not on a card", p.Board.Slot)
		}
	}
	names := map[string]bool{}
	for _, p := range k.Purposes {
		if names[p.Name] {
			t.Errorf("purpose %s is listed twice in keys.yaml", p.Name)
		}
		names[p.Name] = true
	}
	// The unused slot of the plan is no row's slot.
	for slot := range k.SlotPlan.Unused {
		if p, ok := k.SlotPlan.Slots[slot]; ok {
			t.Errorf("slot %s is both unused and the slot of %s", slot, p)
		}
	}
}

// The default map is the one in docs/architecture/keys.yaml, slot for slot, version 2: the slots 89 to 8f hold their purposes,
// the rotation spares 90 to 95 hold none, and the card driver serves a subset of the map (the ten rows of version 1).
//
// verifies: REQ-KEYS-046, REQ-KEYS-278
func TestTheDefaultMapIsTheRegistrys(t *testing.T) {
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
	if len(k.SlotPlan.Slots) != len(slotplan.Default) || len(slotplan.Default) < 9 {
		t.Fatalf("keys.yaml has %d slots, the default %d", len(k.SlotPlan.Slots), len(slotplan.Default))
	}
	for slot, purpose := range k.SlotPlan.Slots {
		if slotplan.Default[purpose] != slot {
			t.Errorf("%s: keys.yaml slot %s, default %q", purpose, slot, slotplan.Default[purpose])
		}
	}
	if k.SlotPlan.Version != 2 {
		t.Errorf("keys.yaml slot plan is version %d, the default map is version 2", k.SlotPlan.Version)
	}
	// Version 2 changed only slots that version 1 left spare: the ten rows of version 1 are where they were.
	v1 := map[string]string{"x509-intermediate": "9c", "token-signing": "9a", "wrapping": "9d", "ssh-user": "82", "ssh-host": "83",
		"audit-log": "84", "release-signing": "85", "workload-issuer": "86", "pin-release": "87", "token-signing-rs256": "88"}
	for purpose, slot := range v1 {
		if slotplan.Default[purpose] != slot {
			t.Errorf("%s: version 1 had slot %s, the default has %q", purpose, slot, slotplan.Default[purpose])
		}
	}
	// The map holds the pinned purposes in 89 to 8f and nothing in the rotation spares 90 to 95, which the registry names.
	if k.SlotPlan.Spare.From != "90" || k.SlotPlan.Spare.To != "95" || k.SlotPlan.Spare.Count != 6 {
		t.Errorf("keys.yaml spare range = %+v, want 90 to 95 (6)", k.SlotPlan.Spare)
	}
	for purpose, slot := range slotplan.Default {
		if n, err := strconv.ParseUint(slot, 16, 8); err != nil || n >= 0x90 && n <= 0x95 {
			t.Errorf("%s: slot %s lies in the rotation spares", purpose, slot)
		}
		if !hexSlot.MatchString(slot) {
			t.Errorf("%s: slot %s is no board slot", purpose, slot)
		}
	}
	// What a card serves is part of the map.
	for purpose := range slotplan.CardServed {
		if _, ok := slotplan.Default[purpose]; !ok {
			t.Errorf("the card serves %s, which the default map lacks", purpose)
		}
	}
	if len(slotplan.CardServed) != 10 {
		t.Errorf("the card driver serves %d purposes; this change serves none of the new ones (version 1's ten)", len(slotplan.CardServed))
	}
}
