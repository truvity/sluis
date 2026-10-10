package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/emptypb"

	_ "github.com/truvity/sluis/gen/sluis/v1"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
)

// gates is a gate per module, each over a table of its own, and the tables.
func gates(t *testing.T) (maintenance.Set, map[port.Module]port.State) {
	t.Helper()
	tables := map[port.Module]port.State{}
	set := maintenance.Set{}
	for _, m := range []port.Module{port.ModuleOIDC, port.ModuleGoogle, port.ModuleGitHub, port.ModuleSlack, port.ModuleCloudflare} {
		tables[m] = memory.New()
		set[m] = maintenance.New(tables[m], maintenance.WithTTL(time.Nanosecond))
	}
	return set, tables
}

func underMaintenance(t *testing.T, st port.State) {
	t.Helper()
	if err := maintenance.Write(t.Context(), st, maintenance.Flag{State: maintenance.StateRestoring, By: "restore"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
}

func callRPC(t *testing.T, srv *httptest.Server, procedure string) error {
	t.Helper()
	c := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+procedure)
	_, err := c.CallUnary(t.Context(), connect.NewRequest(&emptypb.Empty{}))
	return err
}

// Each module's write RPCs are refused by that module's flag and by no other's,
// and its reads keep answering.
func TestEachModuleRefusesItsWritesUnderMaintenance(t *testing.T) {
	cases := []struct {
		module port.Module
		write  string
		read   string
	}{
		{port.ModuleGoogle, "/directoryroster.v1.WorkspaceService/SetServedDomains", "/directoryroster.v1.WorkspaceService/ListWorkspaces"},
		{port.ModuleGitHub, "/directoryroster.v1.GitHubService/DisconnectGitHubOrganisation", "/directoryroster.v1.GitHubService/GetGitHubStatus"},
		{port.ModuleSlack, "/directoryroster.v1.SlackChannelService/UpdateSlackChannel", "/directoryroster.v1.SlackChannelService/ListSlackChannels"},
		{port.ModuleCloudflare, "/directoryroster.v1.CloudflareService/RotateCloudflarePreset", "/directoryroster.v1.CloudflareService/ListCloudflare"},
	}
	for _, c := range cases {
		t.Run(string(c.module), func(t *testing.T) {
			set, tables := gates(t)
			mux := http.NewServeMux()
			registerRPC(mux, named{}, maintenanceOptions(set)...)
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			// Served: the write reaches the (unimplemented) handler.
			if got := connect.CodeOf(callRPC(t, srv, c.write)); got != connect.CodeUnimplemented {
				t.Fatalf("no flag: %s = %v, want it to reach the handler", c.write, got)
			}
			underMaintenance(t, tables[c.module])
			err := callRPC(t, srv, c.write)
			var ce *connect.Error
			if !errors.As(err, &ce) || ce.Code() != connect.CodeUnavailable || !strings.Contains(ce.Message(), "maintenance") {
				t.Fatalf("under maintenance: %s = %v, want unavailable naming maintenance", c.write, err)
			}
			if ce.Meta().Get("Retry-After") == "" {
				t.Error("the refusal carries no Retry-After")
			}
			if got := connect.CodeOf(callRPC(t, srv, c.read)); got != connect.CodeUnimplemented {
				t.Errorf("under maintenance the read %s = %v, want it served", c.read, got)
			}
			// The other modules' writes are not touched by this module's flag.
			for _, o := range cases {
				if o.module != c.module {
					if got := connect.CodeOf(callRPC(t, srv, o.write)); got != connect.CodeUnimplemented {
						t.Errorf("%s's flag refused %s's write %s: %v", c.module, o.module, o.write, got)
					}
				}
			}
		})
	}
}

// A method added to a service without a thought is a write, and a service added
// without a module is the issuer's: this holds every service the console serves
// to a module of its own, apart from the two that are the issuer's.
func TestEveryOperatorServiceIsMappedToAModule(t *testing.T) {
	issuers := map[string]bool{"SettingsService": true, "AccessService": true}
	seen := 0
	protoregistry.GlobalFiles.RangeFilesByPackage("directoryroster.v1", func(f protoreflect.FileDescriptor) bool {
		for i := range f.Services().Len() {
			svc := f.Services().Get(i)
			seen++
			proc := "/" + string(svc.FullName()) + "/X"
			if got := rpcModule(proc); got == port.ModuleOIDC && !issuers[string(svc.Name())] {
				t.Errorf("service %s falls to the issuer's module: map it in rpcModule", svc.FullName())
			}
		}
		return true
	})
	if seen < 9 {
		t.Fatalf("found %d operator services, want at least 9 (an empty sweep proves nothing)", seen)
	}
}

// The reads are the methods named like reads; nothing that writes is.
func TestOnlyReadsAreServedUnderMaintenance(t *testing.T) {
	for _, read := range []string{"GetSettings", "ListWorkspaces", "SearchPeople", "Explain", "WhoAmI"} {
		if !rpcIsRead("/directoryroster.v1.X/" + read) {
			t.Errorf("%s is a read", read)
		}
	}
	for _, write := range []string{"RequestGitHubPass", "RequestSlackPass", "Refresh", "UploadKey", "RotateCloudflarePreset", "DisconnectSlackWorkspace", "ConfirmGitHubRemovals"} {
		if rpcIsRead("/directoryroster.v1.X/" + write) {
			t.Errorf("%s writes and must be refused under maintenance", write)
		}
	}
}

// The pages that store a record on a GET (the return from GitHub, Slack, a
// directory's consent) are refused by the flag of the module they write; the
// shell, the assets, whoami and the audit read.
func TestConsolePagesThatWriteAreRefusedUnderMaintenance(t *testing.T) {
	set, tables := gates(t)
	s := &ConsoleServer{maintenance: set}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := s.withMaintenance(next)
	do := func(method, path string) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w.Code
	}

	underMaintenance(t, tables[port.ModuleGitHub])
	if got := do(http.MethodGet, "/connect/github/callback"); got != http.StatusServiceUnavailable {
		t.Errorf("the GitHub callback under github maintenance = %d, want 503", got)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/connect/slack/workspace/callback"}, {http.MethodGet, "/connect/google/callback"},
		{http.MethodGet, "/"}, {http.MethodGet, "/assets/app.js"}, {http.MethodGet, "/.access/whoami"},
		{http.MethodPost, "/directoryroster.v1.GitHubService/GetGitHubStatus"},
	} {
		if got := do(c.method, c.path); got != http.StatusNoContent {
			t.Errorf("%s %s with only github under maintenance = %d, want it served", c.method, c.path, got)
		}
	}
	underMaintenance(t, tables[port.ModuleOIDC])
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/logout"}, {http.MethodPost, "/login/recovery"}, {http.MethodGet, "/login/google/start"},
	} {
		if got := do(c.method, c.path); got != http.StatusServiceUnavailable {
			t.Errorf("%s %s under oidc maintenance = %d, want 503", c.method, c.path, got)
		}
	}
	if got := do(http.MethodGet, "/login"); got != http.StatusNoContent {
		t.Errorf("the sign-in chooser only reads: %d", got)
	}
}

// whoami says what is under maintenance, so the console shows the banner.
func TestWhoamiReportsTheModuleUnderMaintenance(t *testing.T) {
	set, tables := gates(t)
	s := &ConsoleServer{maintenance: set}
	if b := s.maintenanceBody(t.Context()); b != nil {
		t.Fatalf("nothing is under maintenance: %+v", b)
	}
	underMaintenance(t, tables[port.ModuleSlack])
	b := s.maintenanceBody(t.Context())
	if b == nil || b.Module != "slack" || b.State != maintenance.StateRestoring || b.Since == "" {
		t.Fatalf("body = %+v", b)
	}
}
