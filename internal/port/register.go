package port

// The adapters of the concerns whose wiring is the process's own: the
// signing keys are read by the issuer, the schedule is the controllers'
// ticker, the audit sink is the audit package's. They are described here so
// the table and the matrix name them; none has a Factory.
func init() {
	Register(Descriptor{
		Name: "file", Concern: ConcernSigning,
		Summary: "Signing keys read from files the platform mounts (`signingKey`).",
	})
	Register(Descriptor{
		Name: "ticker", Concern: ConcernSchedule,
		Summary: "An in-process interval timer.",
		// A Lambda has no process between invocations to keep a timer in.
		Runtimes: []Runtime{RuntimeKubernetes, RuntimeProcess},
	})
	Register(Descriptor{
		Name: "connect", Concern: ConcernAudit,
		Summary: "Events sent to the audit installation's receiver (`audit.writer`).",
	})
	Register(Descriptor{
		Name: "log", Concern: ConcernAudit,
		Summary: "Events written to the log only.",
	})
}
