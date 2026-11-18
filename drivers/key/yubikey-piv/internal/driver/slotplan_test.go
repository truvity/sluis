//go:build sluis_test

package driver

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/v2/kits/go/pinrelease"
	"github.com/truvity/sluis/v2/kits/go/slotplan"
)

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
	raw, err := os.ReadFile("../../../../../docs/architecture/keys.yaml")
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
	bySlot, byPurpose := map[uint8]string{}, map[string]bool{}
	for _, e := range slotPlan {
		if other, dup := bySlot[uint8(e.slot)]; dup {
			t.Errorf("slot %02x holds both %s and %s", uint8(e.slot), other, e.purpose)
		}
		bySlot[uint8(e.slot)] = e.purpose
		if byPurpose[e.purpose] {
			t.Errorf("purpose %s has two rows", e.purpose)
		}
		byPurpose[e.purpose] = true
	}
	if len(slotPlan) < 9 {
		t.Fatalf("the plan has %d rows: the sweep checked nothing", len(slotPlan))
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
		e, ok := planFor(purpose)
		if !ok {
			// The slots of version 2 that the driver does not serve yet: the map holds them, the card does not.
			if slotplan.CardServed[purpose] {
				t.Errorf("keys.yaml puts %s in slot %s, and the driver has no row for it, though the kit says the card serves it", purpose, slot)
			}
			unserved++
			continue
		}
		if got := e.slot.String(); got != slot {
			t.Errorf("%s: keys.yaml slot %s, driver slot %s", purpose, slot, got)
		}
	}
	for _, e := range slotPlan {
		if !seen[e.purpose] {
			t.Errorf("the driver has a row for %s that keys.yaml's slot plan does not", e.purpose)
		}
	}
	if len(slotPlan)+unserved != len(k.SlotPlan.Slots) {
		t.Errorf("%d rows in the driver and %d slots it does not serve, %d slots in keys.yaml", len(slotPlan), unserved, len(k.SlotPlan.Slots))
	}
	if unserved == 0 {
		t.Fatal("the registry's slot plan holds no slot that the driver lacks: the sweep of version 2's new slots checked nothing")
	}
	// No row of the driver is a slot that a board does not use: the Yubico attestation slot f9 is never touched, and the
	// rotation spares 90 to 95 have no row (a rotation puts its key there by the installation's plan, not by the default map).
	for _, e := range slotPlan {
		n := uint8(e.slot)
		if board := n == 0x9a || n == 0x9c || n == 0x9d || n >= 0x82 && n <= 0x8f; !board {
			t.Errorf("%s: the driver has a row in slot %s, which the default map does not use for a purpose", e.purpose, e.slot)
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
			e, ok := planFor(p.Name)
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
			if norm(e.algorithm) != norm(want) {
				t.Errorf("%s: the card holds %s, keys.yaml says %s", p.Name, e.algorithm, want)
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
	m, err := pinrelease.SlotMapOf(ok)
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
			_, err := pinrelease.SlotMapOf(c.doc)
			if err == nil || !strings.Contains(err.Error(), c.path) || !strings.Contains(err.Error(), "listed twice") {
				t.Fatalf("error = %v, want %q listed twice", err, c.path)
			}
		})
	}
}

// The card driver's table is the default map that key@ checks a KeyPlan against (kits/go/slotplan), slot for slot: key@
// refuses a plan the card would not follow only if the two tables are one.
//
// The kit's map is version 2 and holds more than the card serves: the driver's table is the part the kit lists as served
// (slotplan.CardServed), slot for slot, and has no row for the other slots of the map.
//
// verifies: REQ-KEYS-278
func TestTheDriversTableIsTheSlotPlanKitsDefault(t *testing.T) {
	if len(slotPlan) != len(slotplan.CardServed) {
		t.Fatalf("the driver has %d purposes, the kit lists %d as served", len(slotPlan), len(slotplan.CardServed))
	}
	if len(slotplan.Default) <= len(slotPlan) {
		t.Fatalf("the kit's map holds %d purposes, the driver %d: version 2 holds more than the card serves", len(slotplan.Default), len(slotPlan))
	}
	for _, e := range slotPlan {
		if !slotplan.CardServed[e.purpose] {
			t.Errorf("%s: the driver has a row the kit does not list as served", e.purpose)
		}
		if got := slotplan.Default[e.purpose]; got != e.slot.String() {
			t.Errorf("%s: the driver's slot %s, the kit's %q", e.purpose, e.slot, got)
		}
	}
	for purpose, slot := range slotplan.Default {
		if _, ok := planFor(purpose); !ok && slotplan.CardServed[purpose] {
			t.Errorf("%s: the kit lists slot %s as served, and the driver has no row", purpose, slot)
		}
	}
}
