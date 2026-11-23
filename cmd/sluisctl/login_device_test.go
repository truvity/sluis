package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A device with no browser: SSH anywhere, and a Unix host with no display.
func TestThereIsNoBrowserOverSSHOrWithoutADisplay(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(name string) string { return vars[name] }
	}
	for _, tc := range []struct {
		name string
		goos string
		vars map[string]string
		want bool
	}{
		{"linux with X11", "linux", map[string]string{"DISPLAY": ":0"}, false},
		{"linux with Wayland", "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, false},
		{"linux with no display", "linux", nil, true},
		{"freebsd with no display", "freebsd", nil, true},
		{"linux over SSH with X forwarding", "linux", map[string]string{"DISPLAY": "localhost:10.0", "SSH_CONNECTION": "192.0.2.1 22 192.0.2.2 50000"}, true},
		{"macOS", "darwin", nil, false},
		{"macOS over SSH", "darwin", map[string]string{"SSH_TTY": "/dev/ttys001"}, true},
		{"windows", "windows", nil, false},
		{"windows over SSH", "windows", map[string]string{"SSH_CLIENT": "192.0.2.1 50000 22"}, true},
	} {
		if got := noBrowser(env(tc.vars), tc.goos); got != tc.want {
			t.Errorf("%s: noBrowser = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// fakeDeviceIssuer answers the two device endpoints from a script of poll
// answers, and records what it was sent.
type fakeDeviceIssuer struct {
	mu      sync.Mutex
	answers []string // one per poll: an OAuth error code, or "ok"
	polls   int
	forms   []map[string]string
	agent   string
	paths   []string
}

func (f *fakeDeviceIssuer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = r.ParseForm()
	form := map[string]string{}
	for k := range r.PostForm {
		form[k] = r.PostForm.Get(k)
	}
	f.forms = append(f.forms, form)
	f.paths = append(f.paths, r.URL.Path)
	f.agent = r.UserAgent()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/device_authorization":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "dc-1", "user_code": "BCDF-GHJK",
			"verification_uri":          "https://issuer.example/device",
			"verification_uri_complete": "https://issuer.example/device?user_code=BCDF-GHJK",
			"expires_in":                600, "interval": 5,
		})
	case "/token":
		answer := "ok"
		if f.polls < len(f.answers) {
			answer = f.answers[f.polls]
		}
		f.polls++
		switch answer {
		case "ok":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at", "refresh_token": "rt", "expires_in": 300,
				"id_token": "e30." + "eyJzdWIiOiJhZGFAbm9ydGguZXhhbXBsZSIsImVtYWlsIjoiYWRhQG5vcnRoLmV4YW1wbGUifQ" + ".sig",
			})
		case "503":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": answer})
		}
	default:
		http.NotFound(w, r)
	}
}

// waits records every wait the poll loop asks for, and waits for none.
type waits struct {
	mu  sync.Mutex
	got []time.Duration
}

func (w *waits) sleep(ctx context.Context, d time.Duration) error {
	w.mu.Lock()
	w.got = append(w.got, d)
	w.mu.Unlock()
	return ctx.Err()
}

func runDeviceLogin(t *testing.T, f *fakeDeviceIssuer) (Session, string, []time.Duration, error) {
	t.Helper()
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	var out bytes.Buffer
	w := &waits{}
	session, err := deviceLogin(t.Context(), Config{Issuer: server.URL, ClientID: "sluisctl"}, &out, w.sleep)
	return session, out.String(), w.got, err
}

// The loop prints the page, the code and the complete page, waits the
// interval between polls, adds five seconds at each slow_down, rides out a
// 503, and caches the session the token response describes.
func TestTheDeviceLoginPollsUntilApproved(t *testing.T) {
	f := &fakeDeviceIssuer{answers: []string{"authorization_pending", "slow_down", "503", "authorization_pending", "ok"}}
	session, printed, waited, err := runDeviceLogin(t, f)
	if err != nil {
		t.Fatalf("deviceLogin: %v", err)
	}
	for _, want := range []string{"https://issuer.example/device\n", "BCDF-GHJK", "https://issuer.example/device?user_code=BCDF-GHJK"} {
		if !strings.Contains(printed, want) {
			t.Errorf("the output does not show %q:\n%s", want, printed)
		}
	}
	wantWaits := []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second, 10 * time.Second, 10 * time.Second}
	if len(waited) != len(wantWaits) {
		t.Fatalf("waits = %v, want %v", waited, wantWaits)
	}
	for i := range wantWaits {
		if waited[i] != wantWaits[i] {
			t.Errorf("wait %d = %v, want %v (all %v)", i, waited[i], wantWaits[i], waited)
		}
	}
	if session.RefreshToken != "rt" || session.AccessToken != "at" || session.Email != "ada@north.example" {
		t.Errorf("session = %+v", session)
	}
	if f.forms[0]["client_id"] != "sluisctl" || f.forms[0]["scope"] != loginScope {
		t.Errorf("device_authorization form = %v", f.forms[0])
	}
	poll := f.forms[1]
	if poll["grant_type"] != deviceGrantType || poll["device_code"] != "dc-1" || poll["client_id"] != "sluisctl" {
		t.Errorf("poll form = %v", poll)
	}
	if !strings.HasPrefix(f.agent, "sluisctl/") {
		t.Errorf("User-Agent = %q, want sluisctl's: the approval page shows it", f.agent)
	}
}

