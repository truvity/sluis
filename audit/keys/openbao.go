package keys

// JWTLogin is how a workload signs in to OpenBAO without a stored secret: it
// presents a JWT — normally its projected service-account token — to a JWT
// auth mount, under a role that binds the token's subject and audience and
// names the policies the session gets. It is the way the estate's workloads
// already reach OpenBAO, and it means there is no long-lived token anywhere to
// leak or rotate.
type JWTLogin struct {
	// Mount is the auth mount, e.g. jwt-devel: one per cluster, inside the
	// environment's namespace.
	Mount string
	// Role is the role on that mount.
	Role string
	// TokenFile holds the JWT. It is read at every login, because the kubelet
	// replaces a projected token before it expires.
	TokenFile string
}
