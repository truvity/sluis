// Package slotplan is the check of a card slot map (ADR 0006, "The slot plan"; docs/reference/keys.md, "Board slot plan"):
// which PIV slot each enabled key purpose of a KeyPlan lives in. Every program that takes a slot map asks it the same
// question, so that they refuse the same maps: key@ when it starts and in its check command, and the commands that read a
// configuration before it is used.
//
// The map is the default one (version 2) with the installation's overrides: a purpose names its slot in the KeyPlan, or
// takes the default. A purpose the KeyPlan does not list is not enabled, needs no slot and gets no key (REQ-PKI-042).
package slotplan

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// CardStore is the name a card key-store driver gives itself in Capabilities. key@ asks for it to know that a plan will be
// carried by a card, and the card driver says it with the same constant.
const CardStore = "yubikey-piv"

// Default is the default map, version 2 (decided 2026-11-18, maintainer decision S1 A): the slot of each purpose that has one.
// Version 2 changed only slots that version 1 left spare, so the first ten rows are version 1's. A purpose that is enabled and
// is not listed in a KeyPlan with a slot takes its slot from here, so that enabling a purpose later never renumbers the keys
// of a live card. The slots 90 to 95 have no holder: they are the rotation spares, and a purpose the map does not hold is named
// in a KeyPlan with one of them. Slot 9e is not used, and f9, the Yubico attestation slot, is no board slot at all
// (docs/architecture/keys.yaml, slot_plan). Cloud federation keys and NATS keys are not card keys and have no row.
var Default = map[string]string{
	"x509-intermediate":   "9c",
	"token-signing":       "9a",
	"wrapping":            "9d",
	"ssh-user":            "82",
	"ssh-host":            "83",
	"audit-log":           "84",
	"release-signing":     "85",
	"workload-issuer":     "86",
	"pin-release":         "87",
	"token-signing-rs256": "88",
	"issuance-log":        "89",
	"enrolment":           "8a",
	"token-signing-ps256": "8b",
	"token-signing-es256": "8c",
	"token-signing-eddsa": "8d",
	"witness":             "8e",
	"audit-log-second":    "8f",
}

// CardServed is the set of purposes of Default that the card driver has a row for, which is the whole of what a card serves:
// the ten rows of version 1. The purposes of the slots 89 to 8f have their slot in the map and no row in the driver yet, so
// CheckCard refuses a plan that enables one on a card until the driver serves it (separate work of the boards milestone).
// The driver's own table is held to this set by its tests.
var CardServed = map[string]bool{
	"x509-intermediate":   true,
	"token-signing":       true,
	"wrapping":            true,
	"ssh-user":            true,
	"ssh-host":            true,
	"audit-log":           true,
	"release-signing":     true,
	"workload-issuer":     true,
	"pin-release":         true,
	"token-signing-rs256": true,
}

// Purpose is one entry of a KeyPlan: a purpose, the algorithm of its key and the slot it names ("" for the default).
type Purpose struct {
	Name, Algorithm, Slot string
}

// onCard says whether the algorithm is one of an asymmetric key that lives in a card slot. A data key purpose names
// AES-256-GCM and has no slot. RS256 is listed: a purpose that is left out here skips every slot check, so two purposes
// could be planned in one slot and a card would be given a plan it silently does not follow. Listed, it has the slot 88 by default
// and a slot of its own like the others. PS256 and EdDSA are not listed: no key@ holds a key for them yet (its plan check refuses
// both), and the keys of NATS and of the cloud federations are Ed25519 and are not card keys at all; they join this list with the
// card driver's rows for the slots 8b, 8d and 8e.
func onCard(algorithm string) bool {
	switch algorithm {
	case "ES384", "ES256", "RS256", "ECDH-P384", "ECDH-P-384":
		return true
	}
	return false
}

// validSlot says whether s is a PIV slot a board uses: 9a, 9c, 9d, or a retired slot 82 to 95, in lower-case hex. 9e, the
// card-authentication slot, is not one (it signs without a PIN by default).
func validSlot(s string) bool {
	if len(s) != 2 || s != strings.ToLower(s) {
		return false
	}
	switch s {
	case "9a", "9c", "9d":
		return true
	}
	n, err := strconv.ParseUint(s, 16, 8)
	return err == nil && n >= 0x82 && n <= 0x95
}

// Resolve returns the slot of each card-key purpose of the list: the one it names, else the default. It refuses, with a
// path and a word and never a value, a slot that is no board slot, a purpose with no slot (not named and no default), two
// purposes in one slot and a purpose listed twice. Refusals are collected in the order of the list.
func Resolve(purposes []Purpose) (map[string]string, error) {
	slots := map[string]string{}
	bySlot := map[string]string{}
	var problems []string
	for i, p := range purposes {
		if !onCard(p.Algorithm) {
			continue
		}
		where := fmt.Sprintf("purposes[%d]", i)
		if _, dup := slots[p.Name]; dup {
			problems = append(problems, where+": listed twice")
			continue
		}
		slot := p.Slot
		if slot == "" {
			def, ok := Default[p.Name]
			if !ok {
				problems = append(problems, where+": no slot (name one of the spare slots 90 to 95)")
				continue
			}
			slot = def
		}
		if !validSlot(slot) {
			problems = append(problems, where+".slot: not a slot of a board")
			continue
		}
		if other, taken := bySlot[slot]; taken {
			problems = append(problems, fmt.Sprintf("%s.slot: shared with %s", where, other))
			continue
		}
		slots[p.Name], bySlot[slot] = slot, p.Name
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return nil, fmt.Errorf("card slot map: %s", strings.Join(problems, "; "))
	}
	return slots, nil
}

// Check is Resolve without the map.
func Check(purposes []Purpose) error {
	_, err := Resolve(purposes)
	return err
}

// CheckCard is the check of a plan against the card driver, which serves the rows of CardServed and nothing else: it keeps no
// override of a slot and has no key for a purpose without a row. A plan that names another slot for a purpose it serves, or
// lists a card-key purpose it does not serve (one of the slots 89 to 8f, or a purpose the map does not hold), is refused, so
// that the plan and the card never say different things. The refusal names the entry and the word, never a key. Call Check first; this adds the card's own limit.
func CheckCard(purposes []Purpose) error {
	var problems []string
	for i, p := range purposes {
		if !onCard(p.Algorithm) {
			continue
		}
		where := fmt.Sprintf("purposes[%d]", i)
		def := Default[p.Name]
		switch {
		case !CardServed[p.Name]:
			problems = append(problems, where+": the card driver serves no slot for this purpose")
		case p.Slot != "" && p.Slot != def:
			problems = append(problems, where+".slot: differs from the slot of the card driver")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("card slot map: %s", strings.Join(problems, "; "))
	}
	return nil
}
