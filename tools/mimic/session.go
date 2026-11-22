package mimic

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// The cookies mimic sets on its own host.
const (
	SessionCookie = "mimic_session"
	loginCookie   = "mimic_login"
)

// session is what the session cookie carries: who signed in at the stable sluis, how, and until when.
type session struct {
	Sub    string   `json:"sub"`
	Groups []string `json:"groups"`
	Via    string   `json:"via"` // "person" or "ci"
	Auth   int64    `json:"auth"`
	Exp    int64    `json:"exp"`
}

// login is what the sign-in cookie carries between /login and /callback.
type login struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	Next     string `json:"next"`
	Exp      int64  `json:"exp"`
}

// seal writes v as <payload>.<mac>, the MAC bound to the cookie's name so one cookie cannot stand for another.
func (k *keys) seal(name string, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	p := base64.RawURLEncoding.EncodeToString(b)
	return p + "." + base64.RawURLEncoding.EncodeToString(mac(k.session, name, p)), nil
}

// open checks the MAC of a sealed value and reads it.
func (k *keys) open(name, s string, v any) error {
	p, m, ok := strings.Cut(s, ".")
	if !ok {
		return errors.New("not a sealed value")
	}
	got, err := base64.RawURLEncoding.DecodeString(m)
	if err != nil || !hmac.Equal(got, mac(k.session, name, p)) {
		return errors.New("the MAC does not verify")
	}
	b, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return errors.New("not a sealed value")
	}
	return json.Unmarshal(b, v)
}

func (s *Server) setCookie(w http.ResponseWriter, name, value string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure whenever the public URL is https; only a loopback test runs over http
		Name: name, Value: value, Path: "/", Expires: exp, MaxAge: int(exp.Sub(s.now()).Seconds()),
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode}) //nolint:gosec // as setCookie
}

// startSession sets the session cookie for a subject of the stable sluis.
func (s *Server) startSession(w http.ResponseWriter, sub *subject, via string) error {
	now := s.now()
	exp := now.Add(s.cfg.SessionTTL)
	v, err := s.keys.seal(SessionCookie, session{Sub: sub.Sub, Groups: sub.Groups, Via: via, Auth: now.Unix(), Exp: exp.Unix()})
	if err != nil {
		return err
	}
	s.setCookie(w, SessionCookie, v, exp)
	return nil
}

// sessionOf reads the session of a request; nil when there is none, or it does not verify, or it has expired.
func (s *Server) sessionOf(r *http.Request) *session {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil
	}
	var v session
	if s.keys.open(SessionCookie, c.Value, &v) != nil || v.Sub == "" || s.now().Unix() >= v.Exp {
		return nil
	}
	return &v
}
