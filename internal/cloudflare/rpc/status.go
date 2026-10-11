package rpc

import (
	"context"
	"sort"
	"time"

	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/storage/logattr"
)

// Prototype statuses, as the page shows them.
const (
	PrototypeOK          = "ok"
	PrototypeActive      = "active"
	PrototypeForbidden   = "forbidden"
	PrototypeMissing     = "missing"
	PrototypeUnreachable = "unreachable"
)

// Status is what the console's Cloudflare page shows. Nothing in it carries a
// token's value, and an account is its name and the last four characters of its
// id.
type Status struct {
	Accounts []AccountView `json:"accounts,omitempty"`
	Presets  []PresetView  `json:"presets,omitempty"`
}

// AccountView is an account, its id redacted.
type AccountView struct {
	Name    string `json:"name"`
	IDLast4 string `json:"id_last4"`
}

// PresetView is one preset's live state. A part that could not be read is that
// part's message, never the whole page's: a prototype that is refused is what
// the page exists to show.
type PresetView struct {
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Account     string        `json:"account,omitempty"`
	Endpoint    string        `json:"endpoint,omitempty"`
	Lifetime    time.Duration `json:"lifetime"`
	Rotation    time.Duration `json:"rotation"`
	Prototype   PrototypeView `json:"prototype"`
	Stored      StoredView    `json:"stored"`
	Live        []LiveView    `json:"live,omitempty"`
	LiveError   string        `json:"live_error,omitempty"`
}

// PrototypeView is the preset's prototype token and whether it can be cloned.
type PrototypeView struct {
	ID     string `json:"id,omitempty"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// StoredView is the preset's stored credential, described.
type StoredView struct {
	Present   bool      `json:"present"`
	TokenID   string    `json:"token_id,omitempty"`
	MintedAt  time.Time `json:"minted_at,omitzero"`
	ExpiresOn time.Time `json:"expires_on,omitzero"`
	Error     string    `json:"error,omitempty"`
}

// LiveView is a token of the preset that Cloudflare still accepts.
type LiveView struct {
	ID        string    `json:"id"`
	Caller    string    `json:"caller,omitempty"`
	MintedAt  time.Time `json:"minted_at,omitzero"`
	ExpiresOn time.Time `json:"expires_on,omitzero"`
	Stored    bool      `json:"stored,omitempty"`
}

// last4 redacts an account id to its last four characters.
func last4(id string) string {
	if len(id) <= 4 {
		return id
	}
	return id[len(id)-4:]
}

func prototypeOf(id string, err error) PrototypeView {
	out := PrototypeView{ID: id, Status: PrototypeOK}
	if err == nil {
		return out
	}
	if pe, ok := cloudflare.IsPrototypeError(err); ok {
		out.Detail = pe.Detail
		switch pe.Reason {
		case cloudflare.ReasonPrototypeActive:
			out.Status = PrototypeActive
		case cloudflare.ReasonPrototypeForbidden:
			out.Status = PrototypeForbidden
		default:
			out.Status = PrototypeMissing
		}
		return out
	}
	out.Status = PrototypeUnreachable
	out.Detail = logattr.Error(err)
	return out
}

// BuildStatus reads the page from the module's own minter.
func BuildStatus(ctx context.Context, src Source) Status {
	var out Status
	for _, a := range src.Accounts() {
		out.Accounts = append(out.Accounts, AccountView{Name: a.Name, IDLast4: last4(a.ID)})
	}
	for _, info := range src.Presets() {
		out.Presets = append(out.Presets, presetView(ctx, src, info))
	}
	return out
}

func presetView(ctx context.Context, src Source, info PresetInfo) PresetView {
	protoID, _ := src.PrototypeID(info.Name)
	out := PresetView{
		Name: info.Name, Description: info.Description, Account: info.Account, Endpoint: info.Endpoint,
		Lifetime: info.Lifetime, Rotation: info.Rotation,
		Prototype: prototypeOf(protoID, src.CheckPreset(ctx, info.Name)),
	}
	live, lerr := src.Live(ctx, info.Name)
	if lerr != nil {
		out.LiveError = logattr.Error(lerr)
	}
	stored, serr := src.Stored(ctx, info.Name)
	switch {
	case serr != nil:
		out.Stored = StoredView{Error: logattr.Error(serr)}
	case stored.Present:
		out.Stored = StoredView{Present: true, TokenID: stored.AccessKeyID, MintedAt: stored.MintedAt, ExpiresOn: stored.ExpiresOn}
	}
	for _, t := range live {
		if t.Stored {
			// The stored token is the live stored-named one that expires with the
			// stored document; the others of that name are older ones awaiting
			// their sweep, and are listed as revocable tokens.
			if out.Stored.Present && out.Stored.TokenID == "" && !out.Stored.ExpiresOn.IsZero() &&
				t.ExpiresOn.UTC().Truncate(time.Second).Equal(out.Stored.ExpiresOn.UTC()) {
				out.Stored.TokenID = t.ID
				continue
			}
		}
		out.Live = append(out.Live, LiveView{ID: t.ID, Caller: t.Caller, MintedAt: t.MintedAt, ExpiresOn: t.ExpiresOn, Stored: t.Stored})
	}
	if out.Stored.TokenID != "" {
		// An R2 preset knows its stored token by the access key id; do not list it twice.
		kept := out.Live[:0]
		for _, t := range out.Live {
			if t.ID != out.Stored.TokenID {
				kept = append(kept, t)
			}
		}
		out.Live = kept
	}
	sort.SliceStable(out.Live, func(i, j int) bool { return out.Live[i].ExpiresOn.After(out.Live[j].ExpiresOn) })
	return out
}
