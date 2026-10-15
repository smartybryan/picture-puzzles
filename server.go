package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// The page is authored as an Artifact body (no doctype or <head>); the server
// wraps it into a full document so the same file works in both places.
//
//go:embed web/puzzle-table.html
var pageBody []byte

//go:embed web/favicon.svg
var faviconSVG []byte

//go:embed web/favicon.png
var faviconPNG []byte

//go:embed web/apple-touch-icon.png
var appleTouchIcon []byte

//go:embed web/icon-192.png
var icon192 []byte

//go:embed web/icon-maskable-512.png
var iconMaskable []byte

//go:embed web/manifest.webmanifest
var manifest []byte

const pageHead = `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
	`<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">` +
	`<link rel="icon" href="/favicon.svg" type="image/svg+xml">` +
	`<link rel="icon" href="/favicon.png" type="image/png" sizes="512x512">` +
	`<link rel="apple-touch-icon" href="/apple-touch-icon.png" sizes="180x180">` +
	`<link rel="manifest" href="/manifest.webmanifest">` +
	`<meta name="theme-color" content="#2D5852">` +
	`<meta name="apple-mobile-web-app-title" content="Puzzles"></head><body>`

const (
	maxPhotoBytes    = 20 << 20
	maxThumbBytes    = 2 << 20
	maxUploadBytes   = maxPhotoBytes + maxThumbBytes + 1<<20
	maxProgressBytes = 512 << 10
	maxPhotoName     = 120
)

var imageTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}

const contentSecurityPolicy = "default-src 'self'; img-src 'self' data: blob:; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; " +
	"script-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

type Server struct {
	store *Store
	auth  *Auth
	page  []byte
}

func NewServer(store *Store, auth *Auth) *Server {
	page := append([]byte(pageHead), pageBody...)
	page = append(page, "</body></html>"...)
	return &Server{store: store, auth: auth, page: page}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handlePage)
	mux.HandleFunc("GET /favicon.svg", serveStatic(faviconSVG, "image/svg+xml"))
	mux.HandleFunc("GET /favicon.png", serveStatic(faviconPNG, "image/png"))
	mux.HandleFunc("GET /apple-touch-icon.png", serveStatic(appleTouchIcon, "image/png"))
	mux.HandleFunc("GET /icon-192.png", serveStatic(icon192, "image/png"))
	mux.HandleFunc("GET /icon-maskable-512.png", serveStatic(iconMaskable, "image/png"))
	mux.HandleFunc("GET /manifest.webmanifest", serveStatic(manifest, "application/manifest+json"))
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("POST /api/login", s.mutating(s.handleLogin))
	mux.HandleFunc("POST /api/logout", s.mutating(s.handleLogout))
	mux.HandleFunc("GET /api/photos", s.signedIn(s.handleListPhotos))
	mux.HandleFunc("POST /api/photos", s.mutating(s.signedIn(s.handleUpload)))
	mux.HandleFunc("DELETE /api/photos/{id}", s.mutating(s.signedIn(s.handleRemove)))
	mux.HandleFunc("GET /photos/{id}", s.signedIn(s.handlePhotoFile))
	mux.HandleFunc("GET /api/progress", s.signedIn(s.handleProgress))
	mux.HandleFunc("PUT /api/progress/{id}", s.mutating(s.signedIn(s.handleSaveProgress)))
	return headers(mux)
}

type playerHandler func(http.ResponseWriter, *http.Request, Player)

func (s *Server) signedIn(h playerHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.auth.Player(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
			return
		}
		h(w, r, p)
	}
}

// mutating requires a custom header, which a cross-site form cannot send.
// Together with SameSite=Lax cookies this blocks cross-site request forgery.
func (s *Server) mutating(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Puzzle") != "1" {
			writeError(w, http.StatusForbidden, "forbidden", "Missing request header.")
			return
		}
		h(w, r)
	}
}

func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Puzzle-Server", "1")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	w.Write(s.page)
}

