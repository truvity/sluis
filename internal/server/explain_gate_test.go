package server

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/policy"
)

func explainConsole(t *testing.T) (*Console, *audittest.Recorder) {
	t.Helper()
	declared, err := policy.Parse([]byte("version: 1\ngroups:\n  staff:\n    members: [alice@example.com]\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	trail := audittest.New(t)
	return &Console{deps: ConsoleDeps{Authorizer: access.NewAuthorizer(set, nil, 0), Audit: trail}}, trail
}

func explainAs(console *Console, id access.Identity, email string) error {
	ctx := WithIdentity(context.Background(), id)
	_, err := console.Explain(ctx, &directoryrosterv1.ExplainRequest{Email: email})
	return err
}

// Explain discloses what a proof is granted. A roster controller, which is
// a workload, asks in bulk and is not recorded; it is the console's own
// machinery, not somebody looking.
func TestExplainAllowsAControllerWithoutARecord(t *testing.T) {
	t.Parallel()
	console, trail := explainConsole(t)
	controller := access.Identity{Source: access.SourceWorkload, Subject: "system:serviceaccount:sluis:github", Role: access.RoleViewer,
		Groups: []string{policy.GroupRosterControllers}}
	if err := explainAs(console, controller, "alice@example.com"); err != nil {
		t.Fatalf("a controller was refused: %v", err)
	}
	if got := trail.Actions(); len(got) != 0 {
		t.Errorf("recorded %v, want nothing for a controller", got)
	}
}

// An operator may look anyone up, and each look-up is recorded once, with
// counts and no group names.
func TestExplainAllowsAnOperatorAndRecordsItOnce(t *testing.T) {
	t.Parallel()
	console, trail := explainConsole(t)
	operator := access.Identity{Email: "root@example.com", Source: access.SourceOIDC, Role: access.RoleOperator}
	if err := explainAs(console, operator, "alice@example.com"); err != nil {
		t.Fatalf("an operator was refused: %v", err)
	}
	got := trail.Records()
	if len(got) != 1 || got[0].GetAction() != "roster.access.explained" ||
		got[0].GetOutcome().GetResult() != auditv1.Outcome_RESULT_SUCCESS ||
		got[0].GetActor().GetId() != "root@example.com" || got[0].GetSubject().GetId() != "alice@example.com" {
		t.Fatalf("recorded %v", got)
	}
	if strings.Contains(got[0].String(), "staff") {
		t.Error("the record names a group")
	}
}

// A viewer, and a person asking about themselves, are refused with the
// same text, and the refusal is recorded as denied.
func TestExplainRefusesAViewerAndSelfExplainAndRecordsTheDenial(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		id    access.Identity
		email string
	}{
		"a viewer":          {access.Identity{Email: "v@example.com", Source: access.SourceOIDC, Role: access.RoleViewer}, "alice@example.com"},
		"a person, herself": {access.Identity{Email: "alice@example.com", Source: access.SourceOIDC, Role: access.RoleNone}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			console, trail := explainConsole(t)
			err := explainAs(console, tc.id, tc.email)
			if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "not authorised") {
				t.Fatalf("err = %v, want permission denied, not authorised", err)
			}
			got := trail.Records()
			if len(got) != 1 || got[0].GetAction() != "roster.access.explained" ||
				got[0].GetOutcome().GetResult() != auditv1.Outcome_RESULT_DENIED {
				t.Errorf("recorded %v, want one denied record", got)
			}
		})
	}
}

// Holding viewer is not being a controller: another workload with viewer is
// refused and recorded as denied, and one with operator is served and
// recorded, as a person would be.
func TestExplainTreatsAnyOtherWorkloadAsAPerson(t *testing.T) {
	t.Parallel()
	console, trail := explainConsole(t)
	other := access.Identity{Source: access.SourceWorkload, Subject: "system:serviceaccount:team:tool", Role: access.RoleViewer}
	err := explainAs(console, other, "alice@example.com")
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a viewer workload: err = %v, want permission denied", err)
	}
	if got := trail.Records(); len(got) != 1 || got[0].GetOutcome().GetResult() != auditv1.Outcome_RESULT_DENIED {
		t.Errorf("recorded %v, want one denied record", got)
	}

	other.Role = access.RoleOperator
	if err := explainAs(console, other, "alice@example.com"); err != nil {
		t.Fatalf("an operator workload was refused: %v", err)
	}
	got := trail.Records()
	if len(got) != 2 || got[1].GetOutcome().GetResult() != auditv1.Outcome_RESULT_SUCCESS || got[1].GetActor().GetKind() != "workload" {
		t.Errorf("recorded %v, want a success by the workload", got)
	}
}
