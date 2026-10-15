package issuer

import (
	"context"
	"net/http"
)

// SluisConfigurationPath is where a signed-in caller learns where the
// installation's backup and restore functions are, so that `sluisctl login`
// can save the addresses and nobody types them. It is not OpenID discovery
// (that stays at `/.well-known/openid-configuration` and is public): this
// document is about the installation, and it is served only to a caller that
// presents a token this issuer signed.
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

// sluisConfigurationHandler serves the document to a caller with a verified
// token of this issuer. Any such token will do: the document names addresses
// and the installation, and nothing a caller could not learn from being granted
// the access the functions themselves enforce. It is withheld from anonymous
// callers, so that the issuer does not tell the internet where its functions are.
func sluisConfigurationHandler(iss *Issuer, verify func(context.Context, string) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "this endpoint answers GET", http.StatusMethodNotAllowed)
			return
		}
		bearer := bearerFrom(r)
		if bearer == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "a bearer token is required", http.StatusUnauthorized)
			return
		}
		if err := verify(r.Context(), bearer); err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			http.Error(w, "that token was not accepted", http.StatusUnauthorized)
			return
		}
		doc, ok := iss.sluisConfiguration()
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, doc)
	})
}
