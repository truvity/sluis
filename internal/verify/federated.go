package verify

import (
	"net/http"
	"strings"
)

// Federation is the set of clusters whose ServiceAccount tokens this
// installation will act on: the policy document's exchange.clusters
// (internal/config), held to their rules when the document is loaded. It is the whole of what makes one issuer
// serve many clusters, and it holds no secret: every row is a name and
// two URLs.
type Federation struct {
	Clusters []FederatedCluster `yaml:"clusters"`
}

// FederatedCluster is one cluster's row.
type FederatedCluster struct {
	// Name is the estate's own word for the cluster — `dev`, `prod`
	// — the same one that scopes a group name. It becomes the cluster in
	// a `workload` matcher and in the subject of the minted token, so a
	// row renamed is every rule about it changed.
	Name string `yaml:"name"`
	// Issuer is the `iss` its ServiceAccount tokens carry: for EKS the
	// cluster's OIDC provider URL, for Talos whatever
	// `--service-account-issuer` says.
	Issuer string `yaml:"issuer"`
	// JWKSURI is where its public keys are. Empty discovers it from the
	// issuer, which works wherever the cluster publishes a discovery
	// document at an address this service can reach.
	JWKSURI string `yaml:"jwksUri,omitempty"`
}

// Verifiers turns the rows into verifiers, one per cluster, each
// answering only for tokens carrying its own issuer.
func (f Federation) Verifiers(audience string, httpClient *http.Client) []*Cluster {
	out := make([]*Cluster, 0, len(f.Clusters))
	for _, row := range f.Clusters {
		out = append(out, &Cluster{
			Name:     strings.TrimSpace(row.Name),
			Issuer:   strings.TrimSpace(row.Issuer),
			JWKSURI:  strings.TrimSpace(row.JWKSURI),
			Audience: audience,
			Client:   httpClient,
		})
	}
	return out
}

// Names lists the clusters, for the line an operator reads at start.
func (f Federation) Names() []string {
	names := make([]string, 0, len(f.Clusters))
	for _, row := range f.Clusters {
		names = append(names, row.Name)
	}
	return names
}
