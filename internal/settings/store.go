// Package settings persists the handful of UI preferences that must outlive
// a page reload.
//
// The backup and snapshot tabs used to reset their target directory to the
// built-in default on every visit, so anyone backing up to a remote mount had
// to retype the path each time (reported against v0.6.3). Browser storage
// would have been cheaper, but the target directory is a property of the
// appliance, not of whichever laptop opened the UI — so it lives here.
package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Data is the whole persisted set. Paths only; nothing secret belongs here.
type Data struct {
	BackupDir   string `json:"backup_dir,omitempty"`
	SnapshotDir string `json:"snapshot_dir,omitempty"`
}

// Store is a small JSON-backed settings file.
type Store struct {
	Path string
	mu   sync.RWMutex
	data Data
}

// NewStore opens or creates the store. A missing file is treated as empty.
func NewStore(path string) (*Store, error) {
	s := &Store{Path: path}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return fmt.Errorf("settings decode: %w", err)
	}
	return nil
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Get returns a copy of the current settings.
func (s *Store) Get() Data {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data
}

// Set replaces the settings and writes them to disk. Fields left empty in in
// clear the stored value — the caller sends the full set it wants persisted.
func (s *Store) Set(in Data) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = in
	return s.saveLocked()
}
