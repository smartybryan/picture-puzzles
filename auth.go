package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	sessionCookie = "pt_session"
	sessionMaxAge = 365 * 24 * time.Hour
	maxNameRunes  = 40

	loginWindow      = 15 * time.Minute
	loginMaxFailures = 10
)

// Player is a signed-in family member. ID is the normalized name, so the same
// person signing in on another device gets the same progress.
type Player struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
}

// Auth checks the family passcode and issues signed session cookies.
type Auth struct {
	passHash [32]byte
	key      []byte
	admins   map[string]bool

	mu       sync.Mutex
	failures map[string][]time.Time
}

// NewAuth loads or creates the signing secret in dataDir. The cookie key is
// derived from the secret and the passcode, so changing the passcode signs
// everyone out.
func NewAuth(dataDir, passcode string, admins []string) (*Auth, error) {
	secret, err := loadSecret(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("session:" + passcode))

	a := &Auth{
		passHash: sha256.Sum256([]byte(passcode)),
		key:      mac.Sum(nil),
		admins:   map[string]bool{},
		failures: map[string][]time.Time{},
	}
	for _, n := range admins {
		if id, _, ok := normalizeName(n); ok {
			a.admins[id] = true
		}
	}
	return a, nil
}

func loadSecret(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil && len(b) >= 32 {
		return b, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	b = make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, b, 0o600); err != nil {
		return nil, err
	}
	return b, nil
}

// normalizeName trims and collapses spaces. The ID is the lower-cased form.
func normalizeName(s string) (id, display string, ok bool) {
	display = strings.Join(strings.Fields(s), " ")
	if display == "" || utf8.RuneCountInString(display) > maxNameRunes {
		return "", "", false
	}
	for _, r := range display {
		if unicode.IsControl(r) {
			return "", "", false
		}
	}
	return strings.ToLower(display), display, true
}

func (a *Auth) CheckPasscode(p string) bool {
	h := sha256.Sum256([]byte(p))
	return subtle.ConstantTimeCompare(h[:], a.passHash[:]) == 1
}

// Allow reports whether this client may try another sign-in.
func (a *Auth) Allow(r *http.Request) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	ip := clientIP(r)
	a.failures[ip] = recent(a.failures[ip])
	return len(a.failures[ip]) < loginMaxFailures
}

func (a *Auth) Failed(r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ip := clientIP(r)
	a.failures[ip] = append(recent(a.failures[ip]), time.Now())
}

func recent(ts []time.Time) []time.Time {
	cut := time.Now().Add(-loginWindow)
	out := ts[:0]
	for _, t := range ts {
		if t.After(cut) {
			out = append(out, t)
		}
	}
	return out
}

// clientIP is the visitor's address. Behind a reverse proxy on the same
// machine every request arrives from loopback, so the proxy's
// X-Forwarded-For is used instead; its last entry is the one the proxy added.
// The header is ignored from anyone else, who could set it to anything.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			parts := strings.Split(fwd, ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
				return last
			}
		}
	}
	return host
}

type sessionClaims struct {
	Name   string `json:"n"`
	Issued int64  `json:"t"`
}

// SignIn sets the session cookie for name and returns the player.
func (a *Auth) SignIn(w http.ResponseWriter, r *http.Request, name string) (Player, error) {
	id, display, ok := normalizeName(name)
	if !ok {
		return Player{}, fmt.Errorf("name must be 1 to %d characters", maxNameRunes)
	}
	payload, err := json.Marshal(sessionClaims{Name: display, Issued: time.Now().Unix()})
	if err != nil {
		return Player{}, err
	}
	value := base64.RawURLEncoding.EncodeToString(payload) + "." + a.sign(payload)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   int(sessionMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
	return Player{ID: id, Name: display, Admin: a.admins[id]}, nil
}

func (a *Auth) SignOut(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
}

// Player returns the signed-in player for r, if the session cookie is valid.
func (a *Auth) Player(r *http.Request) (Player, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return Player{}, false
	}
	enc, sig, found := strings.Cut(c.Value, ".")
	if !found {
		return Player{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || !hmac.Equal([]byte(sig), []byte(a.sign(payload))) {
		return Player{}, false
	}
	var claims sessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Player{}, false
	}
	if time.Since(time.Unix(claims.Issued, 0)) > sessionMaxAge {
		return Player{}, false
	}
	id, display, ok := normalizeName(claims.Name)
	if !ok {
		return Player{}, false
	}
	return Player{ID: id, Name: display, Admin: a.admins[id]}, true
}

func (a *Auth) sign(payload []byte) string {
	mac := hmac.New(sha256.New, a.key)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// playerKey is a filesystem-safe directory name for a player's progress.
func playerKey(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:16])
}
