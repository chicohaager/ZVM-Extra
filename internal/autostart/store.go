// Package autostart persists per-VM autostart preferences (delay, order)
// in a JSON file under DATA_DIR. libvirt's own `virsh autostart` only knows
// on/off — we add the missing bits.
package autostart

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Entry is the per-VM config persisted on disk.
type Entry struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`  // start on host boot
	Order    int    `json:"order"`    // lower starts first
	DelayS   int    `json:"delay_s"`  // seconds to wait after starting this VM
	Watchdog bool   `json:"watchdog"` // keep running: restart if it stops/crashes
}

// Store is a simple file-backed map. Concurrency-safe.
type Store struct {
	Path string
	mu   sync.RWMutex
	data map[string]Entry
}

// New opens or creates the store. Missing file is treated as empty.
func New(path string) (*Store, error) {
	s := &Store{Path: path, data: map[string]Entry{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		return fmt.Errorf("autostart store decode: %w", err)
	}
	for _, e := range arr {
		s.data[e.Name] = e
	}
	return nil
}

func (s *Store) saveLocked() error {
	arr := make([]Entry, 0, len(s.data))
	for _, e := range s.data {
		arr = append(arr, e)
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].Order < arr[j].Order })
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

// All returns a snapshot sorted by Order.
func (s *Store) All() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	arr := make([]Entry, 0, len(s.data))
	for _, e := range s.data {
		arr = append(arr, e)
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].Order < arr[j].Order })
	return arr
}

// Get returns the entry for a VM, or a zero value with Enabled=false.
func (s *Store) Get(name string) Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if e, ok := s.data[name]; ok {
		return e
	}
	return Entry{Name: name}
}

// Set upserts and persists. Returns the stored entry.
func (s *Store) Set(e Entry) (Entry, error) {
	if e.Name == "" {
		return Entry{}, fmt.Errorf("entry.Name required")
	}
	if e.DelayS < 0 {
		e.DelayS = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[e.Name] = e
	if err := s.saveLocked(); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Delete removes a VM's autostart entry.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, name)
	return s.saveLocked()
}

// ---------- Boot orchestration ----------

// Controller is the subset of the virsh client the orchestrator needs.
type Controller interface {
	ListDomains() ([]string, error)
	State(name string) (string, error)
	Start(name string) error
	SetAutostart(name string, enable bool) error
}

// runMarker lives on tmpfs, so it is cleared on every reboot. Its presence
// means orchestration already ran this boot — a plain daemon restart must
// not re-start VMs the user has since shut down by hand.
const runMarker = "/run/zima-vm-extras-autostart.done"

// Run starts every enabled VM in Order sequence, waiting DelayS seconds
// after each before starting the next. VMs already running are skipped.
// It is idempotent and intended to be called once per boot, in a goroutine.
//
// VM Extras owns autostart end to end: the libvirt-native autostart flag is
// kept off for managed VMs (see the PUT handler) so virtqemud cannot start
// them early and defeat the ordering.
func (s *Store) Run(vc Controller, logf func(string, ...any)) {
	if _, err := os.Stat(runMarker); err == nil {
		return // already orchestrated this boot
	}

	// Wait for the libvirt daemon to become responsive (the watchdog may
	// start us only ~15s after boot, but be defensive anyway).
	ready := false
	for i := 0; i < 12; i++ {
		if _, err := vc.ListDomains(); err == nil {
			ready = true
			break
		}
		time.Sleep(5 * time.Second)
	}
	if !ready {
		if logf != nil {
			logf("autostart: libvirt not ready after 60s — skipping (will retry next start)")
		}
		return // no marker written: retry on the next daemon start
	}

	for _, e := range s.All() { // All() is sorted by Order
		if !e.Enabled {
			continue
		}
		// VM Extras owns autostart — keep the libvirt-native flag off so
		// virtqemud cannot start this VM early and bypass order/delay.
		// This also self-heals installs upgraded from an older version.
		if err := vc.SetAutostart(e.Name, false); err != nil && logf != nil {
			logf("autostart %s: could not clear libvirt-native flag: %v", e.Name, err)
		}
		state, err := vc.State(e.Name)
		if err != nil {
			if logf != nil {
				logf("autostart %s: state check failed: %v", e.Name, err)
			}
			continue
		}
		if state == "running" {
			continue
		}
		if logf != nil {
			logf("autostart: starting %s (order=%d, delay=%ds)", e.Name, e.Order, e.DelayS)
		}
		if err := vc.Start(e.Name); err != nil {
			if logf != nil {
				logf("autostart %s: start failed: %v", e.Name, err)
			}
			continue
		}
		if e.DelayS > 0 {
			time.Sleep(time.Duration(e.DelayS) * time.Second)
		}
	}

	if err := os.WriteFile(runMarker, []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		if logf != nil {
			logf("autostart: could not write run marker %s: %v", runMarker, err)
		}
	}
	if logf != nil {
		logf("autostart: orchestration complete")
	}
}

// RunWatchdog keeps watchdog-enabled VMs running: every interval it checks
// each one and restarts it if it is found shut off or crashed. This is an
// explicit keep-alive contract — to stop such a VM the user turns the
// watchdog off first. Blocking; run it in a goroutine.
func (s *Store) RunWatchdog(ctx context.Context, vc Controller, interval time.Duration, logf func(string, ...any)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Stay dormant until boot orchestration (Run) has finished and
			// written the marker — otherwise the watchdog would start VMs
			// concurrently with the ordered/delayed boot sequence.
			if _, err := os.Stat(runMarker); err != nil {
				continue
			}
			for _, e := range s.All() {
				if !e.Watchdog {
					continue
				}
				state, err := vc.State(e.Name)
				if err != nil {
					continue
				}
				if state == "shut off" || state == "crashed" {
					if logf != nil {
						logf("watchdog: %s is %q — restarting", e.Name, state)
					}
					if err := vc.Start(e.Name); err != nil && logf != nil {
						logf("watchdog: restart %s failed: %v", e.Name, err)
					}
				}
			}
		}
	}
}
