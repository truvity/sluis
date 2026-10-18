package config

import (
	"fmt"
	"time"

	"github.com/truvity/sluis/internal/httpsec"
)

// Policy is the Strict-Transport-Security policy this section says for a
// service published at publicURL. A nil section is the defaults: a year, for the
// host alone (sluis may sit on an apex or parent domain, and `includeSubDomains`
// would pin every sibling to https). A max-age below a second cannot be sent, so only zero turns the
// header off and a negative or fractional one is refused.
func (h *HSTS) Policy(publicURL string) (httpsec.HSTS, error) {
	out := httpsec.HSTS{
		MaxAge:    httpsec.DefaultHSTSMaxAge,
		PublicURL: publicURL,
	}
	if h == nil {
		return out, nil
	}

	if h.MaxAge != nil {
		age := h.MaxAge.D()
		if age < 0 || age%time.Second != 0 {
			return httpsec.HSTS{}, fmt.Errorf("hsts.maxAge %s must be zero or a whole number of seconds", h.MaxAge)
		}

		out.MaxAge = age
	}

	if h.IncludeSubDomains != nil {
		out.IncludeSubDomains = *h.IncludeSubDomains
	}

	return out, nil
}
