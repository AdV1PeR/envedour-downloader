// Package groups stores the group chats that an administrator allowed at
// runtime through /admin. The static ALLOWED_GROUP_IDS list from .env remains
// the source of truth for env-managed groups; this store only holds the
// overlay that the bot can change without a restart.
package groups

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Group is one dynamically allowed group chat.
type Group struct {
	ID      int64     `json:"id"`
	Title   string    `json:"title,omitempty"`
	AddedBy int64     `json:"added_by,omitempty"`
	AddedAt time.Time `json:"added_at,omitempty"`
}

type fileData struct {
	Groups []Group `json:"groups"`
}

// Store is a small, concurrency-safe JSON-backed group registry.
type Store struct {
	mu     sync.RWMutex
	path   string
	groups map[int64]Group
}

// New returns an empty store. Call Load to read an existing file.
func New(path string) *Store {
	return &Store{
		path:   path,
		groups: make(map[int64]Group),
	}
}

// Load reads the JSON file. A missing or empty file is not an error.
func (s *Store) Load() error {
	if s.path == "" {
		return nil
	}

	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	if len(data) == 0 {
		return nil
	}

	var stored fileData
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}

	loaded := make(map[int64]Group, len(stored.Groups))
	for _, group := range stored.Groups {
		if group.ID == 0 {
			continue
		}
		loaded[group.ID] = group
	}

	s.mu.Lock()
	s.groups = loaded
	s.mu.Unlock()
	return nil
}

// Add inserts a group or refreshes its title. It reports whether the group was
// newly created. The boolean is useful for user-facing messages.
func (s *Store) Add(id int64, title string, addedBy int64) (Group, bool, error) {
	if id == 0 {
		return Group{}, false, fmt.Errorf("group id is empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.groups[id]; ok {
		if title != "" && existing.Title != title {
			existing.Title = title
			s.groups[id] = existing
			if err := s.saveLocked(); err != nil {
				return Group{}, false, err
			}
		}
		return existing, false, nil
	}

	group := Group{
		ID:      id,
		Title:   title,
		AddedBy: addedBy,
		AddedAt: time.Now().UTC(),
	}
	s.groups[id] = group
	if err := s.saveLocked(); err != nil {
		delete(s.groups, id)
		return Group{}, false, err
	}
	return group, true, nil
}

// Remove deletes a dynamically added group.
func (s *Store) Remove(id int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	group, ok := s.groups[id]
	if !ok {
		return false, nil
	}
	delete(s.groups, id)
	if err := s.saveLocked(); err != nil {
		s.groups[id] = group
		return false, err
	}
	return true, nil
}

// Contains reports whether the group is in the dynamic list.
func (s *Store) Contains(id int64) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.groups[id]
	return ok
}

// All returns a sorted copy of the dynamic groups.
func (s *Store) All() []Group {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sortedLocked()
}

// Len reports the number of dynamic groups.
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.groups)
}

func (s *Store) sortedLocked() []Group {
	out := make([]Group, 0, len(s.groups))
	for _, group := range s.groups {
		out = append(out, group)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Title == out[j].Title {
			return out[i].ID < out[j].ID
		}
		return out[i].Title < out[j].Title
	})
	return out
}

// saveLocked writes atomically: a temp file in the same directory, fsync, then
// rename over the old file. Callers must hold s.mu.
func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(fileData{Groups: s.sortedLocked()}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal groups: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".allowed_groups-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary group file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temporary group file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary group file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary group file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary group file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace group file: %w", err)
	}
	return nil
}
