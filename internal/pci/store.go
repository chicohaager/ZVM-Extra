package pci

// PCI passthrough pinning: persisted desired-state so a passthrough survives
// the official ZVM UI re-saving (and stripping) the domain XML, plus host
// reboots. A reconciler periodically repairs the persistent config.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PinnedDevice is a PCI device the user wants kept passed through to a VM.
type PinnedDevice struct {
	VM          string `json:"vm"`
	Address     string `json:"address"`
	Description string `json:"description,omitempty"`
}

func (p PinnedDevice) key() string { return p.VM + "/" + p.Address }

// Store persists pinned PCI devices in a JSON file. Concurrency-safe.
type Store struct {
	Path string
	mu   sync.RWMutex
	data map[string]PinnedDevice
}

// NewStore opens or creates the store. A missing file is treated as empty.
func NewStore(path string) (*Store, error) {
	s := &Store{Path: path, data: map[string]PinnedDevice{}}
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
	var arr []PinnedDevice
	if err := json.Unmarshal(b, &arr); err != nil {
		return fmt.Errorf("pci store decode: %w", err)
	}
	for _, p := range arr {
		s.data[p.key()] = p
	}
	return nil
}

func (s *Store) saveLocked() error {
	arr := make([]PinnedDevice, 0, len(s.data))
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

// All returns every pinned device.
func (s *Store) All() []PinnedDevice {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PinnedDevice, 0, len(s.data))
	for _, p := range s.data {
		out = append(out, p)
	}
	return out
}

// ForVM returns the pinned devices of one VM.
func (s *Store) ForVM(vm string) []PinnedDevice {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []PinnedDevice{}
	for _, p := range s.data {
		if p.VM == vm {
			out = append(out, p)
		}
	}
	return out
}

// Add pins a device (upsert by VM + address).
func (s *Store) Add(p PinnedDevice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[p.key()] = p
	return s.saveLocked()
}

// Remove unpins a device.
func (s *Store) Remove(vm, address string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, PinnedDevice{VM: vm, Address: address}.key())
	return s.saveLocked()
}

// Controller is the subset of the virsh client the reconciler needs.
type Controller interface {
	State(name string) (string, error)
	HasPCIHostdev(vm, address string) (bool, error)
	AttachDeviceConfig(vm, xml string) error
}

// Reconcile re-adds every pinned device whose VM's persistent config no
// longer declares it — e.g. after the official ZVM UI re-saved the domain
// and stripped the <hostdev>.
func (s *Store) Reconcile(vc Controller, logf func(string, ...any)) {
	for _, p := range s.All() {
		if _, err := vc.State(p.VM); err != nil {
			continue // undefined (deleted) VM
		}
		has, err := vc.HasPCIHostdev(p.VM, p.Address)
		if err != nil {
			if logf != nil {
				logf("pci reconcile %s %s: %v", p.VM, p.Address, err)
			}
			continue
		}
		if has {
			continue
		}
		xml, err := HostdevXML(p.Address)
		if err != nil {
			continue
		}
		if err := vc.AttachDeviceConfig(p.VM, xml); err != nil {
			if logf != nil {
				logf("pci reconcile: repair %s -> %s failed: %v", p.Address, p.VM, err)
			}
			continue
		}
		if logf != nil {
			logf("pci reconcile: restored %s in %s config", p.Address, p.VM)
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
