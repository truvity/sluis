package sluispulumi_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	sluisconfig "github.com/truvity/sluis/config"
	arp "github.com/truvity/sluis/deploy/pulumi"
)

const storedBlobsRef = "external/cloudflare/blobs"

// storedBlobsInstallation is modules-full with an R2 preset for the blobs.
func storedBlobsInstallation(t *testing.T, mutate func(*sluisconfig.CloudflarePreset)) *sluisconfig.Installation {
	t.Helper()
	in := moduleInstallation(t, "modules-full")
	in.AWS.Bucket = ""
	p := sluisconfig.CloudflarePreset{
		Account: "main", Prototype: "9d3b1c7a5e2f4b8a9c0d1e2f3a4b5c6e", Description: "R2 read and write for the blobs",
		Lifetime: sluisconfig.Duration(time.Hour), Rotation: sluisconfig.Duration(20 * time.Minute),
		Endpoint: "https://acct.r2.example.test",
	}
	if mutate != nil {
		mutate(&p)
	}
	in.Cloudflare.Presets["blobs"] = p
	return in
}

func storedBlobs(ref, preset string) func(*arp.LambdaArgs) {
	return func(a *arp.LambdaArgs) {
		ext := r2()
		ext.CredentialsRef, ext.CredentialsPreset = ref, preset
		a.Storage = &arp.StorageGrant{External: ext}
	}
}

// With module functions the presets are the Cloudflare module's. Its stored
// credential is what the other functions read: each role that has the blobs
// reads that one parameter and nothing else of the module, and none holds the
// minter or what the minter writes.
func TestBlobsReadTheStoredPresetWithOneParameterGrantAndNoMinter(t *testing.T) {
	for _, withKey := range []bool{false, true} {
		in := storedBlobsInstallation(t, nil)
		rec, _ := mustLambda(t, moduleEstate(t, in, storedBlobs(storedBlobsRef, ""), func(a *arp.LambdaArgs) {
			if withKey {
				a.ParameterKeyArn = paramKey
			}
		}))
		credential := ssmBase + storedBlobsRef
		for role, st := range rolePolicies(t, rec) {
			creds := sidOf(st, "SluisBlobCredentials")
			if role == "cloudflare" {
				// It mints for its own blobs, and has the write of its stored credentials.
				if creds != nil {
					t.Errorf("cloudflare reads the stored credential it writes: %v", creds)
				}
			} else if creds == nil || !slices.Equal(strs(creds["Resource"]), []string{credential}) {
				t.Errorf("%s: the blob credential grant is %v, want exactly %s", role, creds, credential)
			}
			for _, a := range strs(creds["Action"]) {
				if strings.Contains(a, "Put") || strings.Contains(a, "Delete") {
					t.Errorf("%s may %s the blob credential", role, a)
				}
			}
			for _, sid := range []string{"SluisCrossCloudflareMinter", "SluisCrossCloudflareMinted"} {
				if sidOf(st, sid) != nil {
					t.Errorf("%s holds %s", role, sid)
				}
			}
			if role == "cloudflare" {
				continue
			}
			for _, r := range resourcesOf(st) {
				if strings.Contains(r, "internal/cloudflare") {
					t.Errorf("%s reaches the minter's namespace: %s", role, r)
				}
				if strings.Contains(r, "external/cloudflare") && r != credential {
					t.Errorf("%s reaches more of external/cloudflare than the blob credential: %s", role, r)
				}
			}
			if withKey && role != "issuer" {
				if got := keyedParameters(withResource(st, paramKey)); !slices.Contains(got, credential) {
					t.Errorf("%s: the parameter key is not granted for the credential: %v", role, got)
				}
			}
		}
		noRoleRepeatsASid(t, "stored preset blobs", rec)

		// The documents: every function reads the address, but the Cloudflare
		// module's, which mints for its own blobs.
		for _, m := range sluisconfig.Modules() {
			doc := archiveFiles(t, rec.one(t, layerType, "staging-"+m+"-config"))["sluis/sluis.yaml"]
			if m == sluisconfig.ModuleCloudflare {
				if !strings.Contains(doc, "preset: blobs") || strings.Contains(doc, "credentialsRef") {
					t.Errorf("cloudflare: the blobs do not mint in process:\n%s", doc)
				}
				continue
			}
			if !strings.Contains(doc, "credentialsRef: "+storedBlobsRef) || strings.Contains(doc, "preset: blobs") {
				t.Errorf("%s: the blobs do not read the stored credential:\n%s", m, doc)
			}
		}
	}
}

