package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/truvity/sluis/gen/directoryroster/v1/directoryrosterv1connect"
	"github.com/truvity/sluis/gen/sluis/v1/sluisv1connect"
	"github.com/truvity/sluis/internal/access"
)

// A laptop's access token opens the backup service and nothing else. It is the
// weaker credential, so it is not a way to every operator page.
func TestABearerFromTheIssuerOpensTheBackupServiceOnly(t *testing.T) {
	t.Parallel()
	server := &ConsoleServer{sessions: &access.Sessions{}, log: slog.New(slog.DiscardHandler)}
	asked := 0
	server.UseBearers(func(*http.Request) (access.Principal, bool) {
		asked++
		return access.Principal{Email: "ada@example.com", Subject: "ada@example.com", Source: access.SourceOIDC}, true
	})
	for path, want := range map[string]bool{
		"/" + sluisv1connect.BackupServiceName + "/RunBackup":                 true,
		"/" + directoryrosterv1connect.BackupServiceName + "/GetBackupStatus": true,
		"/" + sluisv1connect.AccessServiceName + "/ListHolders":               false,
		"/" + sluisv1connect.CloudflareServiceName + "/ListCloudflare":        false,
		"/" + sluisv1connect.BackupServiceName:                                false,
		"/.access/whoami":                                                     false,
		"/":                                                                   false,
	} {
		before := asked
		p, ok := server.principal(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil))
		if ok != want {
			t.Errorf("%s: principal = %v (%q), want %v", path, ok, p.Email, want)
		}
		if !want && asked != before {
			t.Errorf("%s: the bearer reader was consulted for a path it does not open", path)
		}
	}
}

func TestWithoutABearerReaderNoBearerIsRead(t *testing.T) {
	t.Parallel()
	server := &ConsoleServer{sessions: &access.Sessions{}, log: slog.New(slog.DiscardHandler)}
	request := httptest.NewRequest(http.MethodPost, "/"+sluisv1connect.BackupServiceName+"/RunBackup", nil)
	request.Header.Set("Authorization", "Bearer anything")
	if _, ok := server.principal(httptest.NewRecorder(), request); ok {
		t.Error("a bearer was taken with no reader")
	}
}
