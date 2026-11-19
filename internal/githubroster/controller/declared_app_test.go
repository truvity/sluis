package controller_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/githubroster/controller"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/portstore/portstoretest"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state/memory"
)

func pemKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

// switches counts the audit records of an organisation moved to its declared App.
func (r *rig) switches() int {
	n := 0
	for _, rec := range r.audit.Records() {
		if rec.GetAction() == "roster.github_org.app_switched" {
			n++
		}
	}
	return n
}

// installationOfLastToken is the installation the last token was asked for.
func (r *rig) installationOfLastToken(t *testing.T) int64 {
	t.Helper()
	if len(r.github.TokenRequests) == 0 {
		t.Fatal("no installation token was asked for")
	}
	return r.github.TokenRequests[len(r.github.TokenRequests)-1].Installation
}

// An organisation connected earlier with an App of its own keeps acting with it
// while the App the installation declares for it is not installed, and the
// report says why. When the declared App is installed the next pass moves to it,
// mints with it, and records the move once, not on every pass after.
func TestAnAlreadyConnectedOrganisationMovesToItsDeclaredAppOnTheNextPassAfterTheInstall(t *testing.T) {
	portstoretest.Each(t, func(t *testing.T, e portstoretest.Env) {
		r := newRig(t)
		v5 := secretstore.FromStoreV5(memory.New(), "")
		plain := portstore.New(e.Open(t)).WithV5(v5)
		declared := portstore.New(e.Open(t)).WithV5(v5).DeclareGitHubApps(portstore.DeclaredGitHubApps{
			AppRef: func(org string) string {
				if org == "globex" {
					return "globex-declared"
				}
				return ""
			},
		})
		stored, apps := portstore.NewGitHubOrgs(plain), portstore.NewGitHubCatalogueApps(plain)
		ctx := context.Background()

		own := connection.Record{Org: "globex", AppID: 42, AppSlug: "globex-access-roster", InstallationID: 7, ConnectedAt: time.Now(), Owner: "ws1"}
		if err := stored.Put(ctx, own, connection.Credential{Org: "globex", AppID: 42, InstallationID: 7, PrivateKey: pemKey(t)}); err != nil {
			t.Fatal(err)
		}
		app := catalogueapp.Record{ID: "globex-declared", Org: "globex", AppID: 43, AppSlug: "globex-declared", ConnectedAt: time.Now()}
		if err := apps.Put(ctx, app, pemKey(t)); err != nil {
			t.Fatal(err)
		}

		c := controller.New(controller.Config{AppsDir: r.appsDir, Enabled: map[string]bool{"globex": true}}, controller.Deps{
			Log: slog.New(slog.DiscardHandler), GitHub: r.github.Client(),
			Access: r.console, Audit: r.audit, Status: r.report, Links: r.links, Bindings: bindings, Policy: testPolicy,
			Apps: portstore.NewGitHubOrgs(declared),
		})

		// Created, not installed: the organisation's own App acts, and the
		// report says what is missing.
		c.Pass(ctx)
		if got := r.installationOfLastToken(t); got != 7 {
			t.Errorf("the token was minted for installation %d, want the organisation's own App's (7)", got)
		}
		if note := r.report.org(t, "globex").AppNote; !strings.Contains(note, "globex-declared") || !strings.Contains(note, "not installed") {
			t.Errorf("app_note = %q, want that the declared App is not installed", note)
		}
		if n := r.switches(); n != 0 {
			t.Errorf("the organisation moved %d times before the declared App was installed", n)
		}

		// Installed: the next pass moves to it.
		app.InstallationID = 12
		if err := apps.Put(ctx, app, pemKey(t)); err != nil {
			t.Fatal(err)
		}
		c.Pass(ctx)
		if got := r.installationOfLastToken(t); got != 12 {
			t.Errorf("the token was minted for installation %d, want the declared App's (12)", got)
		}
		if note := r.report.org(t, "globex").AppNote; note != "" {
			t.Errorf("app_note = %q after the move, want none", note)
		}
		if n := r.switches(); n != 1 {
			t.Fatalf("the move was recorded %d times, want once: %v", n, r.audit.kinds())
		}
		var data map[string]string
		for _, rec := range r.audit.Records() {
			if rec.GetAction() == "roster.github_org.app_switched" {
				data = map[string]string{
					"from": rec.GetData().GetFields()["from"].GetStringValue(), "to": rec.GetData().GetFields()["to"].GetStringValue(),
				}
			}
		}
		if data["from"] != "globex-access-roster" || data["to"] != "globex-declared" {
			t.Errorf("the record says %v, want from the own App to the declared one", data)
		}
		if list, err := stored.List(ctx); err != nil || len(list) != 1 || list[0].AppRef != "globex-declared" || list[0].Owner != "ws1" {
			t.Errorf("the stored record = %+v, %v", list, err)
		}

		// Later passes record nothing more.
		c.Pass(ctx)
		c.Pass(ctx)
		if n := r.switches(); n != 1 {
			t.Errorf("after two more passes the move was recorded %d times, want still once", n)
		}
	})
}
