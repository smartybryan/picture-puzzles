package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrForbidden = errors.New("forbidden")

	idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Photo is one gallery entry. The full image and its thumbnail are stored as
// separate files named by their IDs.
type Photo struct {
	ID        string `json:"id"`
	Thumb     string `json:"thumb"`
	Name      string `json:"name"`
	By        string `json:"by"`
	ByName    string `json:"byName"`
	At        int64  `json:"at"` // unix milliseconds
	Type      string `json:"type"`
	ThumbType string `json:"thumbType"`
}

// Store keeps photos and progress on disk:
//
//	photos.json                 the gallery index
//	photos/<id>                 image files (full size and thumbnails)
//	progress/<player>/<id>.json one saved puzzle per player and photo
type Store struct {
	dir string

	mu     sync.Mutex
	photos []Photo           // newest first
	files  map[string]string // file id -> content type
}

func OpenStore(dir string) (*Store, error) {
	for _, d := range []string{dir, filepath.Join(dir, "photos"), filepath.Join(dir, "progress")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	s := &Store{dir: dir, files: map[string]string{}}
	b, err := os.ReadFile(s.indexPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &s.photos); err != nil {
			return nil, fmt.Errorf("read %s: %w", s.indexPath(), err)
		}
	}
	for _, p := range s.photos {
		s.files[p.ID] = p.Type
		s.files[p.Thumb] = p.ThumbType
	}
	return s, nil
}

func (s *Store) indexPath() string         { return filepath.Join(s.dir, "photos.json") }
func (s *Store) filePath(id string) string { return filepath.Join(s.dir, "photos", id) }

func (s *Store) Photos() []Photo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.photos)
}

// AddPhoto writes both image files, then records the photo in the index.
func (s *Store) AddPhoto(p Photo, full, thumb []byte) (Photo, error) {
	p.ID, p.Thumb = newID(), newID()
	if err := writeFileAtomic(s.filePath(p.ID), full, 0o644); err != nil {
		return Photo{}, err
	}
	if err := writeFileAtomic(s.filePath(p.Thumb), thumb, 0o644); err != nil {
		os.Remove(s.filePath(p.ID))
		return Photo{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	next := append([]Photo{p}, s.photos...)
	if err := s.saveIndex(next); err != nil {
		os.Remove(s.filePath(p.ID))
		os.Remove(s.filePath(p.Thumb))
		return Photo{}, err
	}
	s.photos = next
	s.files[p.ID] = p.Type
	s.files[p.Thumb] = p.ThumbType
	return p, nil
}

// RemovePhoto deletes a photo the player added, or any photo for an admin.
// Saved progress on it is left alone; it simply stops being reachable.
func (s *Store) RemovePhoto(id string, player Player) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.photos, func(p Photo) bool { return p.ID == id })
	if i < 0 {
		return ErrNotFound
	}
	p := s.photos[i]
	if p.By != player.ID && !player.Admin {
		return ErrForbidden
	}
	next := slices.Delete(slices.Clone(s.photos), i, i+1)
	if err := s.saveIndex(next); err != nil {
		return err
	}
	s.photos = next
	delete(s.files, p.ID)
	delete(s.files, p.Thumb)
	os.Remove(s.filePath(p.ID))
	os.Remove(s.filePath(p.Thumb))
	return nil
}

// File returns the path and content type of a stored image.
func (s *Store) File(id string) (path, contentType string, ok bool) {
	if !idPattern.MatchString(id) {
		return "", "", false
	}
	s.mu.Lock()
	ct, ok := s.files[id]
	s.mu.Unlock()
	return s.filePath(id), ct, ok
}

func (s *Store) HasPhoto(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.ContainsFunc(s.photos, func(p Photo) bool { return p.ID == id })
}

// Progress returns every saved puzzle for a player, keyed by photo ID.
func (s *Store) Progress(player Player) (map[string]json.RawMessage, error) {
	dir := filepath.Join(s.dir, "progress", playerKey(player.ID))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(entries))
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !idPattern.MatchString(id) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[id] = b
	}
	return out, nil
}

// SaveProgress stores a player's puzzle for one gallery photo.
func (s *Store) SaveProgress(player Player, photoID string, body []byte) error {
	if !idPattern.MatchString(photoID) || !s.HasPhoto(photoID) {
		return ErrNotFound
	}
	dir := filepath.Join(s.dir, "progress", playerKey(player.ID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, photoID+".json"), body, 0o644)
}

func (s *Store) saveIndex(photos []Photo) error {
	b, err := json.MarshalIndent(photos, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.indexPath(), b, 0o644)
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// writeFileAtomic writes to a temporary file in the same directory and renames
// it into place, so readers never see a half-written file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
