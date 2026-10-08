package catalogue

import "strings"

const (
	// SchemaBase is where this repository's schemas are published and the
	// prefix every schema it generates or ships carries in its $id.
	SchemaBase = "https://truvity.github.io/sluis/schemas/audit/v1/"

	// legacySchemaBase is the prefix the identifiers carried before they moved
	// to GitHub Pages. It never resolved. Catalogues and schemas written under
	// it are in archives that cannot be rewritten, so a reader keeps accepting
	// it, as a name for the same schema, for as long as it reads them.
	legacySchemaBase = "https://schemas.truvity.com/audit/v1/"

	// pagesSchemaBase is the prefix of the audit repository's own GitHub Pages
	// site, which the identifiers carried until audit moved into the sluis
	// repository and its site. It resolved, and archives written under it are
	// read the same way as the older base.
	pagesSchemaBase = "https://truvity.github.io/audit/schemas/v1/"
)

// legacySchemaBases are every prefix an archive may carry for a schema that
// SchemaBase now names.
var legacySchemaBases = []string{legacySchemaBase, pagesSchemaBase}

// CanonicalID names a schema by the identifier new documents use. An id under
// a legacy base maps onto the same path under SchemaBase; any other id is
// returned unchanged, so an adopter's own schemas are untouched.
func CanonicalID(id string) string {
	for _, base := range legacySchemaBases {
		if rest, ok := strings.CutPrefix(id, base); ok {
			return SchemaBase + rest
		}
	}
	return id
}