func serveStatic(body []byte, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(body)
	}
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p, ok := s.auth.Player(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.auth.Allow(r) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many tries. Wait a few minutes, then try again.")
		return
	}
	var req struct {
		Name     string `json:"name"`
		Passcode string `json:"passcode"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Enter your name and the family passcode.")
		return
	}
	if _, _, ok := normalizeName(req.Name); !ok {
		writeError(w, http.StatusBadRequest, "bad_name", "Enter a name of up to 40 characters.")
		return
	}
	if !s.auth.CheckPasscode(req.Passcode) {
		s.auth.Failed(r)
		time.Sleep(500 * time.Millisecond)
		writeError(w, http.StatusUnauthorized, "bad_passcode", "That passcode is not right.")
		return
	}
	p, err := s.auth.SignIn(w, r, req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_name", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.SignOut(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleListPhotos(w http.ResponseWriter, r *http.Request, _ Player) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string][]Photo{"photos": s.store.Photos()})
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request, p Player) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "That photo is over the 20 MB limit.")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "The upload could not be read.")
		return
	}
	defer r.MultipartForm.RemoveAll()

	full, fullType, err := readImage(r, "photo", maxPhotoBytes)
	if err != nil {
		writeUploadError(w, err)
		return
	}
	thumb, thumbType, err := readImage(r, "thumb", maxThumbBytes)
	if err != nil {
		writeUploadError(w, err)
		return
	}

	photo, err := s.store.AddPhoto(Photo{
		Name:      cleanPhotoName(r.FormValue("name")),
		By:        p.ID,
		ByName:    p.Name,
		At:        time.Now().UnixMilli(),
		Type:      fullType,
		ThumbType: thumbType,
	}, full, thumb)
	if err != nil {
		log.Printf("save photo: %v", err)
		writeError(w, http.StatusInternalServerError, "upstream_error", "The photo could not be saved.")
		return
	}
	writeJSON(w, http.StatusCreated, photo)
}

var (
	errTooLarge    = errors.New("too large")
	errNotAnImage  = errors.New("not an image")
	errMissingFile = errors.New("missing file")
)

// readImage reads one uploaded file and checks its bytes, not its declared type.
func readImage(r *http.Request, field string, limit int64) ([]byte, string, error) {
	f, hdr, err := r.FormFile(field)
	if err != nil {
		return nil, "", errMissingFile
	}
	defer f.Close()
	if hdr.Size > limit {
		return nil, "", errTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(b)) > limit {
		return nil, "", errTooLarge
	}
	ct := http.DetectContentType(b)
	if !imageTypes[ct] {
		return nil, "", errNotAnImage
	}
	return b, ct, nil
}

func writeUploadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "That photo is over the 20 MB limit.")
	case errors.Is(err, errNotAnImage):
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_type", "Photos must be JPG, PNG, WebP or GIF.")
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "The upload is missing the photo.")
	}
}

func cleanPhotoName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "Untitled photo"
	}
	if utf8.RuneCountInString(s) > maxPhotoName {
		s = string([]rune(s)[:maxPhotoName])
	}
	return s
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request, p Player) {
	switch err := s.store.RemovePhoto(r.PathValue("id"), p); {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "That photo is already gone.")
	case errors.Is(err, ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "Only the person who added a photo can remove it.")
	case err != nil:
		log.Printf("remove photo: %v", err)
		writeError(w, http.StatusInternalServerError, "upstream_error", "The photo could not be removed.")
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) handlePhotoFile(w http.ResponseWriter, r *http.Request, _ Player) {
	path, ct, ok := s.store.File(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}

func (s *Server) handleProgress(w http.ResponseWriter, r *http.Request, p Player) {
	progress, err := s.store.Progress(p)
	if err != nil {
		log.Printf("read progress: %v", err)
		writeError(w, http.StatusInternalServerError, "upstream_error", "Progress could not be loaded.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"progress": progress})
}

func (s *Server) handleSaveProgress(w http.ResponseWriter, r *http.Request, p Player) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxProgressBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "That saved puzzle is too large.")
		return
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Progress must be a JSON object.")
		return
	}
	switch err := s.store.SaveProgress(p, r.PathValue("id"), body); {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "That photo is not in the gallery.")
	case err != nil:
		log.Printf("save progress: %v", err)
		writeError(w, http.StatusInternalServerError, "upstream_error", "Progress could not be saved.")
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"code": code, "error": msg})
}
