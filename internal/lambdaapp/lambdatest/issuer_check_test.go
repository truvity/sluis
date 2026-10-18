package lambdatest_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/deploycheck"
	"github.com/truvity/sluis/internal/lambdaapp/lambdatest"
)

// The one scenario that runs on today's code: the check event, invoked through
// the emulator as a deploy would invoke it, answers the declared secrets' report
// and names addresses and versions, never a value.
func TestTheIssuerAnswersACheckEventThroughTheEmulator(t *testing.T) {
	h := lambdatest.NewHarness(t)
	fn := h.OpenIssuer(t)

	res, err := h.Emulator.Invoke(context.Background(), fn, "live", []byte(`{"kind":"check"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.FunctionError != "" {
		t.Fatalf("the check failed: %s", res.Payload)
	}
	var rep deploycheck.Report
	if err = json.Unmarshal(res.Payload, &rep); err != nil {
		t.Fatalf("%v: %s", err, res.Payload)
	}
	if !rep.OK || rep.Layout != "v5" || rep.Root != "/sluis/"+lambdatest.Instance {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Results) != 1 || rep.Results[0].Address != lambdatest.RecoveryVerifierParam || !rep.Results[0].OK {
		t.Fatalf("results = %+v, want the recovery verifier only", rep.Results)
	}
	if strings.Contains(string(res.Payload), "correct horse") {
		t.Errorf("the report carries a secret's value: %s", res.Payload)
	}
	calls := h.Emulator.Calls()
	if len(calls) != 1 || calls[0].Function != fn || calls[0].Qualifier != "live" || calls[0].Class != "" {
		t.Errorf("calls = %+v", calls)
	}
}

// A secret the document declares and SSM lacks fails the invocation, and the
// failure names the address and no value: a deploy stops on it.
func TestTheCheckEventFailsTheInvocationForAMissingSecret(t *testing.T) {
	h := lambdatest.NewHarness(t)
	fn := h.OpenIssuer(t)
	h.SSM.Delete(lambdatest.RecoveryVerifierParam)

	res, err := h.Emulator.Invoke(context.Background(), fn, "live", []byte(`{"kind":"check"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.FunctionError == "" || !strings.Contains(string(res.Payload), lambdatest.RecoveryVerifierParam+": missing") {
		t.Fatalf("function error %q, payload %s", res.FunctionError, res.Payload)
	}
}

// An alias the emulator is not asked about is the caller class: the qualifier
// `live-console` reaches the function as the console, and an unknown function is
// the platform's ResourceNotFoundException.
func TestTheQualifierIsTheCallerClassAndAnUnknownFunctionIsRefused(t *testing.T) {
	h := lambdatest.NewHarness(t)
	fn := h.OpenIssuer(t)
	if _, err := h.Emulator.Invoke(context.Background(), fn, "live-console", []byte(`{"kind":"check"}`)); err != nil {
		t.Fatal(err)
	}
	if c := h.Emulator.Calls(); len(c) != 1 || c[0].Class != "console" {
		t.Errorf("calls = %+v, want the console class", c)
	}
	_, err := h.Emulator.Invoke(context.Background(), "sluis-acme-nothing", "live", []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "ResourceNotFoundException") {
		t.Errorf("an unknown function: %v", err)
	}
}

// TODO(v1.76): the scenarios below need the units named; each is one test over
// this harness.
//
//   - the mint call (V04, V06a): the issuer mints an installation token through
//     githubtokens.NewClient and lambdacall.Caller{API: Emulator.Client()}; the
//     github function answers `mint_installation` for the class `issuer`, and
//     the githubfake organisation sees one access_tokens call.
//   - run-now (V06a): a console run-now invokes the github and slack functions
//     (adapters.trigger: invoke aimed at Emulator.URL()) rather than the issuer.
//   - the live-console refusal (V01, V06a): a call through `live-console` to a
//     method that allows only `issuer` is refused with the caller class named.
//   - the deadline (V01): a callee slower than the envelope's deadline fails the
//     caller with a deadline error, and the emulator cancels the handler's
//     context at the function's configured timeout.
//   - N-1 (V01): a v1.76 caller against a callee that serves only the previous
//     envelope version, and the reverse, both refused or served as ADR 0073 says.
//   - google stale-serve (V03b, V05): with the google function failing, a sign-in
//     whose snapshot is due serves the stale snapshot and triggers an async
//     refresh (D-v176-6 c).
//   - cloudflare status (V06b): the console's Cloudflare page reads the module's
//     `status` method through the emulator.
