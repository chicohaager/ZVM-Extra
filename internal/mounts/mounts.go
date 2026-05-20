// Package mounts manages app-owned NFS / CIFS network mounts.
//
// Design:
//   - The full config (including the SMB password) is persisted in a JSON
//     file at DATA_DIR/mounts.json with mode 0600.
//   - On daemon start, entries marked AutoMount=true are mounted.
//   - mount.cifs reads credentials from /run/zvmx-cifs-<id>.creds (mode 0600,
//     written immediately before the mount call) to keep the password out
//     of /proc/<pid>/cmdline. The credentials file is removed after the call.
//   - Mountpoints are restricted to non-system paths. /mnt is mostly read-only
//     on ZimaOS rootfs (squashfs), so /DATA/.zvmx-mounts/<name>/ is the
//     recommended target.
package mounts

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Entry is one configured remote mount.
type Entry struct {
	ID         string `json:"id"`
	Name       string `json:"name"`              // human label
	Type       string `json:"type"`              // "nfs" | "cifs"
	Host       string `json:"host"`              // 192.168.1.10 or hostname
	Share      string `json:"share"`             // "/export/foo" (NFS) or "myshare" (CIFS)
	Mountpoint string `json:"mountpoint"`        // absolute path
	Username   string `json:"username,omitempty"` // CIFS
	Password   string `json:"password,omitempty"` // CIFS, stored on disk (mode 0600)
	ReadOnly   bool   `json:"read_only"`
	AutoMount  bool   `json:"auto_mount"`
	Options    string `json:"options,omitempty"` // extra mount options, comma-separated
}

// PublicView is what we send to the UI (no password).
type PublicView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Host        string `json:"host"`
	Share       string `json:"share"`
	Mountpoint  string `json:"mountpoint"`
	Username    string `json:"username,omitempty"`
	HasPassword bool   `json:"has_password"`
	ReadOnly    bool   `json:"read_only"`
	AutoMount   bool   `json:"auto_mount"`
	Options     string `json:"options,omitempty"`
	Mounted     bool   `json:"mounted"`
	LastError   string `json:"last_error,omitempty"`
}

// Manager holds the persisted entries.
type Manager struct {
	Path string // JSON config path
	mu   sync.RWMutex
	data map[string]*Entry
	last map[string]string // id -> last error
}

func New(path string) (*Manager, error) {
	m := &Manager{Path: path, data: map[string]*Entry{}, last: map[string]string{}}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) load() error {
	b, err := os.ReadFile(m.Path)
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
		return fmt.Errorf("mounts decode: %w", err)
	}
	for i := range arr {
		e := arr[i]
		m.data[e.ID] = &e
	}
	return nil
}

func (m *Manager) saveLocked() error {
	arr := make([]Entry, 0, len(m.data))
	for _, e := range m.data {
		arr = append(arr, *e)
	}
	b, err := json.MarshalIndent(arr, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.Path), 0o755); err != nil {
		return err
	}
	tmp := m.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.Path)
}

// List returns a snapshot of all entries with current mounted-state.
func (m *Manager) List() []PublicView {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]PublicView, 0, len(m.data))
	for _, e := range m.data {
		out = append(out, m.toView(e))
	}
	return out
}

func (m *Manager) Get(id string) (PublicView, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.data[id]
	if !ok {
		return PublicView{}, false
	}
	return m.toView(e), true
}

func (m *Manager) toView(e *Entry) PublicView {
	return PublicView{
		ID: e.ID, Name: e.Name, Type: e.Type, Host: e.Host, Share: e.Share,
		Mountpoint: e.Mountpoint, Username: e.Username, HasPassword: e.Password != "",
		ReadOnly: e.ReadOnly, AutoMount: e.AutoMount, Options: e.Options,
		Mounted: IsMounted(e.Mountpoint), LastError: m.last[e.ID],
	}
}

// Upsert validates and stores an entry. If id is empty, a new id is generated.
// password=="" means: keep existing password (if any).
func (m *Manager) Upsert(in Entry) (*Entry, error) {
	if err := validate(&in); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if in.ID == "" {
		in.ID = newID()
	}
	if existing, ok := m.data[in.ID]; ok && in.Password == "" {
		in.Password = existing.Password
	}
	cp := in
	m.data[in.ID] = &cp
	if err := m.saveLocked(); err != nil {
		return nil, err
	}
	return &cp, nil
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, id)
	delete(m.last, id)
	return m.saveLocked()
}

func (m *Manager) setError(id, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if msg == "" {
		delete(m.last, id)
	} else {
		m.last[id] = msg
	}
}

// Mount mounts the entry (creates Mountpoint if missing).
func (m *Manager) Mount(id string) error {
	m.mu.RLock()
	e, ok := m.data[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("mount id %q not found", id)
	}
	if IsMounted(e.Mountpoint) {
		return nil
	}
	if err := os.MkdirAll(e.Mountpoint, 0o755); err != nil {
		return fmt.Errorf("mkdir mountpoint: %w", err)
	}
	var err error
	switch e.Type {
	case "nfs", "nfs4":
		err = doMountNFS(e)
	case "cifs", "smb":
		err = doMountCIFS(e)
	default:
		err = fmt.Errorf("unsupported mount type %q", e.Type)
	}
	if err != nil {
		m.setError(id, err.Error())
		return err
	}
	m.setError(id, "")
	return nil
}

