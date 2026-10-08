// Package schemas carries the JSON Schemas this module publishes, so a
// consumer in another module can embed them in its own schema without a copy.
package schemas

import _ "embed" // the schema files

// Keys is keys.schema.json: the `keys:` block of a configuration file.
//
//go:embed keys.schema.json
var Keys []byte
