package issuer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/truvity/audit/sdk/record"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/policy"
)

// ClientSecretsPath is where an operator manages the stored secrets of the
// clients the issuer generates (`secret: {generate: true}`): POST
// `<path>/rotate`, `<path>/show` and `<path>/purge`, each with a JSON body
// naming the client. Nothing here ever returns a secret.
//
// It is authorised like the session service's operator-only calls: the
// caller's own bearer, from this issuer, whose `groups` hold the operators
// group (policy.GroupOperators). Nothing else admits a caller, and the groups
// are the ones in the token, not a claim the request makes.
const ClientSecretsPath = "/.access/client-secrets"

// ClientSecretAdmin is what the endpoint asks for; [clientcreds.Manager] is
// one.
type ClientSecretAdmin interface {
	Rotate(ctx context.Context, clientID string, overlap time.Duration) (clientcreds.Rotation, error)
	Show(ctx context.Context, clientID string) (clientcreds.Meta, error)
	Purge(ctx context.Context, clientID string) error
}

// ClientSecretsAudiences are the clients whose tokens may manage secrets:
// sluisctl's (`accessctl`, its default) and the console's. A token minted for
// any other client, however valid and whatever its groups, is refused.
var ClientSecretsAudiences = []string{"accessctl", "console"}

// UseClientSecrets mounts the operator endpoint over admin. Without it the
// endpoint answers 404.
func (i *Issuer) UseClientSecrets(admin ClientSecretAdmin) { i.clientSecrets = admin }

// Record writes one audit record down, or nothing where no recorder was given.
// The issuer's host application calls it for what happens outside a request.
func (i *Issuer) Record(ctx context.Context, r *record.Record) { i.record(ctx, r) }

type clientSecretRequest struct {
	Client string `json:"client"`
	// OverlapSeconds is how long the replaced secret stays accepted. Absent
	// is [clientcreds.DefaultOverlap]; zero is a hard cut.
	OverlapSeconds *int64 `json:"overlap_seconds,omitempty"`
}

type clientSecretMeta struct {
	Client             string `json:"client"`
	Exists             bool   `json:"exists"`
	Generated          bool   `json:"generated"`
	Created            string `json:"created,omitempty"`
	Rotated            string `json:"rotated,omitempty"`
	HasPrevious        bool   `json:"has_previous"`
	PreviousValidUntil string `json:"previous_valid_until,omitempty"`
	PreviousActive     bool   `json:"previous_active"`
	Orphaned           string `json:"orphaned,omitempty"`
}

