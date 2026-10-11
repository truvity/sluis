package issuer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/policy"
)

// PolicyExplainPath is where an operator asks what a token for a client would
// carry for a person or a workload: POST with a JSON body
// `{"audience": "<client>", "email": "<address>"}` or
// `{"audience": "<client>", "subject": "<workload subject>"}`.
//
// The answer is [policy.Explanation], the value `sluisctl explain` prints
// offline, computed against the policy in force and the directory snapshots
// the hub already holds. It reads snapshots only: it never asks the directory,
// never refreshes a snapshot and never changes what the issuer remembers about
// the person, and it mints and signs nothing.
//
// It is authorised like the client-secrets endpoint: the caller's own bearer,
// issued by this issuer to sluisctl or the console, whose `groups` hold the
// operators group (policy.GroupOperators). Explaining another person's token
// discloses their group memberships, so a person without that group may not
// explain even their own, and every ask by a verified caller is audited as
// roster.policy.explained, the refused ones included.
const PolicyExplainPath = "/.access/policy/explain"

type policyExplainRequest struct {
	Audience string `json:"audience"`
	Email    string `json:"email,omitempty"`
	Subject  string `json:"subject,omitempty"`
}

// policyExplainHandler serves the explain action. verify turns the caller's
// bearer into who they are, the groups the token carries and its audiences.
func policyExplainHandler(
	iss *Issuer, verify func(context.Context, string) (string, []string, []string, error),
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "POST a JSON body naming the audience and the subject", http.StatusMethodNotAllowed)
			return
		}
		bearer := bearerFrom(r)
		if bearer == "" {
			http.Error(w, "a bearer token is required", http.StatusUnauthorized)
			return
		}
		identity, groups, audiences, err := verify(r.Context(), bearer)
		if err != nil {
			http.Error(w, "that token was not accepted", http.StatusUnauthorized)
			return
		}
		actor := audit.Identified(identity)
		denied := func(reason string, status int, message string) {
			iss.record(r.Context(), audit.PolicyExplained(actor, "", "", "", nil, audit.Denied(reason)))
			http.Error(w, message, status)
		}
		// The body is not read before the caller is known to be an operator:
		// somebody who may not explain learns nothing about which clients exist.
		if !slices.ContainsFunc(audiences, func(a string) bool { return slices.Contains(ClientSecretsAudiences, a) }) {
			denied("wrong_audience", http.StatusForbidden, "this token was not issued for a client that explains policy")
			return
		}
		if !slices.ContainsFunc(groups, policy.IsOperators) {
			denied("forbidden", http.StatusForbidden, "explaining a token is an operator's")
			return
		}
		var req policyExplainRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&req); err != nil {
			http.Error(w, "the body is JSON naming the audience and an email or a subject", http.StatusBadRequest)
			return
		}
		req.Audience, req.Email, req.Subject = strings.TrimSpace(req.Audience), strings.TrimSpace(req.Email), strings.TrimSpace(req.Subject)
		if req.Audience == "" || (req.Email == "") == (req.Subject == "") {
			http.Error(w, "name the audience and exactly one of email and subject", http.StatusBadRequest)
			return
		}
		explained, subject, proof, err := iss.explain(r.Context(), req)
		result := &audit.PolicyExplain{Held: len(explained.Held), Kept: len(explained.Kept), Admitted: explained.Admitted}
		var refused *Refused
		switch {
		case err == nil:
			iss.record(r.Context(), audit.PolicyExplained(actor, subject, proof, req.Audience, result, audit.Succeeded()))
			writeJSON(w, explained)
		case errors.Is(err, errExplainBadSubject):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, errExplainUnknownAudience):
			iss.record(r.Context(), audit.PolicyExplained(actor, subject, proof, "", nil, audit.Denied("unknown_client")))
			http.Error(w, err.Error(), http.StatusNotFound)
		case errors.As(err, &refused):
			iss.record(r.Context(), audit.PolicyExplained(actor, subject, proof, req.Audience, nil, audit.Denied("not_live")))
			http.Error(w, "the issuer would refuse this account a token: "+refused.Reason, http.StatusUnprocessableEntity)
		default:
			iss.record(r.Context(), audit.PolicyExplained(actor, subject, proof, req.Audience, nil, audit.Failed("unavailable")))
			http.Error(w, "the explanation could not be made", http.StatusServiceUnavailable)
		}
	})
}

var (
	errExplainBadSubject      = errors.New("the subject is neither a ServiceAccount nor an AWS role subject")
	errExplainUnknownAudience = errors.New("that audience is not a declared client")
)

// Explain says what a token for audience would carry for a person (email) or a
// workload (subject), against the policy in force and the directory snapshots.
// It is a read: see [PolicyExplainPath].
func (i *Issuer) Explain(ctx context.Context, audience, email, subject string) (policy.Explanation, error) {
	explained, _, _, err := i.explain(ctx, policyExplainRequest{Audience: audience, Email: email, Subject: subject})
	return explained, err
}

// explain returns the explanation, the subject as the trail names it and how
// it was read ("person" or "workload").
func (i *Issuer) explain(ctx context.Context, req policyExplainRequest) (policy.Explanation, string, string, error) {
	var in policy.Input
	subject, proof := req.Subject, "workload"
	if req.Email != "" {
		subject, proof = strings.ToLower(req.Email), "person"
		resolved, err := i.resolver.Snapshot(ctx, subject)
		if err != nil {
			return policy.Explanation{}, subject, proof, err
		}
		in = resolved.Input(subject)
	} else {
		var ok bool
		if in, ok = policy.WorkloadInput(subject); !ok {
			return policy.Explanation{}, "", "", errExplainBadSubject
		}
	}
	explained, err := i.set.Explain(req.Audience, in)
	if err != nil {
		return policy.Explanation{}, subject, proof, errExplainUnknownAudience
	}
	return explained, subject, proof, nil
}
