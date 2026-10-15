package issuer

import (
	"context"
	"net/http"
)

// SluisConfigurationPath is where a signed-in caller learns where the
// installation's backup and restore functions are, so that `sluisctl login`
// can save the addresses and nobody types them. It is not OpenID discovery
// (that stays at `/.well-known/openid-configuration` and is public): this
// document is about the installation, and it is served only to a person signed
// in at the CLI (see [Storage.CLISubject]); anyone else gets a 404.
const SluisConfigurationPath = "/.well-known/sluis-configuration"

// SluisConfiguration is the document. Every endpoint is a URI whose scheme
// names the transport a caller reaches the function by:
//
//	arn:aws:lambda:<region>:<account>:function:<name>   invoke it with AWS credentials
//
// `k8s://` and `https://` are reserved for the other transports; a client that
// meets a scheme it cannot use says so and does not guess. An endpoint is
// listed only when a laptop could use it: a function that only a cluster-local
// URL reaches is not.
type SluisConfiguration struct {
	// Issuer is this issuer's URL.
	Issuer string `json:"issuer"`
	// Instance is the installation's name, which `sluisctl restore start
	// --confirm` must match.
	Instance string `json:"instance,omitempty"`
	// Region is the AWS region of a Lambda endpoint, when there is one.
	Region  string               `json:"region,omitempty"`
	Backup  *SluisConfigEndpoint `json:"backup,omitempty"`
	Restore *SluisConfigEndpoint `json:"restore,omitempty"`
}

// SluisConfigEndpoint is one function's address.
type SluisConfigEndpoint struct {
	Endpoint string `json:"endpoint"`
}

// UseConfiguration sets the document the issuer serves at
// [SluisConfigurationPath]: the installation's own part. The issuer fills in its
// URL. Without it the path answers 404, as an issuer that knows no function has
// nothing to say.
func (i *Issuer) UseConfiguration(c SluisConfiguration) { i.sluisConfig.Store(&c) }

func (i *Issuer) sluisConfiguration() (SluisConfiguration, bool) {
	if i == nil {
		return SluisConfiguration{}, false
	}
	p := i.sluisConfig.Load()
	if p == nil {
		return SluisConfiguration{}, false
	}
	out := *p
	out.Issuer = i.Config().URL
	return out, true
}

// sluisConfigurationHandler serves the document to a caller that is signed in
// at the CLI: a live person's sign-in (see [Storage.CLISubject]), not any token
// this issuer signed. The document names addresses and the installation, and the
// issuer does not tell anyone else where its functions are. Everyone else, with
// no token or a token that does not count, gets the 404 an issuer with nothing
// to say gets, so the path reveals nothing.
func sluisConfigurationHandler(iss *Issuer, allowed func(context.Context, string) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "this endpoint answers GET", http.StatusMethodNotAllowed)
			return
		}
		doc, ok := iss.sluisConfiguration()
		if bearer := bearerFrom(r); !ok || bearer == "" || !allowed(r.Context(), bearer) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, doc)
	})
}
