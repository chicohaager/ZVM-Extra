// Package schedule runs periodic, retention-limited snapshots per VM.
package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chicohaager/zima-vm-extras/internal/virsh"
)

// AutoPrefix marks snapshots created by the scheduler. Only snapshots with
// this prefix are considered for retention pruning — manual snapshots are
// never touched.
const AutoPrefix = "auto-"

// Entry is a per-VM snapshot schedule.
type Entry struct {
	VM            string `json:"vm"`
	Enabled       bool   `json:"enabled"`
	IntervalHours int    `json:"interval_hours"` // how often to snapshot
	Keep          int    `json:"keep"`           // newest N auto-snapshots kept
	LastRunUnix   int64  `json:"last_run_unix"`  // 0 = never run
}

// Store persists schedules in a JSON file. Concurrency-safe.
type Store struct {
	Path string
	mu   sync.RWMutex
	data map[string]Entry
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
		return fmt.Errorf("schedule store decode: %w", err)
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
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// List returns all schedules.
func (s *Store) List() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, 0, len(s.data))
	for _, e := range s.data {
		out = append(out, e)
	}
	return out
}

// Get returns the schedule for a VM, or a zero value with Enabled=false.
func (s *Store) Get(vm string) Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if e, ok := s.data[vm]; ok {
		return e
	}
	return Entry{VM: vm}
}

// Upsert validates and stores a schedule (LastRun is preserved).
func (s *Store) Upsert(in Entry) (Entry, error) {
	if in.VM == "" {
		return Entry{}, fmt.Errorf("vm required")
	}
	if in.Enabled && in.IntervalHours <= 0 {
		return Entry{}, fmt.Errorf("interval_hours must be > 0")
	}
	if in.Keep < 0 {
		in.Keep = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.data[in.VM]; ok {
		in.LastRunUnix = prev.LastRunUnix // never reset the run clock on edit
	}
	s.data[in.VM] = in
	if err := s.saveLocked(); err != nil {
		return Entry{}, err
	}
	return in, nil
}

// Delete removes a VM's schedule.
func (s *Store) Delete(vm string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, vm)
	return s.saveLocked()
}

// Controller is the subset of the virsh client the scheduler needs.
type Controller interface {
	State(name string) (string, error)
	CreateSnapshot(domain, name, description string, external bool, externalDir string) error
	ListSnapshots(domain string) ([]virsh.Snapshot, error)
	DeleteSnapshot(domain, snapshot string, withChildren bool) error
}

// Run is the scheduler loop: every checkInterval it creates any due snapshots
// and prunes old auto-snapshots beyond each schedule's Keep count.
func (s *Store) Run(ctx context.Context, vc Controller, snapshotRoot string, checkInterval time.Duration, logf func(string, ...any)) {
	t := time.NewTicker(checkInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(vc, snapshotRoot, logf)
		}
	}
}

func (s *Store) tick(vc Controller, snapshotRoot string, logf func(string, ...any)) {
	now := time.Now()
	var due []Entry
	s.mu.Lock()
	for vm, e := range s.data {
		if !e.Enabled || e.IntervalHours <= 0 {
			continue
		}
		if e.LastRunUnix != 0 &&
			now.Sub(time.Unix(e.LastRunUnix, 0)) < time.Duration(e.IntervalHours)*time.Hour {
			continue
		}
		e.LastRunUnix = now.Unix()
		s.data[vm] = e
		due = append(due, e)
	}
	if len(due) > 0 {
		_ = s.saveLocked()
	}
	s.mu.Unlock()
	for _, e := range due {
		s.snapshot(vc, snapshotRoot, e, now, logf)
	}
}

func (s *Store) snapshot(vc Controller, snapshotRoot string, e Entry, now time.Time, logf func(string, ...any)) {
	state, err := vc.State(e.VM)
	if err != nil {
		if logf != nil {
			logf("schedule %s: state check: %v", e.VM, err)
		}
		return
	}
	// Same rule as a manual snapshot: running/paused → full external snapshot,
	// shut-off → internal.
	external := state == "running" || state == "paused"
	name := AutoPrefix + now.Format("20060102-150405")
	extDir := ""
	if external {
		extDir = filepath.Join(snapshotRoot, e.VM)
		if err := os.MkdirAll(extDir, 0o755); err != nil {
			if logf != nil {
				logf("schedule %s: mkdir: %v", e.VM, err)
			}
			return
		}
	}
	if err := vc.CreateSnapshot(e.VM, name, "scheduled snapshot", external, extDir); err != nil {
		if logf != nil {
			logf("schedule %s: create %s: %v", e.VM, name, err)
		}
		return
	}
	if logf != nil {
		logf("schedule: created snapshot %s for %s", name, e.VM)
	}
	s.prune(vc, e, logf)
}

// prune deletes auto-snapshots beyond the Keep count, oldest first.
func (s *Store) prune(vc Controller, e Entry, logf func(string, ...any)) {
	if e.Keep <= 0 {
		return
	}
	snaps, err := vc.ListSnapshots(e.VM)
	if err != nil {
		return
	}
	var autos []string
	for _, sn := range snaps {
		if strings.HasPrefix(sn.Name, AutoPrefix) {
			autos = append(autos, sn.Name)
		}
	}
	// The auto- name carries a sortable timestamp, so lexical sort == oldest first.
	sort.Strings(autos)
	if len(autos) <= e.Keep {
		return
	}
	for _, name := range autos[:len(autos)-e.Keep] {
		if err := vc.DeleteSnapshot(e.VM, name, false); err != nil {
			if logf != nil {
				logf("schedule prune %s/%s: %v", e.VM, name, err)
			}
			continue
		}
		if logf != nil {
			logf("schedule: pruned old snapshot %s/%s", e.VM, name)
		}
	}
}
