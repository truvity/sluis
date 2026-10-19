package legacy

import "github.com/truvity/sluis/internal/port"

// The legacy adapter is today's storage: the namespace's ConfigMaps and
// Secrets and, when configured, Valkey. It needs the cluster, so it works on
// Kubernetes and on a host, never on Lambda. It has no Factory: it takes the
// cluster client and the Valkey connection, which internal/store opens.
func init() {
	for _, c := range []struct {
		concern port.Concern
		summary string
	}{
		{port.ConcernState, "ConfigMaps and, optionally, Valkey sessions and leases; deprecated since v1.74.0, still the default (migrate with `sluis migrate`)."},
		{port.ConcernSecrets, "Kubernetes Secrets the service writes; deprecated since v1.74.0, still the default."},
		{port.ConcernBlobs, "Reports and snapshots in ConfigMaps or Valkey; deprecated since v1.74.0, still the default."},
		{port.ConcernTrigger, "An in-process trigger: a notification reaches only this process."},
	} {
		port.Register(port.Descriptor{
			Name: "legacy", Concern: c.concern, Summary: c.summary,
			Requires: port.Requires{Kubernetes: true},
			Runtimes: []port.Runtime{port.RuntimeKubernetes, port.RuntimeProcess},
			// Secrets are Kubernetes Secrets: a real secret store.
			SecretStore: c.concern == port.ConcernSecrets,
		})
	}
}
