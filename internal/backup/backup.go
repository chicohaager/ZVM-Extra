// Package backup exports a VM — its domain XML plus disk images — to a
// directory. Disks are copied with `qemu-img convert -O qcow2`, so the
// result is a compact, standalone qcow2 regardless of the source format
// (raw, qcow2, or a backing chain). Backups run asynchronously as jobs.
package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/chicohaager/zima-vm-extras/internal/virsh"
)

const qemuImgBin = "/usr/bin/qemu-img"

// Job is one backup run.
type Job struct {
	ID          string `json:"id"`
	VM          string `json:"vm"`
	Dest        string `json:"dest"`  // final directory <destDir>/<vm>-<ts>
	State       string `json:"state"` // running | done | failed
	Step        string `json:"step"`  // human-readable current step
	StartedUnix int64  `json:"started_unix"`
	EndedUnix   int64  `json:"ended_unix,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Controller is the subset of the virsh client the backup needs.
type Controller interface {
	State(name string) (string, error)
	DumpXML(name string) (string, error)
	DomainDisks(name string) ([]virsh.Disk, error)
}

// Manager owns backup jobs (kept in memory; cleared on daemon restart).
type Manager struct {
	vc   Controller
	logf func(string, ...any)
	mu   sync.Mutex
	jobs map[string]*Job
}

// NewManager returns a Manager. logf may be nil.
func NewManager(vc Controller, logf func(string, ...any)) *Manager {
	return &Manager{vc: vc, logf: logf, jobs: map[string]*Job{}}
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// List returns all jobs, newest first.
func (m *Manager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, *j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].StartedUnix > out[k].StartedUnix })
	return out
}

// Start validates and launches an asynchronous backup of vm into destDir.
// The VM must be shut off so the disk image is in a consistent state.
func (m *Manager) Start(vm, destDir string) (Job, error) {
	state, err := m.vc.State(vm)
	if err != nil {
		return Job{}, err
	}
	if state != "shut off" {
		return Job{}, fmt.Errorf("the VM must be shut off to back up safely (currently %q)", state)
	}
	xml, err := m.vc.DumpXML(vm)
	if err != nil {
		return Job{}, err
	}
	disks, err := m.vc.DomainDisks(vm)
	if err != nil {
		return Job{}, err
	}
	dest := filepath.Join(destDir, vm+"-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return Job{}, err
	}
	job := &Job{
		ID: newID(), VM: vm, Dest: dest, State: "running",
		Step: "starting", StartedUnix: time.Now().Unix(),
	}
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.pruneLocked()
	snap := *job
	m.mu.Unlock()
	go m.run(job, xml, disks)
	return snap, nil
}

// pruneLocked keeps only the 50 newest jobs (by StartedUnix). Caller holds m.mu.
func (m *Manager) pruneLocked() {
	const maxJobs = 50
	if len(m.jobs) <= maxJobs {
		return
	}
	type ref struct {
		id      string
		started int64
	}
	refs := make([]ref, 0, len(m.jobs))
	for id, j := range m.jobs {
		refs = append(refs, ref{id: id, started: j.StartedUnix})
	}
	sort.Slice(refs, func(i, k int) bool { return refs[i].started > refs[k].started })
	for _, r := range refs[maxJobs:] {
		delete(m.jobs, r.id)
	}
}

func (m *Manager) set(job *Job, step string) {
	m.mu.Lock()
	job.Step = step
	m.mu.Unlock()
}

func (m *Manager) finish(job *Job, err error) {
	m.mu.Lock()
	job.EndedUnix = time.Now().Unix()
	if err != nil {
		job.State, job.Step, job.Error = "failed", "failed", err.Error()
	} else {
		job.State, job.Step = "done", "done"
	}
	m.mu.Unlock()
	if m.logf != nil {
		if err != nil {
			m.logf("backup %s: failed: %v", job.VM, err)
		} else {
			m.logf("backup %s: done -> %s", job.VM, job.Dest)
		}
	}
}

func (m *Manager) run(job *Job, domXML string, disks []virsh.Disk) {
	m.set(job, "writing domain.xml")
	if err := os.WriteFile(filepath.Join(job.Dest, "domain.xml"), []byte(domXML), 0o644); err != nil {
		m.finish(job, err)
		return
	}

	var saved []string
	for i, d := range disks {
		// Removable media is not part of a VM backup.
		if d.Device == "cdrom" || d.Device == "floppy" {
			continue
		}
		m.set(job, "converting disk "+d.Target)
		// Disks can share a Target (rare, but possible across buses); the loop
		// index keeps each output filename unique so none overwrites another.
		name := d.Target + "-" + strconv.Itoa(i) + ".qcow2"
		out := filepath.Join(job.Dest, name)
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
		err := exec.CommandContext(ctx, qemuImgBin, "convert", "-O", "qcow2", d.Source, out).Run()
		cancel()
		if err != nil {
			m.finish(job, fmt.Errorf("qemu-img convert %s: %w", d.Target, err))
			return
		}
		saved = append(saved, name)
	}

	m.set(job, "writing manifest")
	manifest, _ := json.MarshalIndent(map[string]any{
		"vm":      job.VM,
		"created": time.Now().Format(time.RFC3339),
		"disks":   saved,
		"format":  "qcow2",
	}, "", "  ")
	_ = os.WriteFile(filepath.Join(job.Dest, "manifest.json"), manifest, 0o644)
	m.finish(job, nil)
}
