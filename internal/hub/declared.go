package hub

// Declared is one workspace the deployment owns, as the service document's
// `directory.workspaces` states it. It is deliberately thin: an id the
// deployment may know, a backend, an admin to act as, and where the
// credential is mounted.
// Everything else — the domains, the tenant's own id — is discovered,
// because a value maintained by hand is a value that drifts.
type Declared struct {
	// ID is the backend's tenant id. Optional: the credential opens
	// exactly one tenant and that tenant knows its own id, so the hub
	// discovers it. Supplying it turns the adoption into a check, which
	// is worth doing where a wrong credential would be quiet.
	ID string `yaml:"id,omitempty"`
	// Backend names the implementation that reads it.
	Backend string `yaml:"backend"`
	// Admin is the account the credential impersonates.
	Admin string `yaml:"admin"`
	// KeyFile is where the service-account key is mounted.
	KeyFile string `yaml:"keyFile"`
	// Serve narrows the tenant to a subset of its domains. Optional:
	// empty serves every domain discovery returns. A domain named here
	// that the tenant does not own routes nothing and is reported as
	// such, which is what makes a domain moving between tenants safe to
	// declare ahead of the move.
	Serve []string `yaml:"serve,omitempty"`
	// SyncGroups narrows the tenant to a subset of its groups. Optional:
	// empty keeps every group in the served domains. Unlike Serve there
	// is no discovery to check it against at start, so a group named here
	// that the tenant does not hold simply never appears.
	SyncGroups []string `yaml:"syncGroups,omitempty"`
}
