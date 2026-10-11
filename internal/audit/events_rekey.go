package audit

import "github.com/truvity/sluis/audit/sdk/record"

// The phases of the record `sluis secrets rekey` keeps twice.
const (
	// RekeyStarted is kept before the first parameter is written.
	RekeyStarted = "started"
	// RekeyCompleted is kept after the last one.
	RekeyCompleted = "completed"
)

// SecretsRekey is what `sluis secrets rekey` records: names and counts, never a
// value.
type SecretsRekey struct {
	Installation string
	// Key is the alias the parameters are re-written under.
	Key   string
	Phase string
	// Considered, Planned, Rekeyed and Deferred are counts of parameters.
	Considered, Planned, Rekeyed, Deferred int
	// Names are the parameters to re-write.
	Names []string
}

// SecretsRekeyed is an operator re-writing the installation's SecureString
// parameters under the key the document names. The catalogue declares it
// block: the started record is kept before the first write.
func SecretsRekeyed(actor Actor, r SecretsRekey, o Outcome) *record.Record {
	return build("roster.secrets.rekeyed", actor, o, subjectOf(actor),
		[]*record.Target{{Type: "installation", Id: r.Installation}},
		data{"installation": r.Installation, "key": r.Key, "phase": r.Phase, "considered": r.Considered,
			"planned": r.Planned, "rekeyed": r.Rekeyed, "deferred": r.Deferred, "names": r.Names})
}
