package pinrelease

import (
	"errors"
	"fmt"
	"strings"

	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/slotplan"
)

// Since atoom 0.3.0 the card driver serves the purposes its host declares, in the slots it names, and keeps no list of its own. What a
// card of sluis serves is sluis's to say, and stays what ADR 0006 ("Built and planned") records: the ten rows of version 1 of the default
// map (9a, 9c, 9d and 82 to 88), each in its default slot, and no override. The card driver's host declares these (CardPurposes), and key@
// refuses a KeyPlan that a card so declared would not follow (CheckCard). Serving the slots 89 to 8f is the boards work of the roadmap.

// CardServed is the set of purposes a card of sluis serves: the ten rows of version 1.
var CardServed = map[string]bool{
	"x509-intermediate":   true,
	"token-signing":       true,
	"wrapping":            true,
	"ssh-user":            true,
	"ssh-host":            true,
	"audit-log":           true,
	"release-signing":     true,
	"workload-issuer":     true,
	PurposePinRelease:     true,
	"token-signing-rs256": true,
}

// CardPurposes returns the declaration that the host of the card driver gives the driver: the purposes of purpose.Reference() that
// CardServed lists, each in its slot of slotplan.Default. It returns a new slice every time.
func CardPurposes() []purpose.Declaration {
	var out []purpose.Declaration
	for _, d := range purpose.Reference() {
		if CardServed[d.Name] {
			d.Slot = slotplan.Default[d.Name]
			out = append(out, d)
		}
	}
	return out
}

// CheckCard refuses a plan that a card declared by CardPurposes would not follow: a card-key purpose it does not serve (one of the slots
// 89 to 8f, or a purpose the map does not hold), or another slot than its own for one it serves. It adds to slotplan.CheckCard, which
// refuses an algorithm no card holds; call slotplan.Check first. The refusal names the entry and the word, never a key.
func CheckCard(purposes []slotplan.Purpose) error {
	if err := slotplan.CheckCard(purposes); err != nil {
		return err
	}
	var problems []string
	for i, p := range purposes {
		if !onCard(p.Algorithm) {
			continue
		}
		where := fmt.Sprintf("purposes[%d]", i)
		switch {
		case !CardServed[p.Name]:
			problems = append(problems, where+": the card driver serves no slot for this purpose")
		case p.Slot != "" && p.Slot != slotplan.Default[p.Name]:
			problems = append(problems, where+".slot: differs from the slot of the card driver")
		}
	}
	if len(problems) > 0 {
		return errors.New("card slot map: " + strings.Join(problems, "; "))
	}
	return nil
}

// onCard says whether the algorithm is one of a key that lives in a card slot (slotplan's own rule, which it does not export).
func onCard(algorithm string) bool {
	switch algorithm {
	case "ES384", "ES256", "RS256", "ECDH-P384", "ECDH-P-384":
		return true
	}
	return false
}
