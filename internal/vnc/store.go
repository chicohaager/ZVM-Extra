package vnc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PinnedVNC holds VNC configs we want to preserve.
type PinnedVNC struct {
	VM            string `json:"vm"`
	Enabled       bool   `json:"enabled"`
	Port          int    `json:"port"`
	ListenAddress string `json:"listen_address"`
	Password      string `json:"password,omitempty"`
}

// Store persists VNC configs in a JSON file. Concurrency-safe.
type Store struct {
	Path string
	mu   sync.RWMutex
	data map[string]PinnedVNC
}

// NewStore opens or creates the store.
func NewStore(path string) (*Store, error) {
	s := &Store{Path: path, data: map[string]PinnedVNC{}}
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
	var arr []PinnedVNC
	if err := json.Unmarshal(b, &arr); err != nil {
		return fmt.Errorf("vnc store decode: %w", err)
	}
	for _, p := range arr {
		s.data[p.VM] = p
	}
	return nil
}

func (s *Store) saveLocked() error {
	arr := make([]PinnedVNC, 0, len(s.data))
	for _, p := range s.data {
		arr = append(arr, p)
	}
	b, err := json.MarshalIndent(arr, "", "  ")
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

// All returns every VNC config.
func (s *Store) All() []PinnedVNC {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PinnedVNC, 0, len(s.data))
	for _, p := range s.data {
		out = append(out, p)
	}
	return out
}

// Get returns the VNC config of one VM.
func (s *Store) Get(vm string) (PinnedVNC, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.data[vm]
	return p, ok
}

// Add adds/updates a VNC config.
func (s *Store) Add(p PinnedVNC) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[p.VM] = p
	return s.saveLocked()
}

// Remove deletes a VNC config from the store.
func (s *Store) Remove(vm string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, vm)
	return s.saveLocked()
}

// Controller is the subset of virsh client needed for VNC reconciliation.
type Controller interface {
	State(name string) (string, error)
	DumpXML(name string) (string, error)
	DefineXML(xml string) error
}

// Reconcile re-applies the VNC settings to VM configs if they don't match.
func (s *Store) Reconcile(vc Controller, logf func(string, ...any)) {
	for _, p := range s.All() {
		if _, err := vc.State(p.VM); err != nil {
			continue // VM deleted
		}
		xmlStr, err := vc.DumpXML(p.VM)
		if err != nil {
			if logf != nil {
				logf("vnc reconcile %s dump: %v", p.VM, err)
			}
			continue
		}

		current, has := GetVNCConfig(xmlStr)
		// Check if we need to reconcile
		autoportVal := "no"
		if p.Port <= 0 {
			autoportVal = "yes"
		}

		needsUpdate := false
		if !has {
			needsUpdate = true
		} else {
			if current.Listen != p.ListenAddress {
				needsUpdate = true
			}
			if current.Autoport != autoportVal {
				needsUpdate = true
			}
			if autoportVal == "no" && current.Port != p.Port {
				needsUpdate = true
			}
			if current.Passwd != p.Password {
				needsUpdate = true
			}
		}

		if !needsUpdate {
			continue
		}

		// Apply
		newXML, err := ModifyGraphicsXML(xmlStr, p.Port, p.ListenAddress, p.Password)
		if err != nil {
			if logf != nil {
				logf("vnc reconcile: modify XML failed for %s: %v", p.VM, err)
			}
			continue
		}

		if err := vc.DefineXML(newXML); err != nil {
			if logf != nil {
				logf("vnc reconcile: define VM %s failed: %v", p.VM, err)
			}
			continue
		}

		if logf != nil {
			logf("vnc reconcile: restored VNC settings (listen=%s, port=%d) for VM %s", p.ListenAddress, p.Port, p.VM)
		}
	}
}

// RunReconciler runs VNC reconciliation loop.
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