// Denied, expired, and anything else the issuer refuses with, end the loop
// with words for the person.
func TestTheDeviceLoginStopsOnARefusal(t *testing.T) {
	for answer, want := range map[string]string{
		"access_denied":  "denied",
		"expired_token":  "expired",
		"invalid_grant":  "invalid_grant",
		"invalid_client": "invalid_client",
	} {
		_, _, _, err := runDeviceLogin(t, &fakeDeviceIssuer{answers: []string{"authorization_pending", answer}})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to say %q", answer, err, want)
		}
	}
}

// The loop stops at expires_in even when the issuer never says so.
func TestTheDeviceLoginStopsAtTheCodesLifetime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/device_authorization" {
			_, _ = w.Write([]byte(`{"device_code":"d","user_code":"BCDF-GHJK","verification_uri":"https://issuer.example/device","expires_in":1,"interval":1}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	}))
	t.Cleanup(server.Close)
	_, err := deviceLogin(t.Context(), Config{Issuer: server.URL, ClientID: "sluisctl"}, &bytes.Buffer{}, sleepContext)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("err = %v, want the code's lifetime to end the loop", err)
	}
}

// An issuer that does not serve the grant says what to do instead.
func TestTheDeviceLoginNamesTheClientFlagWhenNotServed(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	_, err := deviceLogin(t.Context(), Config{Issuer: server.URL, ClientID: "sluisctl"}, &bytes.Buffer{}, sleepContext)
	if !errors.Is(err, errDeviceNotServed) || !strings.Contains(err.Error(), "device_authorization: true") {
		t.Errorf("err = %v", err)
	}
}

// `sluisctl login --device` caches the session exactly where a loopback
// sign-in does.
func TestLoginWithDeviceCachesTheSession(t *testing.T) {
	choiceEnv(t)
	f := &fakeDeviceIssuer{answers: []string{"ok"}}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	out, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatalf("create stdout: %v", err)
	}
	saved := stdout
	stdout = out
	t.Cleanup(func() { stdout = saved })

	if err = login([]string{"--device", "--issuer", server.URL, "--client", "sluisctl"}); err != nil {
		t.Fatalf("login --device: %v", err)
	}
	session, err := loadSession(server.URL)
	if err != nil {
		t.Fatalf("load the session: %v", err)
	}
	if session.RefreshToken != "rt" || session.Email != "ada@north.example" {
		t.Errorf("cached session = %+v", session)
	}
}

// stubLogin says whether this machine has a browser, stands in for the
// loopback sign-in, and silences stdout, for the tests of which way `login`
// goes.
func stubLogin(t *testing.T, noBrowser bool) *int {
	t.Helper()
	choiceEnv(t)
	savedDetect, savedBrowser, savedOut := withoutBrowser, browserLogin, stdout
	out, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatalf("create stdout: %v", err)
	}
	stdout = out
	calls := 0
	withoutBrowser = func() bool { return noBrowser }
	browserLogin = func(Config) (Session, error) {
		calls++
		return Session{RefreshToken: "loopback"}, nil
	}
	t.Cleanup(func() { withoutBrowser, browserLogin, stdout = savedDetect, savedBrowser, savedOut })
	return &calls
}

// `--device=false` is the person's choice too: the browser sign-in, even
// where none could be opened, and the issuer is never asked for a code.
func TestAnExplicitDeviceFalseUsesTheBrowser(t *testing.T) {
	calls := stubLogin(t, true)
	f := &fakeDeviceIssuer{}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)

	if err := login([]string{"--device=false", "--issuer", server.URL, "--client", "sluisctl"}); err != nil {
		t.Fatalf("login --device=false: %v", err)
	}
	if *calls != 1 {
		t.Errorf("browser sign-ins = %d, want 1", *calls)
	}
	for i, path := range f.paths {
		if path == "/device_authorization" || f.forms[i]["grant_type"] == deviceGrantType {
			t.Errorf("the issuer was asked for the device flow: %v %v", f.paths, f.forms)
		}
	}
}

// Picked because there is no browser, the device sign-in falls back to the
// browser one when the issuer does not serve the grant.
func TestNoBrowserFallsBackToTheBrowserWhenTheGrantIsNotServed(t *testing.T) {
	calls := stubLogin(t, true)
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)

	if err := login([]string{"--issuer", server.URL, "--client", "sluisctl"}); err != nil {
		t.Fatalf("login: %v", err)
	}
	if *calls != 1 {
		t.Errorf("browser sign-ins = %d, want the fallback", *calls)
	}
	session, err := loadSession(server.URL)
	if err != nil || session.RefreshToken != "loopback" {
		t.Errorf("session = %+v, %v", session, err)
	}
}

// Asked for by name, it does not fall back: the person said device.
func TestAnExplicitDeviceDoesNotFallBack(t *testing.T) {
	calls := stubLogin(t, false)
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)

	err := login([]string{"--device", "--issuer", server.URL, "--client", "sluisctl"})
	if !errors.Is(err, errDeviceNotServed) || *calls != 0 {
		t.Errorf("err = %v, browser sign-ins = %d; want the refusal and no fallback", err, *calls)
	}
}

// With a browser and no flag, the loopback sign-in it always was.
func TestABrowserMachineUsesTheBrowser(t *testing.T) {
	calls := stubLogin(t, false)
	f := &fakeDeviceIssuer{}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)

	if err := login([]string{"--issuer", server.URL, "--client", "sluisctl"}); err != nil {
		t.Fatalf("login: %v", err)
	}
	if *calls != 1 {
		t.Errorf("browser sign-ins = %d, want 1", *calls)
	}
}