// Unmount unmounts the entry (config is kept).
func (m *Manager) Unmount(id string) error {
	m.mu.RLock()
	e, ok := m.data[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("mount id %q not found", id)
	}
	if !IsMounted(e.Mountpoint) {
		return nil
	}
	return runCmd(umountBin, e.Mountpoint)
}

// MountAllAuto mounts every entry with AutoMount=true. Errors are logged but
// don't abort iteration.
func (m *Manager) MountAllAuto(logf func(format string, args ...any)) {
	m.mu.RLock()
	ids := make([]string, 0, len(m.data))
	for id, e := range m.data {
		if e.AutoMount {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()
	for _, id := range ids {
		if err := m.Mount(id); err != nil {
			if logf != nil {
				logf("automount %s: %v", id, err)
			}
		}
	}
}

// ---------- helpers ----------

func validate(e *Entry) error {
	switch e.Type {
	case "nfs", "nfs4", "cifs", "smb":
	default:
		return fmt.Errorf("type must be nfs|nfs4|cifs|smb")
	}
	if strings.TrimSpace(e.Host) == "" {
		return fmt.Errorf("host required")
	}
	if strings.TrimSpace(e.Share) == "" {
		return fmt.Errorf("share required")
	}
	// A leading "-" would let host/share be parsed as a mount flag.
	if strings.HasPrefix(e.Host, "-") {
		return fmt.Errorf("host must not start with '-'")
	}
	if strings.HasPrefix(e.Share, "-") {
		return fmt.Errorf("share must not start with '-'")
	}
	if !filepath.IsAbs(e.Mountpoint) {
		return fmt.Errorf("mountpoint must be absolute path")
	}
	if strings.Contains(e.Mountpoint, "..") {
		return fmt.Errorf("mountpoint must not contain '..'")
	}
	clean := filepath.Clean(e.Mountpoint)
	for _, f := range []string{"/", "/proc", "/sys", "/dev", "/run", "/boot", "/usr", "/etc", "/opt", "/var"} {
		if clean == f || strings.HasPrefix(clean, f+"/") {
			return fmt.Errorf("mountpoint not allowed (system path %s)", f)
		}
	}
	// Tighten: no shell metacharacters in user-provided fields. The password
	// is included because it is written verbatim into the credentials file.
	for _, s := range []string{e.Host, e.Share, e.Username, e.Options, e.Password} {
		if strings.ContainsAny(s, "`$;|&<>\"\\\n") {
			return fmt.Errorf("invalid characters in input")
		}
	}
	// A caller-supplied ID is later substituted into filesystem paths; restrict
	// it to the hex form newID() produces.
	if e.ID != "" {
		for _, r := range e.ID {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return fmt.Errorf("invalid id")
			}
		}
	}
	return nil
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// IsMounted returns true if any line in /proc/mounts has mountpoint==path.
func IsMounted(path string) bool {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}
	clean := filepath.Clean(path)
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == clean {
			return true
		}
	}
	return false
}

// Absolute paths verified on ZimaOS 1.6.1 — never rely on $PATH for these.
const (
	mountBin  = "/usr/bin/mount"
	umountBin = "/usr/bin/umount"
)

func doMountNFS(e *Entry) error {
	src := fmt.Sprintf("%s:%s", e.Host, e.Share)
	mode := "rw"
	if e.ReadOnly {
		mode = "ro"
	}
	// nosuid,nodev: a remote share must never introduce setuid binaries or
	// device nodes onto the host.
	opts := mode + ",nosuid,nodev,hard,timeo=600,retrans=2,_netdev"
	if e.Options != "" {
		opts = opts + "," + e.Options
	}
	t := "nfs"
	if e.Type == "nfs4" {
		t = "nfs4"
	}
	// "--" terminates option parsing so a hostile src/mountpoint cannot be
	// read as a flag by mount.
	return runCmd(mountBin, "-t", t, "-o", opts, "--", src, e.Mountpoint)
}

func doMountCIFS(e *Entry) error {
	// A unique temp file avoids a fixed-path race and any collision between
	// concurrent mounts. CreateTemp creates the file with mode 0600.
	f, err := os.CreateTemp("/run", "zvmx-cifs-*.creds")
	if err != nil {
		return fmt.Errorf("create creds file: %w", err)
	}
	credPath := f.Name()
	defer os.Remove(credPath)
	content := fmt.Sprintf("username=%s\npassword=%s\n", e.Username, e.Password)
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return fmt.Errorf("write creds: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write creds: %w", err)
	}
	// Belt and braces: CreateTemp already uses 0600, but pin it explicitly.
	if err := os.Chmod(credPath, 0o600); err != nil {
		return fmt.Errorf("chmod creds: %w", err)
	}

	src := fmt.Sprintf("//%s/%s", e.Host, strings.TrimPrefix(e.Share, "/"))
	opts := fmt.Sprintf("credentials=%s,uid=0,gid=0,iocharset=utf8,vers=3.0,nosuid,nodev,_netdev", credPath)
	if e.ReadOnly {
		opts += ",ro"
	}
	if e.Options != "" {
		opts = opts + "," + e.Options
	}
	// "--" terminates option parsing so a hostile src/mountpoint cannot be
	// read as a flag by mount.
	return runCmd(mountBin, "-t", "cifs", "-o", opts, "--", src, e.Mountpoint)
}

func runCmd(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return fmt.Errorf("%s %v: %v (%s)", name, args, err, msg)
	}
	return nil
}
