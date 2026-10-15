package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
)

const testPasscode = "blue-canoe"

func newTestServer(t *testing.T, admins ...string) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth(dir, testPasscode, admins)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(NewServer(store, auth).Routes())
	t.Cleanup(ts.Close)
	return ts
}

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func newClient(t *testing.T, ts *httptest.Server) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: ts.URL, http: &http.Client{Jar: jar}}
}

func (c *client) do(method, path string, body io.Reader, contentType string) (*http.Response, []byte) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("X-Puzzle", "1")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

func (c *client) json(method, path string, v any) (*http.Response, []byte) {
	b, _ := json.Marshal(v)
	return c.do(method, path, bytes.NewReader(b), "application/json")
}

func (c *client) login(name string) {
	c.t.Helper()
	res, body := c.json("POST", "/api/login", map[string]string{"name": name, "passcode": testPasscode})
	if res.StatusCode != http.StatusOK {
		c.t.Fatalf("login %s: %d %s", name, res.StatusCode, body)
	}
}

func (c *client) upload(name string, photo []byte) (*http.Response, Photo) {
	c.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("name", name)
	fw, _ := mw.CreateFormFile("photo", "photo")
	fw.Write(photo)
	tw, _ := mw.CreateFormFile("thumb", "thumb.jpg")
	tw.Write(pngBytes(4))
	mw.Close()
	res, body := c.do("POST", "/api/photos", &buf, mw.FormDataContentType())
	var p Photo
	json.Unmarshal(body, &p)
	return res, p
}

func pngBytes(size int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	img.Set(0, 0, color.RGBA{200, 100, 50, 255})
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func TestPageIsAFullDocument(t *testing.T) {
	ts := newTestServer(t)
	res, body := newClient(t, ts).do("GET", "/", nil, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	if !strings.HasPrefix(string(body), "<!doctype html>") || !strings.Contains(string(body), "<title>Puzzle Table</title>") {
		t.Fatalf("page is not a wrapped document: %.120s", body)
	}
	if res.Header.Get("X-Puzzle-Server") != "1" {
		t.Fatal("missing X-Puzzle-Server header")
	}
}

func TestFavicons(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t, ts)
	for path, want := range map[string]string{"/favicon.svg": "image/svg+xml", "/favicon.png": "image/png", "/apple-touch-icon.png": "image/png",
		"/icon-192.png": "image/png", "/icon-maskable-512.png": "image/png", "/manifest.webmanifest": "application/manifest+json"} {
		res, body := c.do("GET", path, nil, "")
		if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != want || len(body) == 0 {
			t.Errorf("%s: %d %s", path, res.StatusCode, res.Header.Get("Content-Type"))
		}
	}
	_, page := c.do("GET", "/", nil, "")
	for _, link := range []string{`href="/favicon.svg"`, `href="/apple-touch-icon.png"`, `href="/manifest.webmanifest"`} {
		if !strings.Contains(string(page), link) {
			t.Errorf("page does not link %s", link)
		}
	}
}

// Every icon the manifest lists must be served.
func TestManifestIconsExist(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t, ts)
	var m struct {
		Name     string `json:"name"`
		StartURL string `json:"start_url"`
		Icons    []struct{ Src, Type string }
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if m.Name == "" || m.StartURL != "/" || len(m.Icons) == 0 {
		t.Fatalf("manifest is missing name, start_url or icons: %+v", m)
	}
	for _, icon := range m.Icons {
		res, _ := c.do("GET", icon.Src, nil, "")
		if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != icon.Type {
			t.Errorf("%s: %d %s, want %s", icon.Src, res.StatusCode, res.Header.Get("Content-Type"), icon.Type)
		}
	}
}

func TestLogin(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t, ts)

	if res, _ := c.do("GET", "/api/me", nil, ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me before login: %d", res.StatusCode)
	}
	if res, _ := c.json("POST", "/api/login", map[string]string{"name": "Ann", "passcode": "nope"}); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong passcode: %d", res.StatusCode)
	}

	c.login("  Ann   Lee ")
	res, body := c.do("GET", "/api/me", nil, "")
	var me Player
	json.Unmarshal(body, &me)
	if res.StatusCode != http.StatusOK || me.Name != "Ann Lee" || me.ID != "ann lee" {
		t.Fatalf("me: %d %+v", res.StatusCode, me)
	}

	c.do("POST", "/api/logout", nil, "")
	if res, _ := c.do("GET", "/api/me", nil, ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me after logout: %d", res.StatusCode)
	}
}

