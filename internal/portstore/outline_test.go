package portstore_test

import (
	"testing"
	"time"

	"github.com/truvity/sluis/internal/outlineroster/connection"
	"github.com/truvity/sluis/internal/portstore"
)

// An Outline workspace's record and API key are one item, its key under the
// outline module's secrets on layout v5; the confirmations and the pass request
// sit beside it and go with it.
func TestOutlineWorkspacesOnLayoutV5(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		b, l, _ := v5base(t, e)
		ws := portstore.NewOutlineWorkspaces(b)
		record := connection.Record{Workspace: "wiki", URL: "https://wiki.example.com", KeyUser: "key.owner@example.com", Team: "t-1",
			ConnectedAt: time.Unix(1700000000, 0).UTC()}
		if err := ws.Put(ctx, record, connection.Credential{Workspace: "wiki", APIKey: "ol_api_TOPSECRET"}); err != nil {
			t.Fatal(err)
		}
		wantPaths(t, "workspace Put", l.written(), "internal/outline/workspaces/wiki/<ref>")
		got, cred, found, err := ws.Get(ctx, "wiki")
		if err != nil || !found || cred.APIKey != "ol_api_TOPSECRET" || got.URL != record.URL {
			t.Fatalf("Get = %+v, %v, %v", got, found, err)
		}
		listed, err := ws.List(ctx)
		if err != nil || len(listed) != 1 || listed[0].Workspace != "wiki" {
			t.Fatalf("List = %+v, %v", listed, err)
		}
		now := time.Now().UTC()
		for _, scope := range []string{connection.ScopeAdmins, connection.GroupScope("all:wiki:acme")} {
			if err = ws.PutConfirmation(ctx, connection.Confirmation{Workspace: "wiki", Scope: scope, Fingerprint: "f00d", By: "op@example.com", At: now}); err != nil {
				t.Fatal(err)
			}
		}
		confirmations, err := ws.Confirmations(ctx)
		if err != nil || len(confirmations) != 2 {
			t.Fatalf("Confirmations = %+v, %v", confirmations, err)
		}
		if kept, _, err := ws.RequestPass(ctx, connection.PassRequest{Workspace: "wiki", By: "op@example.com", At: now}); err != nil || !kept {
			t.Fatalf("RequestPass = %v, %v", kept, err)
		}
		if kept, _, _ := ws.RequestPass(ctx, connection.PassRequest{Workspace: "wiki", By: "op@example.com", At: now.Add(time.Second)}); kept {
			t.Error("a second request within a minute was kept")
		}
		if requests, err := ws.PassRequests(ctx); err != nil || requests["wiki"].By != "op@example.com" {
			t.Fatalf("PassRequests = %+v, %v", requests, err)
		}
		l.reset()
		if err = ws.Delete(ctx, "wiki"); err != nil {
			t.Fatal(err)
		}
		wantPaths(t, "workspace Delete", l.removed(), "internal/outline/workspaces/wiki/<ref>")
		if _, _, found, _ := ws.Get(ctx, "wiki"); found {
			t.Error("a deleted workspace is still connected")
		}
		if confirmations, _ = ws.Confirmations(ctx); len(confirmations) != 0 {
			t.Errorf("the confirmations outlived the workspace: %+v", confirmations)
		}
	})
}
