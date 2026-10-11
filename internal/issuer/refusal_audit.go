package issuer

import (
	"context"
	"errors"
	"slices"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/policy"
)

// A refusal tells its caller "not authorised" and a request id. What the
// audit record under that id keeps is the evaluation: this file builds it,
// from the same resolution and the same rules that made the decision, so the
// record cannot say something the decision did not.

// codedReason is a refusal's reason with the code the audit record files it
// under. It travels as the parent of the error the caller is given, and
// never reaches the caller.
type codedReason struct {
	code string
	err  error
}

func (c *codedReason) Error() string { return c.err.Error() }
func (c *codedReason) Unwrap() error { return c.err }

// reasonCodeOf is the code a refusal carries, or fallback.
func reasonCodeOf(err error, fallback string) string {
	var coded *codedReason
	if errors.As(err, &coded) {
		return coded.code
	}
	return fallback
}

// directoryState is what a proof's groups were read from.
func directoryState(person, held bool, err error) string {
	var refused *Refused
	switch {
	case !person:
		return "not_applicable"
	case errors.As(err, &refused) && refused.Authoritative:
		// The directory answered, and the answer was no.
		return "live"
	case err != nil:
		return "unavailable"
	case held:
		return "held"
	}
	return "live"
}

// admission is what a client's requirements make of the groups a caller holds:
// the groups it requires that the caller lacks, those that admitted them, and
// whether any did.
func admission(client policy.Client, result policy.Result) (missing, by []string, admitted bool) {
	for _, group := range client.Requires {
		if slices.Contains(result.Groups, group) {
			by = append(by, group)
		} else {
			missing = append(missing, group)
		}
	}
	return missing, by, client.Admits(result)
}

// exchangeRefusal is the evaluation behind a refused exchange of proof for
// audience, filed under code. Evaluating again costs nothing: a request
// resolves an address once.
func (i *Issuer) exchangeRefusal(ctx context.Context, proof Proof, audience, code string) audit.Refusal {
	why := audit.Refusal{Code: code, Audience: audience}
	result, held, err := i.evaluate(ctx, proof)
	why.Directory = directoryState(proof.Email != "", held, err)
	if err != nil {
		return why
	}
	why.Held = slices.Clone(result.Groups)
	if client, ok := i.set.Client(audience); ok {
		why.Missing, why.AdmittedBy, why.Admitted = admission(client, result)
	}
	return why
}

// signInRefusal is the evaluation behind a refused sign-in of subject to
// client: the same resolution [Storage.entitled] applies.
func (s *Storage) signInRefusal(ctx context.Context, clientID, subject string, serviceAccount bool, code string) audit.Refusal {
	why := audit.Refusal{Code: code, Audience: clientID}
	result, _, _, err := s.resolveSubject(ctx, subject, serviceAccount)
	why.Directory = directoryState(!serviceAccount, false, err)
	if err != nil {
		return why
	}
	if !serviceAccount {
		if resolved, rerr := s.iss.resolver.Resolve(ctx, subject); rerr == nil && resolved.Held {
			why.Directory = "held"
		}
	}
	why.Held = slices.Clone(result.Groups)
	if client, ok := s.iss.Policy().Client(clientID); ok {
		why.Missing, why.AdmittedBy, why.Admitted = admission(client, result)
	}
	return why
}
