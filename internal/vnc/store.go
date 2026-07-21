package vnc

// VNC password pinning: persisted desired-state so a console password
// survives the official ZVM UI re-saving (and thereby stripping) the domain
// XML, as well as host reboots. A reconciler periodically repairs the
// persistent config.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is the VNC console password VM Extras keeps applied to one VM.
type Entry struct {
	VM       string `json:"vm"`
	Password string `json:"password"`
}

// Store persists per-VM VNC passwords in a JSON file. Because the file holds
// secrets it is written mode 0600 (root-only) — unlike the USB/PCI stores,
// whose pinned-device lists are not sensitive. Concurrency-safe.
type Store struct {
	Path string
	mu   sync.RWMutex
	data map[string]Entry // keyed by VM name
}

// NewStore opens or creates the store. A missing file is treated as empty.
func NewStore(path string) (*Store, error) {
	s := &Store{Path: path, data: map[string]Entry{}}
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
	var arr []Entry
	if err := json.Unmarshal(b, &arr); err != nil {
		return fmt.Errorf("vnc store decode: %w", err)
	}
	for _, e := range arr {
		s.data[e.VM] = e
	}
	return nil
}

func (s *Store) saveLocked() error {
	arr := make([]Entry, 0, len(s.data))
	for _, e := range s.data {
		arr = append(arr, e)
	}
	b, err := json.MarshalIndent(arr, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	// 0600: the file contains plaintext VNC passwords.
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// All returns every stored entry.
func (s *Store) All() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, 0, len(s.data))
	for _, e := range s.data {
		out = append(out, e)
	}
	return out
}

// Get returns the entry for a VM and whether one exists.
func (s *Store) Get(vm string) (Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.data[vm]
	return e, ok
}

// Set stores (upserts) a VM's VNC password.
func (s *Store) Set(e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[e.VM] = e
	return s.saveLocked()
}

// Remove deletes a VM's stored password.
func (s *Store) Remove(vm string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, vm)
	return s.saveLocked()
}

// Controller is the subset of the virsh client the reconciler needs.
type Controller interface {
	State(name string) (string, error)
	VNCHasPassword(name string) (bool, error)
	SetVNCPassword(name, pw string) error
}

// Reconcile re-applies the stored VNC password to every VM whose persistent
// config no longer has one — e.g. after the official ZVM UI re-saved the
// domain and stripped the passwd attribute. Repairing the *persistent* config
// is what makes the password survive the next VM start.
func (s *Store) Reconcile(vc Controller, logf func(string, ...any)) {
	for _, e := range s.All() {
		// State errors for an undefined (deleted) VM — skip those.
		if _, err := vc.State(e.VM); err != nil {
			continue
		}
		has, err := vc.VNCHasPassword(e.VM)
		if err != nil {
			if logf != nil {
				logf("vnc reconcile %s: %v", e.VM, err)
			}
			continue
		}
		if has {
			continue
		}
		if err := vc.SetVNCPassword(e.VM, e.Password); err != nil {
			if logf != nil {
				logf("vnc reconcile: repair %s failed: %v", e.VM, err)
			}
			continue
		}
		if logf != nil {
			logf("vnc reconcile: restored VNC password on %s config", e.VM)
		}
	}
}

// RunReconciler reconciles immediately, then every interval until ctx ends.
func (s *Store) RunReconciler(ctx context.Context, vc Controller, interval time.Duration, logf func(string, ...any)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	s.Reconcile(vc, logf)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Reconcile(vc, logf)
		}
	}
}