// CredentialsPreset mints in the function's process, and with module functions
// only the Cloudflare module has a minter: the library refuses it there and
// says what to write instead. The stored form must name an R2 preset that
// keeps store: external, at the blobs' endpoint.
func TestBlobPresetsAreRefusedWithModuleFunctionsAndTheStoredFormIsChecked(t *testing.T) {
	cases := map[string]struct {
		mutate   func(*sluisconfig.CloudflarePreset)
		ref, pre string
		want     string // empty: accepted
	}{
		"preset with module functions": {pre: "blobs", want: "external/cloudflare/blobs"},
		"stored ref":                   {ref: storedBlobsRef},
		"an undeclared preset":         {ref: "external/cloudflare/missing", want: "not in Installation.Cloudflare.Presets"},
		"a preset that is not R2": {ref: storedBlobsRef, want: "not an R2 preset",
			mutate: func(p *sluisconfig.CloudflarePreset) { p.Endpoint = "" }},
		"a preset with store none": {ref: storedBlobsRef, want: "store: none",
			mutate: func(p *sluisconfig.CloudflarePreset) { p.Store, p.Rotation = "none", 0 }},
		"another endpoint": {ref: storedBlobsRef, want: "is for the endpoint",
			mutate: func(p *sluisconfig.CloudflarePreset) { p.Endpoint = "https://other.r2.example.test" }},
		"an address that is no preset": {ref: "external/oidc/blobs", want: "does not match pattern"},
	}
	for name, c := range cases {
		in := storedBlobsInstallation(t, c.mutate)
		_, _, err := buildLambda(t, moduleEstate(t, in, storedBlobs(c.ref, c.pre)))
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want an error naming %q", name, err, c.want)
		}
	}
}

// Under a customer key, every role that reads the stored blob credential is
// admitted by the key policy to decrypt exactly that parameter, so an estate
// adds no statement of its own: the Cloudflare module writes it under the key,
// and a reader the key does not admit cannot open its blobs. The Cloudflare
// module (which mints for its own blobs and owns the namespace) and the backup
// and restore roles (which read every parameter) need no row.
func TestTheKeyPolicyAdmitsTheBlobCredentialsReaders(t *testing.T) {
	a := moduleEstate(t, storedBlobsInstallation(t, nil), storedBlobs(storedBlobsRef, "")).args(t)
	viaArgs, err := a.KeyPolicyStatements(arp.KeyPolicyOptions{KeyAdmins: []string{kpEstateKeyAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	ra := a.RoleArgs()
	ra.Instance = "staging" // the installation names it; the args do not
	viaRoles, err := ra.KeyPolicyStatements(arp.KeyPolicyOptions{KeyAdmins: []string{kpEstateKeyAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	credential := ssmBase + storedBlobsRef
	for name, ks := range map[string][]map[string]any{"LambdaArgs": viaArgs, "RoleArgs": viaRoles} {
		got := map[string]map[string]any{}
		for _, s := range ks {
			if sid, _ := s["Sid"].(string); strings.HasPrefix(sid, "SluisKeyBlobCredentials") {
				got[strings.TrimPrefix(sid, "SluisKeyBlobCredentials")] = s
			}
		}
		for _, role := range []string{"Issuer", "Github", "Google", "Slack"} {
			s := got[role]
			if s == nil {
				t.Errorf("%s: no SluisKeyBlobCredentials%s: the reader cannot decrypt the blob credential", name, role)
				continue
			}
			raw, _ := json.Marshal(s)
			if !strings.Contains(string(raw), `"kms:EncryptionContext:PARAMETER_ARN":["`+credential+`"]`) ||
				!strings.Contains(string(raw), `"Action":["kms:Decrypt","kms:DescribeKey"]`) ||
				!strings.Contains(string(raw), `"kms:ViaService":"ssm.`) {
				t.Errorf("%s: SluisKeyBlobCredentials%s = %s, want Decrypt and DescribeKey of %s through SSM", name, role, raw, credential)
			}
			if p := principals(s); len(p) != 1 || !strings.Contains(p[0], ":role/sluis-staging") {
				t.Errorf("%s: SluisKeyBlobCredentials%s admits %v", name, role, p)
			}
			delete(got, role)
		}
		for role := range got {
			t.Errorf("%s: SluisKeyBlobCredentials%s: a role whose own statements cover the credential got a row", name, role)
		}
	}

	// A bucket, or no blobs credential, adds no row.
	plain, err := moduleEstate(t, moduleInstallation(t, "modules-full")).args(t).KeyPolicyStatements(arp.KeyPolicyOptions{KeyAdmins: []string{kpEstateKeyAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plain {
		if sid, _ := s["Sid"].(string); strings.HasPrefix(sid, "SluisKeyBlobCredentials") {
			t.Errorf("a bucket estate has %s", sid)
		}
	}
}