type clientSecretRotated struct {
	Client             string `json:"client"`
	Rotated            string `json:"rotated"`
	OverlapSeconds     int64  `json:"overlap_seconds"`
	PreviousValidUntil string `json:"previous_valid_until,omitempty"`
	DiscardedPrevious  bool   `json:"discarded_previous"`
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// clientSecretsHandler serves the operator endpoint. verify turns the caller's
// bearer into who they are and the groups the token carries.
func clientSecretsHandler(
	iss *Issuer, verify func(context.Context, string) (string, []string, []string, error),
) http.Handler {
	mux := http.NewServeMux()
	handle := func(name string, do func(w http.ResponseWriter, r *http.Request, admin ClientSecretAdmin, identity string, req clientSecretRequest)) {
		mux.HandleFunc("POST "+ClientSecretsPath+"/"+name, func(w http.ResponseWriter, r *http.Request) {
			admin := iss.clientSecrets
			if admin == nil {
				http.NotFound(w, r)
				return
			}
			bearer := bearerFrom(r)
			if bearer == "" {
				refuseUnauthenticated(r, name)
				http.Error(w, "a bearer token is required", http.StatusUnauthorized)
				return
			}
			identity, groups, audiences, err := verify(r.Context(), bearer)
			if err != nil {
				refuseUnauthenticated(r, name)
				http.Error(w, "that token was not accepted", http.StatusUnauthorized)
				return
			}
			// From here the caller is a verified identity, so a refusal is
			// audited. The body is not read yet: somebody who may not manage
			// secrets learns nothing about which clients exist.
			if !slices.ContainsFunc(audiences, func(a string) bool { return slices.Contains(ClientSecretsAudiences, a) }) {
				clientcreds.CountAdminRefused(r.Context(), "wrong_audience")
				iss.record(r.Context(), audit.ClientSecretDenied(audit.Identified(identity), "", name, "wrong_audience", nil))
				http.Error(w, "this token was not issued for a client that manages secrets", http.StatusForbidden)
				return
			}
			operator := false
			for _, g := range groups {
				operator = operator || g == policy.GroupOperators
			}
			if !operator {
				clientcreds.CountAdminRefused(r.Context(), "forbidden")
				iss.record(r.Context(), audit.ClientSecretDenied(audit.Identified(identity), "", name, "forbidden", nil))
				http.Error(w, "managing client secrets is an operator's", http.StatusForbidden)
				return
			}
			var req clientSecretRequest
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
			dec.DisallowUnknownFields()
			if err = dec.Decode(&req); err != nil || strings.TrimSpace(req.Client) == "" {
				http.Error(w, "the body is JSON naming the client", http.StatusBadRequest)
				return
			}
			do(w, r, admin, identity, req)
		})
	}
	handle("rotate", func(w http.ResponseWriter, r *http.Request, admin ClientSecretAdmin, identity string, req clientSecretRequest) {
		overlap := clientcreds.DefaultOverlap
		if req.OverlapSeconds != nil {
			// Bounded before it becomes a Duration, which wraps.
			if secs := *req.OverlapSeconds; secs < 0 || secs > int64(clientcreds.MaxOverlap/time.Second) {
				iss.record(r.Context(), audit.ClientSecretDenied(audit.Identified(identity), req.Client, "rotate", "bad_overlap", req.OverlapSeconds))
				http.Error(w, "the overlap is between 0 and 7 days", http.StatusBadRequest)
				return
			}
			overlap = time.Duration(*req.OverlapSeconds) * time.Second
		}
		res, err := admin.Rotate(r.Context(), req.Client, overlap)
		if err != nil {
			iss.deny(r.Context(), identity, req.Client, "rotate", err, req.OverlapSeconds)
			clientSecretError(w, err)
			return
		}
		iss.record(r.Context(), audit.ClientSecretRotated(audit.Identified(identity), req.Client,
			res.Rotated, res.Overlap, res.PreviousValidUntil, res.DiscardedPrevious))
		writeJSON(w, clientSecretRotated{
			Client: req.Client, Rotated: stamp(res.Rotated), OverlapSeconds: int64(res.Overlap / time.Second),
			PreviousValidUntil: stamp(res.PreviousValidUntil), DiscardedPrevious: res.DiscardedPrevious,
		})
	})
	handle("show", func(w http.ResponseWriter, r *http.Request, admin ClientSecretAdmin, identity string, req clientSecretRequest) {
		meta, err := admin.Show(r.Context(), req.Client)
		if err != nil {
			iss.deny(r.Context(), identity, req.Client, "show", err, nil)
			clientSecretError(w, err)
			return
		}
		writeJSON(w, clientSecretMeta{
			Client: req.Client, Exists: meta.Exists, Generated: meta.Generated,
			Created: stamp(meta.Created), Rotated: stamp(meta.Rotated),
			HasPrevious: meta.HasPrevious, PreviousValidUntil: stamp(meta.PreviousValidUntil),
			PreviousActive: meta.PreviousActive, Orphaned: stamp(meta.Orphaned),
		})
	})
	handle("purge", func(w http.ResponseWriter, r *http.Request, admin ClientSecretAdmin, identity string, req clientSecretRequest) {
		if err := admin.Purge(r.Context(), req.Client); err != nil {
			iss.deny(r.Context(), identity, req.Client, "purge", err, nil)
			clientSecretError(w, err)
			return
		}
		iss.record(r.Context(), audit.ClientSecretDeleted(audit.Identified(identity), req.Client))
		writeJSON(w, map[string]any{"client": req.Client, "deleted": true})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// clientSecretError answers a refusal. The messages are the package's own and
// hold no value.
func clientSecretError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, clientcreds.ErrOverlap):
		http.Error(w, "the overlap is between 0 and 7 days", http.StatusBadRequest)
	case errors.Is(err, clientcreds.ErrNotGenerated):
		http.Error(w, "that client does not have `secret: {generate: true}`", http.StatusUnprocessableEntity)
	case errors.Is(err, clientcreds.ErrStillDeclared):
		http.Error(w, "that client is still a generated client of the policy; only an orphan is purged", http.StatusUnprocessableEntity)
	case errors.Is(err, clientcreds.ErrNoRecord):
		http.Error(w, "that client has no stored secret", http.StatusNotFound)
	case errors.Is(err, clientcreds.ErrBusy):
		http.Error(w, "the client's secret is being changed; try again", http.StatusConflict)
	default:
		http.Error(w, "the secret could not be changed", http.StatusInternalServerError)
	}
}

// refuseUnauthenticated is a request with no verified identity: logged and
// counted, never audited, since there is no actor to name.
func refuseUnauthenticated(r *http.Request, action string) {
	clientcreds.CountAdminRefused(r.Context(), "unauthenticated")
	slog.WarnContext(r.Context(), "a request to manage client secrets carried no accepted token", "action", action)
}

// denialReason is the audit reason for a refusal the caller can be told of; ""
// for any other error, which is not a refusal.
func denialReason(err error) string {
	switch {
	case errors.Is(err, clientcreds.ErrOverlap):
		return "bad_overlap"
	case errors.Is(err, clientcreds.ErrNotGenerated):
		return "not_generated"
	case errors.Is(err, clientcreds.ErrStillDeclared):
		return "still_declared"
	case errors.Is(err, clientcreds.ErrNoRecord):
		return "no_record"
	case errors.Is(err, clientcreds.ErrBusy):
		return "busy"
	}
	return ""
}

// deny records a refused admin request by a verified caller.
func (i *Issuer) deny(ctx context.Context, identity, client, action string, err error, overlap *int64) {
	if reason := denialReason(err); reason != "" {
		i.record(ctx, audit.ClientSecretDenied(audit.Identified(identity), client, action, reason, overlap))
	}
}
