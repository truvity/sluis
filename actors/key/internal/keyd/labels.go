package keyd

import "strings"

// reservedLabelPrefixes are the labels of Mac that only one actor may use, by prefix. A label separates two uses of one
// key, and for a few of them the output is not an identifier but a credential, so that holding the key's rule (every
// actor with the boundary `mac` key may call Mac) must not be enough to make one.
//
// sluis-refresh- is the tag that is the secret of a refresh token (session's sluis-refresh-tag-v1): HMAC of the tenant,
// the sid, the generation and the chain's salt. The sid and the generation are in every token and in the session chain,
// which other actors may read, and the salt is sealed under the tenant's chain key, which exchange can open, so whoever
// could ask Mac for such a label could make the refresh token of any chain it can read. Only session makes and compares
// them. A prefix and not a list, so that a rename or a new version in session (-v2) stays reserved without a change
// here.
var reservedLabelPrefixes = map[string]string{
	"sluis-refresh-": "session",
}

// labelAllowed says the caller may use the label: any label that is not reserved, and a reserved one only for its
// actor.
func labelAllowed(caller, label string) bool {
	for prefix, owner := range reservedLabelPrefixes {
		if strings.HasPrefix(label, prefix) && owner != caller {
			return false
		}
	}
	return true
}
