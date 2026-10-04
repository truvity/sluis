package access

// ServiceAccountSubject is the username a Kubernetes API server gives a
// ServiceAccount: what a policy's service_account matcher and a recovery
// account are compared against. It lives here, with no cluster client behind
// it, so the Lambda build can name a subject without linking one.
func ServiceAccountSubject(namespace, name string) string {
	return "system:serviceaccount:" + namespace + ":" + name
}
