// Package tpm keeps a TPM 2.0 emulator device pinned to a VM.
//
// ZVM re-saves a domain whenever the user touches it in the official UI, and
// that re-save drops device elements ZVM does not know about — the same reason
// the USB, PCI and VNC stores exist. A TPM the operator enabled here would
// quietly disappear, and Windows 11 would start refusing again on the next
// boot with nothing in the UI to explain why. So the desired state is
// persisted and a reconciler repairs the domain.
package tpm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store persists the set of VMs that should have a TPM device.
type Store struct {
	Path string
	mu   sync.RWMutex
	data map[string]bool // VM name -> pinned
}

// NewStore opens or creates the store. A missing file is treated as empty.
func NewStore(path string) (*Store, error) {
	s := &Store{Path: path, data: map[string]bool{}}
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
	var vms []string
	if err := json.Unmarshal(b, &vms); err != nil {
		return fmt.Errorf("tpm store decode: %w", err)
	}
	for _, vm := range vms {
		s.data[vm] = true
	}
	return nil
}

func (s *Store) saveLocked() error {
	vms := make([]string, 0, len(s.data))
	for vm := range s.data {
		vms = append(vms, vm)
	}
	b, err := json.MarshalIndent(vms, "", "  ")
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

// Pinned reports whether a VM's TPM is pinned by VM Extras.
func (s *Store) Pinned(vm string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data[vm]
}

// All returns every pinned VM name.
func (s *Store) All() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.data))
	for vm := range s.data {
		out = append(out, vm)
	}
	return out
}

// Pin records that a VM should keep its TPM device.
func (s *Store) Pin(vm string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[vm] = true
	return s.saveLocked()
}

// Unpin stops VM Extras from re-applying a TPM to this VM.
func (s *Store) Unpin(vm string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, vm)
	return s.saveLocked()
}

// Controller is the subset of the virsh client the reconciler needs.
type Controller interface {
	State(name string) (string, error)
	TPMInfo(name string) (present bool, model, version string, err error)
	SetTPM(name string, enabled bool) error
}

// Reconcile re-adds the TPM device to every pinned VM whose persistent config
// lost it — e.g. after the official ZVM UI re-saved the domain.
//
// It only ever acts on a domain that genuinely has no TPM. A reconciler that
// keeps acting on an unchanged domain is reporting a broken read path, not a
// restless system, so the read side must be exact: TPMInfo reads with
// --security-info for the same reason the VNC store does.
func (s *Store) Reconcile(vc Controller, logf func(string, ...any)) {
	for _, vm := range s.All() {
		// State errors for an undefined (deleted) VM — skip those.
		if _, err := vc.State(vm); err != nil {
			continue
		}
		present, _, _, err := vc.TPMInfo(vm)
		if err != nil {
			if logf != nil {
				logf("tpm reconcile %s: %v", vm, err)
			}
			continue
		}
		if present {
			continue
		}
		if err := vc.SetTPM(vm, true); err != nil {
			if logf != nil {
				logf("tpm reconcile: repair %s failed: %v", vm, err)
			}
			continue
		}
		if logf != nil {
			logf("tpm reconcile: restored TPM device on %s config", vm)
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
