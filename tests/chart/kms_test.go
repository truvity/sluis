package chart_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func caseValues(name string) string {
	return filepath.Join("..", "cases", "sluis", name, "values.yaml")
}

func volumeNames(t *testing.T, deployment map[string]any) (volumes, mounts []string) {
	t.Helper()
	pod, ok := dig(deployment, "spec", "template", "spec")
	if !ok {
		t.Fatal("no pod spec")
	}
	for _, v := range pod.(map[string]any)["volumes"].([]any) {
		volumes = append(volumes, v.(map[string]any)["name"].(string))
	}
	for _, c := range pod.(map[string]any)["containers"].([]any) {
		for _, m := range c.(map[string]any)["volumeMounts"].([]any) {
			mounts = append(mounts, m.(map[string]any)["name"].(string))
		}
	}
	return volumes, mounts
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// With KMS-wrapped signing nothing is issued and nothing is mounted for a key
// file: no Certificate, no signing Secret volume. What the openbao Secrets
// adapter needs is: the CA, the projected token, and the inputs as files.
func TestKMSWrappedSigningRendersNoKeyAndOpenBaoSecretsMountWhatTheyNeed(t *testing.T) {
	for _, name := range []string{"k8s-aws-kms-openbao", "k8s-aws-kms-irsa"} {
		t.Run(name, func(t *testing.T) {
			docs := renderArgs(t, "-f", caseValues(name))
			var deployment map[string]any
			for _, d := range docs {
				switch d["kind"] {
				case "Certificate":
					t.Errorf("a Certificate is rendered: %v", d["metadata"])
				case "Deployment":
					deployment = d
				}
			}
			if deployment == nil {
				t.Fatal("no Deployment")
			}
			volumes, mounts := volumeNames(t, deployment)
			for _, n := range append(volumes, mounts...) {
				if strings.Contains(n, "signing") {
					t.Errorf("%q: a signing key is mounted", n)
				}
			}
			if name == "k8s-aws-kms-openbao" {
				// The old public key rides in a ConfigMap, never a Secret.
				var verify bool
				for _, d := range docs {
					if d["kind"] == "ConfigMap" {
						if n, _ := dig(d, "metadata", "name"); n == "sluis-verify-keys" {
							_, verify = dig(d, "data", "0.pem")
						}
					}
				}
				if !verify {
					t.Error("no ConfigMap sluis-verify-keys holding 0.pem")
				}
				for _, n := range []string{"verify-keys", "openbao-ca", "openbao-token", "secrets"} {
					if !contains(volumes, n) || !contains(mounts, n) {
						t.Errorf("volume and mount %q: %v / %v", n, volumes, mounts)
					}
				}
			}
		})
	}
}

// The default is unchanged: cert-manager's Certificate and its mount.
func TestWithoutKMSTheKeyIsStillCertManagers(t *testing.T) {
	docs := renderArgs(t, "-f", caseValues("minimal"))
	var certs int
	for _, d := range docs {
		if d["kind"] == "Certificate" {
			certs++
		}
		if d["kind"] == "Deployment" {
			if volumes, _ := volumeNames(t, d); !contains(volumes, "signing-key") {
				t.Errorf("no signing-key volume: %v", volumes)
			}
		}
	}
	if certs != 1 {
		t.Errorf("%d Certificates, want 1", certs)
	}
}

// IRSA annotates the account; Pod Identity adds nothing.
func TestAWSIdentity(t *testing.T) {
	annotation := func(values string) any {
		for _, d := range renderArgs(t, "-f", caseValues("minimal"), "-f", values) {
			if d["kind"] == "ServiceAccount" {
				if v, ok := dig(d, "metadata", "annotations", "eks.amazonaws.com/role-arn"); ok {
					return v
				}
			}
		}
		return nil
	}
	if got := annotation(caseValues("k8s-aws-kms-irsa")); got != "arn:example:iam::acct:role/sluis" {
		t.Errorf("irsa: role-arn %v", got)
	}
	if got := annotation(caseValues("k8s-aws-kms-openbao")); got != nil {
		t.Errorf("pod-identity: role-arn annotation %v", got)
	}
}

// The rotation-stalled threshold follows the rotation: 12h + 2h, not 350 days;
// the default stays 350 days; a value the operator sets is kept.
func TestTheRotationAlertFollowsTheRotationInterval(t *testing.T) {
	threshold := func(args ...string) string {
		for _, d := range renderArgs(t, args...) {
			for _, r := range rulesOf(t, d) {
				if r["alert"] == "AccessRosterSigningKeyRotationStalled" {
					expr := r["expr"].(string)
					return strings.TrimSpace(expr[strings.LastIndex(expr, ">")+1:])
				}
			}
		}
		t.Fatal("no AccessRosterSigningKeyRotationStalled rule")
		return ""
	}
	if got := threshold("-f", caseValues("alerts-kms-wrapped")); got != "50400" {
		t.Errorf("12h rotation: threshold %s, want 50400", got)
	}
	if got := threshold("-f", alertsCase); got != "30240000" {
		t.Errorf("cert-manager keys: threshold %s, want 30240000", got)
	}
	if got := threshold("-f", caseValues("alerts-kms-wrapped"), "--set", "alerts.rules.signingKeyRotationStalled.maxAgeSeconds=1000"); got != "1000" {
		t.Errorf("explicit: threshold %s, want 1000", got)
	}
	// The default rotation is 24h: 26h.
	noRotateEvery := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(noRotateEvery, []byte("renders: alerts\nconfig:\n  signingKey:\n    file: null\n"+
		"    kmsWrapped: {keyId: alias/k, stateSecret: s}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := threshold("-f", noRotateEvery); got != "93600" {
		t.Errorf("default rotation: threshold %s, want 93600 (26h)", got)
	}
}