func TestMutationsNeedTheCustomHeader(t *testing.T) {
	ts := newTestServer(t)
	res, err := http.Post(ts.URL+"/api/login", "application/json", strings.NewReader(`{"name":"Ann","passcode":"blue-canoe"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", res.StatusCode)
	}
}

func TestGalleryNeedsSignIn(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t, ts)
	if res, _ := c.do("GET", "/api/photos", nil, ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("photos: %d", res.StatusCode)
	}
	if res, _ := c.upload("x.png", pngBytes(8)); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("upload: %d", res.StatusCode)
	}
}

func TestUploadListServeAndRemove(t *testing.T) {
	ts := newTestServer(t)
	ann, bob := newClient(t, ts), newClient(t, ts)
	ann.login("Ann")
	bob.login("Bob")

	res, photo := ann.upload("Beach.png", pngBytes(16))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d", res.StatusCode)
	}
	if photo.By != "ann" || photo.ByName != "Ann" || photo.Name != "Beach.png" || photo.Type != "image/png" {
		t.Fatalf("photo: %+v", photo)
	}

	_, body := bob.do("GET", "/api/photos", nil, "")
	var list struct{ Photos []Photo }
	json.Unmarshal(body, &list)
	if len(list.Photos) != 1 || list.Photos[0].ID != photo.ID {
		t.Fatalf("bob sees %+v", list.Photos)
	}

	res, img := bob.do("GET", "/photos/"+photo.ID, nil, "")
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png" || !bytes.Equal(img, pngBytes(16)) {
		t.Fatalf("serve photo: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if res, _ := bob.do("GET", "/photos/"+photo.Thumb, nil, ""); res.StatusCode != http.StatusOK {
		t.Fatalf("serve thumb: %d", res.StatusCode)
	}

	if res, _ := bob.do("DELETE", "/api/photos/"+photo.ID, nil, ""); res.StatusCode != http.StatusForbidden {
		t.Fatalf("bob removes ann's photo: %d", res.StatusCode)
	}
	if res, _ := ann.do("DELETE", "/api/photos/"+photo.ID, nil, ""); res.StatusCode != http.StatusOK {
		t.Fatalf("ann removes her photo: %d", res.StatusCode)
	}
	if res, _ := ann.do("GET", "/photos/"+photo.ID, nil, ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("removed photo still served: %d", res.StatusCode)
	}
}

func TestAdminCanRemoveAnyPhoto(t *testing.T) {
	ts := newTestServer(t, "Mom")
	ann, mom := newClient(t, ts), newClient(t, ts)
	ann.login("Ann")
	mom.login("mom")
	_, photo := ann.upload("Dog.png", pngBytes(8))
	if res, _ := mom.do("DELETE", "/api/photos/"+photo.ID, nil, ""); res.StatusCode != http.StatusOK {
		t.Fatalf("admin remove: %d", res.StatusCode)
	}
}

func TestUploadRejectsNonImages(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t, ts)
	c.login("Ann")
	if res, _ := c.upload("evil.html", []byte("<html><script>alert(1)</script></html>")); res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status %d, want 415", res.StatusCode)
	}
}

func TestProgressFollowsThePlayer(t *testing.T) {
	ts := newTestServer(t)
	laptop, phone, bob := newClient(t, ts), newClient(t, ts), newClient(t, ts)
	laptop.login("Ann")
	phone.login("ann")
	bob.login("Bob")
	_, photo := laptop.upload("Lake.png", pngBytes(8))

	saved := map[string]any{"v": 1, "placed": 12, "total": 72}
	if res, body := laptop.json("PUT", "/api/progress/"+photo.ID, saved); res.StatusCode != http.StatusOK {
		t.Fatalf("save: %d %s", res.StatusCode, body)
	}

	_, body := phone.do("GET", "/api/progress", nil, "")
	var got struct{ Progress map[string]map[string]any }
	json.Unmarshal(body, &got)
	if got.Progress[photo.ID]["placed"] != float64(12) {
		t.Fatalf("phone sees %s", body)
	}

	_, body = bob.do("GET", "/api/progress", nil, "")
	got.Progress = nil
	json.Unmarshal(body, &got)
	if len(got.Progress) != 0 {
		t.Fatalf("bob sees ann's progress: %s", body)
	}

	if res, _ := laptop.json("PUT", "/api/progress/"+strings.Repeat("a", 32), saved); res.StatusCode != http.StatusNotFound {
		t.Fatalf("progress for unknown photo: %d", res.StatusCode)
	}
	if res, _ := laptop.do("PUT", "/api/progress/"+photo.ID, strings.NewReader("[1,2]"), "application/json"); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-object progress: %d", res.StatusCode)
	}
}

func TestStoreSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenStore(dir)
	p, err := store.AddPhoto(Photo{Name: "Cabin", By: "ann", Type: "image/png", ThumbType: "image/png"}, pngBytes(4), pngBytes(2))
	if err != nil {
		t.Fatal(err)
	}
	again, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if photos := again.Photos(); len(photos) != 1 || photos[0].ID != p.ID {
		t.Fatalf("after reopen: %+v", photos)
	}
	if _, ct, ok := again.File(p.Thumb); !ok || ct != "image/png" {
		t.Fatalf("thumb after reopen: %v %s", ok, ct)
	}
}

func TestClientIP(t *testing.T) {
	for _, tc := range []struct{ remote, fwd, want string }{
		{"203.0.113.9:5000", "", "203.0.113.9"},
		{"203.0.113.9:5000", "198.51.100.1", "203.0.113.9"}, // not from a local proxy: header ignored
		{"127.0.0.1:5000", "198.51.100.1", "198.51.100.1"},
		{"127.0.0.1:5000", "10.0.0.1, 198.51.100.1", "198.51.100.1"},
		{"[::1]:5000", "198.51.100.2", "198.51.100.2"},
		{"127.0.0.1:5000", "", "127.0.0.1"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.remote
		if tc.fwd != "" {
			r.Header.Set("X-Forwarded-For", tc.fwd)
		}
		if got := clientIP(r); got != tc.want {
			t.Errorf("clientIP(%s, %q) = %s, want %s", tc.remote, tc.fwd, got, tc.want)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	for _, tc := range []struct {
		in, id, display string
		ok              bool
	}{
		{"  Grandma   Jo ", "grandma jo", "Grandma Jo", true},
		{"", "", "", false},
		{"   ", "", "", false},
		{strings.Repeat("x", 41), "", "", false},
		{"bad\x00name", "", "", false},
	} {
		id, display, ok := normalizeName(tc.in)
		if id != tc.id || display != tc.display || ok != tc.ok {
			t.Errorf("normalizeName(%q) = %q, %q, %v", tc.in, id, display, ok)
		}
	}
}
