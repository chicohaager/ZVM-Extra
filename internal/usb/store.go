package usb

// USB device pinning: persisted desired-state so a passthrough survives the
// official ZVM UI re-saving (and thereby stripping) the domain XML, as well
// as host reboots. A reconciler periodically repairs the persistent config.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PinnedDevice is a USB device the user wants kept attached to a VM.
type PinnedDevice struct {
	VM          string `json:"vm"`
	VendorID    string `json:"vendor_id"`
	ProductID   string `json:"product_id"`
	Description string `json:"description,omitempty"`
}

func (p PinnedDevice) key() string {
	return p.VM + "/" + p.VendorID + ":" + p.ProductID
}

// Store persists pinned USB devices in a JSON file. Concurrency-safe.
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
		return fmt.Errorf("usb store decode: %w", err)
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

// Add pins a device (upsert by VM + vendor:product).
func (s *Store) Add(p PinnedDevice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[p.key()] = p
	return s.saveLocked()
}

// Remove unpins a device.
func (s *Store) Remove(vm, vendor, product string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, PinnedDevice{VM: vm, VendorID: vendor, ProductID: product}.key())
	return s.saveLocked()
}

// Controller is the subset of the virsh client the reconciler needs.
type Controller interface {
	State(name string) (string, error)
	HasUSBHostdev(vm, vendor, product string) (bool, error)
	AttachDeviceConfig(vm, xml string) error
}

// Reconcile re-adds every pinned device whose VM's persistent config no
// longer declares it — e.g. after the official ZVM UI re-saved the domain
// and stripped the <hostdev>. Repairing the *persistent* config is what
// makes the passthrough survive the next VM reboot.
func (s *Store) Reconcile(vc Controller, logf func(string, ...any)) {
	for _, p := range s.All() {
		// State errors for an undefined (deleted) VM — skip those.
		if _, err := vc.State(p.VM); err != nil {
			continue
		}
		has, err := vc.HasUSBHostdev(p.VM, p.VendorID, p.ProductID)
		if err != nil {
			if logf != nil {
				logf("usb reconcile %s %s:%s: %v", p.VM, p.VendorID, p.ProductID, err)
			}
			continue
		}
		if has {
			continue
		}
		if err := vc.AttachDeviceConfig(p.VM, HostdevXML(p.VendorID, p.ProductID)); err != nil {
			if logf != nil {
				logf("usb reconcile: repair %s:%s -> %s failed: %v",
					p.VendorID, p.ProductID, p.VM, err)
			}
			continue
		}
		if logf != nil {
			logf("usb reconcile: restored %s:%s in %s config", p.VendorID, p.ProductID, p.VM)
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
