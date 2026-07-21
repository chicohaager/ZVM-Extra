// Package handlers contains the HTTP routing and JSON glue between
// the Web UI and the underlying virsh / virt_management calls.
package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chicohaager/zima-vm-extras/internal/autostart"
	"github.com/chicohaager/zima-vm-extras/internal/backup"
	"github.com/chicohaager/zima-vm-extras/internal/buildinfo"
	"github.com/chicohaager/zima-vm-extras/internal/mounts"
	"github.com/chicohaager/zima-vm-extras/internal/pci"
	"github.com/chicohaager/zima-vm-extras/internal/schedule"
	"github.com/chicohaager/zima-vm-extras/internal/storage"
	"github.com/chicohaager/zima-vm-extras/internal/usb"
	"github.com/chicohaager/zima-vm-extras/internal/virsh"
	"github.com/chicohaager/zima-vm-extras/internal/vnc"
)

type Server struct {
	Virsh        *virsh.Client
	Auto         *autostart.Store
	Mounts       *mounts.Manager
	USB          *usb.Store
	PCI          *pci.Store
	Sched        *schedule.Store
	Backup       *backup.Manager
	VNC          *vnc.Store
	SnapshotRoot string // base dir for external-snapshot files, e.g. /DATA/AppData/zima-vm-extras/snapshots
}

func NewServer(v *virsh.Client, st *autostart.Store, mm *mounts.Manager, us *usb.Store, pc *pci.Store, sc *schedule.Store, bk *backup.Manager, vn *vnc.Store, snapshotRoot string) *Server {
	return &Server{Virsh: v, Auto: st, Mounts: mm, USB: us, PCI: pc, Sched: sc, Backup: bk, VNC: vn, SnapshotRoot: snapshotRoot}
}

// Routes returns a mux with all API endpoints mounted under /api.
// No CORS: the daemon binds 127.0.0.1 only and the UI reaches it
// same-origin through the ZimaOS gateway, so cross-origin never applies.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.health)
	mux.HandleFunc("/api/vms", s.listVMs)
	mux.HandleFunc("/api/autostart", s.autostartCollection)
	mux.HandleFunc("/api/autostart/", s.autostartItem)
	mux.HandleFunc("/api/snapshot/", s.snapshotHandler)
	mux.HandleFunc("/api/usb/host", s.usbHost)
	mux.HandleFunc("/api/usb/", s.usbDomain)
	mux.HandleFunc("/api/pci/host", s.pciHost)
	mux.HandleFunc("/api/pci/", s.pciDomain)
	mux.HandleFunc("/api/storage/targets", s.storageTargets)
	mux.HandleFunc("/api/metrics/", s.metricsHandler)
	mux.HandleFunc("/api/schedule", s.scheduleCollection)
	mux.HandleFunc("/api/schedule/", s.scheduleItem)
	mux.HandleFunc("/api/backup", s.backupCollection)
	mux.HandleFunc("/api/backup/", s.backupItem)
	mux.HandleFunc("/api/net/networks", s.netNetworks)
	mux.HandleFunc("/api/net/", s.netDomain)
	mux.HandleFunc("/api/vnc/", s.vncDomain)
	mux.HandleFunc("/api/mounts", s.mountsCollection)
	mux.HandleFunc("/api/mounts/", s.mountsItem)
	return mux
}

// ---------- Remote mounts ----------

func (s *Server) mountsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		writeJSON(w, 200, map[string]any{"data": s.Mounts.List()})
	case "POST":
		var in mounts.Entry
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		e, err := s.Mounts.Upsert(in)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		// If AutoMount and not yet mounted, mount immediately.
		if e.AutoMount && !mounts.IsMounted(e.Mountpoint) {
			if mErr := s.Mounts.Mount(e.ID); mErr != nil {
				// Created the entry, but mount failed. Return the entry plus mount error.
				v, _ := s.Mounts.Get(e.ID)
				writeJSON(w, 207, map[string]any{"entry": v, "mount_error": mErr.Error()})
				return
			}
		}
		v, _ := s.Mounts.Get(e.ID)
		writeJSON(w, 201, v)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) mountsItem(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/api/mounts/")
	parts := strings.Split(strings.TrimSuffix(tail, "/"), "/")
	if parts[0] == "" {
		writeErr(w, 400, "id required")
		return
	}
	id := parts[0]

	// /<id>/mount or /<id>/unmount
	if len(parts) >= 2 {
		switch parts[1] {
		case "mount":
			if r.Method != "POST" {
				writeErr(w, 405, "method not allowed")
				return
			}
			if err := s.Mounts.Mount(id); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			v, _ := s.Mounts.Get(id)
			writeJSON(w, 200, v)
			return
		case "unmount":
			if r.Method != "POST" {
				writeErr(w, 405, "method not allowed")
				return
			}
			if err := s.Mounts.Unmount(id); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			v, _ := s.Mounts.Get(id)
			writeJSON(w, 200, v)
			return
		default:
			writeErr(w, 404, "unknown action")
			return
		}
	}

	switch r.Method {
	case "GET":
		v, ok := s.Mounts.Get(id)
		if !ok {
			writeErr(w, 404, "not found")
			return
		}
		writeJSON(w, 200, v)
	case "PUT":
		// PUT updates an existing entry — it must not create one.
		if _, ok := s.Mounts.Get(id); !ok {
			writeErr(w, 404, "not found")
			return
		}
		var in mounts.Entry
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		in.ID = id
		_, err := s.Mounts.Upsert(in)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		v, _ := s.Mounts.Get(id)
		writeJSON(w, 200, v)
	case "DELETE":
		// Best-effort unmount before delete.
		_ = s.Mounts.Unmount(id)
		if err := s.Mounts.Delete(id); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) storageTargets(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	list, err := storage.List()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"data": list})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// validName guards VM and snapshot names that are passed to virsh as CLI
// arguments — rejecting flag injection (e.g. a name like "--metadata") and
// shell-unsafe characters. virsh is exec'd without a shell, so this is
// defence in depth.
func validName(s string) bool {
	if s == "" || s[0] == '-' {
		return false
	}
	// "." and ".." are valid per the character set below but are path-traversal
	// hazards when a name is used to build a filesystem path.
	if s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '.' || r == '-' || r == '+':
		default:
			return false
		}
	}
	return true
}

// validHexID reports whether s is a 4-digit hex USB vendor/product ID.
func validHexID(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "service": "zima-vm-extras", "version": buildinfo.Version})
}

// vmEntry is the unified view we send to the UI: data merged from
// virsh (authoritative for libvirt state) plus our own autostart store.
type vmEntry struct {
	Name      string `json:"name"`
	Title     string `json:"title"`
	State     string `json:"state"`
	LibvirtAS bool   `json:"libvirt_autostart"` // from virsh dominfo
	Enabled   bool   `json:"enabled"`           // our store
	Order     int    `json:"order"`
	DelayS    int    `json:"delay_s"`
	Watchdog  bool   `json:"watchdog"`
	HasUEFI   bool   `json:"has_uefi"`
}

func (s *Server) listVMs(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	names, err := s.Virsh.ListDomains()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]vmEntry, 0, len(names))
	for _, n := range names {
		e := s.Auto.Get(n)
		state, _ := s.Virsh.State(n)
		las, _ := s.Virsh.IsAutostart(n)
		uefi, _ := s.Virsh.HasUEFI(n)
		title, _ := s.Virsh.Title(n)
		out = append(out, vmEntry{
			Name: n, Title: title, State: state, LibvirtAS: las,
			Enabled: e.Enabled, Order: e.Order, DelayS: e.DelayS,
			Watchdog: e.Watchdog, HasUEFI: uefi,
		})
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

// ---------- Autostart ----------

func (s *Server) autostartCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		writeJSON(w, 200, map[string]any{"data": s.Auto.All()})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) autostartItem(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Path[len("/api/autostart/"):]
	if name == "" {
		writeErr(w, 400, "vm name required")
		return
	}
	if !validName(name) {
		writeErr(w, 400, "invalid vm name")
		return
	}
	switch r.Method {
	case "GET":
		writeJSON(w, 200, s.Auto.Get(name))
	case "PUT":
		var in autostart.Entry
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		in.Name = name
		saved, err := s.Auto.Set(in)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		// VM Extras orchestrates autostart itself (ordered + delayed), so the
		// libvirt-native flag must stay off — otherwise virtqemud would start
		// the VM immediately at boot and defeat the ordering.
		if err := s.Virsh.SetAutostart(name, false); err != nil {
			writeErr(w, 500, "libvirt autostart sync failed: "+err.Error())
			return
		}
		writeJSON(w, 200, saved)
	case "DELETE":
		_ = s.Virsh.SetAutostart(name, false)
		if err := s.Auto.Delete(name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

// ---------- Snapshots ----------

// snapshotHandler routes:
//   GET    /api/snapshot/<vm>                 — list snapshots + capability info
//   POST   /api/snapshot/<vm>                 — create (body: {name, description, external})
//   POST   /api/snapshot/<vm>/<snap>/revert   — revert (body: {force})
//   DELETE /api/snapshot/<vm>/<snap>          — delete (?children=1 for recursive)
func (s *Server) snapshotHandler(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/api/snapshot/")
	parts := strings.Split(strings.TrimSuffix(tail, "/"), "/")
	if parts[0] == "" {
		writeErr(w, 400, "vm name required")
		return
	}
	vm := parts[0]
	if !validName(vm) {
		writeErr(w, 400, "invalid vm name")
		return
	}

	// /<vm>
	if len(parts) == 1 {
		switch r.Method {
		case "GET":
			s.snapshotList(w, vm)
		case "POST":
			s.snapshotCreate(w, r, vm)
		default:
			writeErr(w, 405, "method not allowed")
		}
		return
	}

	// /<vm>/<snap>
	snap := parts[1]
	if snap == "" {
		writeErr(w, 400, "snapshot name required")
		return
	}
	if !validName(snap) {
		writeErr(w, 400, "invalid snapshot name")
		return
	}

	// /<vm>/<snap>/revert
	if len(parts) >= 3 && parts[2] == "revert" {
		if r.Method != "POST" {
			writeErr(w, 405, "method not allowed")
			return
		}
		s.snapshotRevert(w, r, vm, snap)
		return
	}

	// /<vm>/<snap>
	switch r.Method {
	case "DELETE":
		withChildren := r.URL.Query().Get("children") == "1"
		if err := s.Virsh.DeleteSnapshot(vm, snap, withChildren); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

type snapshotsResponse struct {
	Data            []virsh.Snapshot `json:"data"`
	Current         string           `json:"current"`
	HasUEFI         bool             `json:"has_uefi"`
	State           string           `json:"state"`
	ExtRequired     bool             `json:"external_required"`     // hint for UI: must use external?
	DefaultExtDir   string           `json:"default_external_dir"`  // suggested path
}

func (s *Server) snapshotList(w http.ResponseWriter, vm string) {
	list, err := s.Virsh.ListSnapshots(vm)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	cur, _ := s.Virsh.CurrentSnapshot(vm)
	state, _ := s.Virsh.State(vm)
	uefi, _ := s.Virsh.HasUEFI(vm)
	// Running/paused VMs get a full external snapshot (disk + memory);
	// shut-off VMs get a plain internal snapshot.
	extReq := state == "running" || state == "paused"
	writeJSON(w, 200, snapshotsResponse{
		Data: list, Current: cur, HasUEFI: uefi, State: state, ExtRequired: extReq,
		DefaultExtDir: filepath.Join(s.SnapshotRoot, vm),
	})
}

type createSnapshotReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	External    bool   `json:"external"`
	ExternalDir string `json:"external_dir"` // override target dir for external snapshot files
}

func (s *Server) snapshotCreate(w http.ResponseWriter, r *http.Request, vm string) {
	var in createSnapshotReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if in.Name == "" {
		writeErr(w, 400, "snapshot name required")
		return
	}
	// Refuse names with path traversal characters.
	if strings.ContainsAny(in.Name, "/\\.") {
		writeErr(w, 400, "snapshot name must not contain '/', '\\\\' or '.'")
		return
	}
	// Bound the free-text description: it is passed to virsh and stored in
	// libvirt metadata.
	if len(in.Description) > 256 {
		writeErr(w, 400, "description must be 256 characters or fewer")
		return
	}
	if strings.ContainsAny(in.Description, "\n\r\x00") {
		writeErr(w, 400, "description must not contain control characters")
		return
	}

	// The snapshot type is decided by VM state, not by the client: a running
	// or paused VM needs a full external snapshot (disk + memory) to stay
	// revertable; a shut-off VM uses a quick internal snapshot.
	state, _ := s.Virsh.State(vm)
	external := state == "running" || state == "paused"

	extDir := ""
	if external {
		extDir = strings.TrimSpace(in.ExternalDir)
		if extDir == "" {
			extDir = filepath.Join(s.SnapshotRoot, vm)
		}
		// Guardrail: the dir must be absolute and not contain ".." traversal.
		if !filepath.IsAbs(extDir) || strings.Contains(extDir, "..") {
			writeErr(w, 400, "external_dir must be an absolute path without '..'")
			return
		}
		// Refuse pseudo-FS roots that would brick the host or VM image.
		if forbiddenSnapshotDir(extDir) {
			writeErr(w, 400, "external_dir is not allowed (system path)")
			return
		}
		if err := os.MkdirAll(extDir, 0o755); err != nil {
			writeErr(w, 500, "mkdir snapshot dir: "+err.Error())
			return
		}
	}
	if err := s.Virsh.CreateSnapshot(vm, in.Name, in.Description, external, extDir); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"name": in.Name, "external": external, "external_dir": extDir})
}

// forbiddenSnapshotDir returns true if the path targets a system location
// where writing snapshot files would be hostile (rootfs overlay, tmpfs, /proc, /sys).
func forbiddenSnapshotDir(p string) bool {
	clean := filepath.Clean(p)
	forbidden := []string{"/", "/proc", "/sys", "/dev", "/run", "/var", "/usr", "/etc", "/boot", "/tmp"}
	for _, f := range forbidden {
		if clean == f || strings.HasPrefix(clean, f+"/") {
			return true
		}
	}
	return false
}

type revertReq struct {
	Force bool `json:"force"`
}

func (s *Server) snapshotRevert(w http.ResponseWriter, r *http.Request, vm, snap string) {
	var in revertReq
	_ = json.NewDecoder(r.Body).Decode(&in)
	if err := s.Virsh.RevertSnapshot(vm, snap, in.Force); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"reverted": true})
}

// ---------- USB passthrough ----------

// usbHost: GET /api/usb/host — list host USB devices.
func (s *Server) usbHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	devs, err := usb.HostList("")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"data": devs})
}

// usbDomain routes:
//   GET    /api/usb/<vm>/pinned                — list pinned devices of a VM
//   POST   /api/usb/<vm>                       — attach (body: {vendor_id, product_id, persistent, description})
//   DELETE /api/usb/<vm>/<vendor>:<product>    — detach
func (s *Server) usbDomain(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/api/usb/")
	if tail == "host" {
		s.usbHost(w, r) // defensive: already routed above
		return
	}
	parts := strings.Split(strings.TrimSuffix(tail, "/"), "/")
	if parts[0] == "" {
		writeErr(w, 400, "vm name required")
		return
	}
	vm := parts[0]
	if !validName(vm) {
		writeErr(w, 400, "invalid vm name")
		return
	}

	// GET /<vm>/pinned — devices pinned (persistently passed through) to this VM.
	if len(parts) == 2 && parts[1] == "pinned" && r.Method == "GET" {
		writeJSON(w, 200, map[string]any{"data": s.USB.ForVM(vm)})
		return
	}

	// POST /<vm> — attach a device.
	if len(parts) == 1 && r.Method == "POST" {
		var in struct {
			VendorID    string `json:"vendor_id"`
			ProductID   string `json:"product_id"`
			Persistent  bool   `json:"persistent"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if !validHexID(in.VendorID) || !validHexID(in.ProductID) {
			writeErr(w, 400, "vendor_id and product_id must be 4 hex digits")
			return
		}
		if usb.IsRootHub(in.VendorID) {
			writeErr(w, 400, "USB root hubs (vendor 1d6b) cannot be passed through to a guest")
			return
		}
		if err := s.Virsh.AttachDeviceXML(vm, usb.HostdevXML(in.VendorID, in.ProductID), in.Persistent); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		// A persistent attach also pins the device, so the reconciler keeps
		// it in the domain config across ZVM re-saves and host reboots.
		if in.Persistent {
			if err := s.USB.Add(usb.PinnedDevice{
				VM: vm, VendorID: in.VendorID, ProductID: in.ProductID, Description: in.Description,
			}); err != nil {
				writeErr(w, 500, "attached, but pinning failed: "+err.Error())
				return
			}
		}
		writeJSON(w, 201, map[string]any{
			"attached": true, "pinned": in.Persistent,
			"vendor_id": in.VendorID, "product_id": in.ProductID,
		})
		return
	}

	// DELETE /<vm>/<vendor>:<product> — detach a device.
	if len(parts) == 2 && r.Method == "DELETE" {
		idParts := strings.SplitN(parts[1], ":", 2)
		if len(idParts) != 2 || !validHexID(idParts[0]) || !validHexID(idParts[1]) {
			writeErr(w, 400, "expected <vendor>:<product> as 4 hex digits each")
			return
		}
		// Unpin first so the reconciler will not immediately re-add it.
		_ = s.USB.Remove(vm, idParts[0], idParts[1])
		if err := s.Virsh.DetachDeviceXML(vm, usb.HostdevXML(idParts[0], idParts[1]), true); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"detached": true})
		return
	}

	writeErr(w, 405, "method not allowed")
}

// ---------- PCIe / VFIO passthrough ----------

// pciHost: GET /api/pci/host — list host PCI devices with IOMMU groups.
func (s *Server) pciHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	devs, err := pci.HostList("")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"data": devs})
}

// pciDomain routes:
//   GET    /api/pci/<vm>/pinned    — list pinned PCI devices of a VM
//   POST   /api/pci/<vm>           — attach (body: {address, persistent, description})
//   DELETE /api/pci/<vm>/<address> — detach
func (s *Server) pciDomain(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/api/pci/")
	if tail == "host" {
		s.pciHost(w, r) // defensive: already routed above
		return
	}
	parts := strings.Split(strings.TrimSuffix(tail, "/"), "/")
	if parts[0] == "" {
		writeErr(w, 400, "vm name required")
		return
	}
	vm := parts[0]
	if !validName(vm) {
		writeErr(w, 400, "invalid vm name")
		return
	}

	// GET /<vm>/pinned — devices pinned (persistently passed through) to this VM.
	if len(parts) == 2 && parts[1] == "pinned" && r.Method == "GET" {
		writeJSON(w, 200, map[string]any{"data": s.PCI.ForVM(vm)})
		return
	}

	// POST /<vm> — attach a device.
	if len(parts) == 1 && r.Method == "POST" {
		var in struct {
			Address     string `json:"address"`
			Persistent  bool   `json:"persistent"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if !pci.ValidAddress(in.Address) {
			writeErr(w, 400, "address must be a PCI address like 0000:00:02.0")
			return
		}
		xmlSnip, err := pci.HostdevXML(in.Address)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if err := s.Virsh.AttachDeviceXML(vm, xmlSnip, in.Persistent); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if in.Persistent {
			if err := s.PCI.Add(pci.PinnedDevice{
				VM: vm, Address: in.Address, Description: in.Description,
			}); err != nil {
				writeErr(w, 500, "attached, but pinning failed: "+err.Error())
				return
			}
		}
		writeJSON(w, 201, map[string]any{"attached": true, "pinned": in.Persistent, "address": in.Address})
		return
	}

	// DELETE /<vm>/<address> — detach a device.
	if len(parts) == 2 && r.Method == "DELETE" {
		addr := parts[1]
		if !pci.ValidAddress(addr) {
			writeErr(w, 400, "invalid pci address")
			return
		}
		xmlSnip, err := pci.HostdevXML(addr)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		// Unpin first so the reconciler will not immediately re-add it.
		_ = s.PCI.Remove(vm, addr)
		if err := s.Virsh.DetachDeviceXML(vm, xmlSnip, true); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"detached": true})
		return
	}

	writeErr(w, 405, "method not allowed")
}

// ---------- Live metrics ----------

type netStat struct {
	Name    string `json:"name"`
	RxBytes uint64 `json:"rx_bytes"`
	TxBytes uint64 `json:"tx_bytes"`
}

type blockStat struct {
	Name    string `json:"name"`
	RdBytes uint64 `json:"rd_bytes"`
	WrBytes uint64 `json:"wr_bytes"`
}

type metricsResponse struct {
	VM        string      `json:"vm"`
	State     string      `json:"state"`
	TSMillis  int64       `json:"ts_ms"`       // server timestamp for rate calculation
	CPUTimeNS uint64      `json:"cpu_time_ns"` // cumulative
	VCPUs     int         `json:"vcpus"`
	MemCurKiB uint64      `json:"mem_cur_kib"`
	MemMaxKiB uint64      `json:"mem_max_kib"`
	Nets      []netStat   `json:"nets"`
	Blocks    []blockStat `json:"blocks"`
}

// metricsHandler: GET /api/metrics/<vm> — one libvirt domstats sample.
// Counters are cumulative; the UI polls and computes rates from deltas.
func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	vm := strings.TrimPrefix(r.URL.Path, "/api/metrics/")
	if vm == "" || !validName(vm) {
		writeErr(w, 400, "invalid vm name")
		return
	}
	stats, err := s.Virsh.DomainStats(vm)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := metricsResponse{
		VM:        vm,
		State:     domStateName(stats["state.state"]),
		TSMillis:  time.Now().UnixMilli(),
		CPUTimeNS: atou(stats["cpu.time"]),
		VCPUs:     int(atou(stats["vcpu.current"])),
		MemCurKiB: atou(stats["balloon.current"]),
		MemMaxKiB: atou(stats["balloon.maximum"]),
	}
	for i := 0; i < int(atou(stats["net.count"])); i++ {
		p := fmt.Sprintf("net.%d.", i)
		out.Nets = append(out.Nets, netStat{
			Name:    stats[p+"name"],
			RxBytes: atou(stats[p+"rx.bytes"]),
			TxBytes: atou(stats[p+"tx.bytes"]),
		})
	}
	for i := 0; i < int(atou(stats["block.count"])); i++ {
		p := fmt.Sprintf("block.%d.", i)
		out.Blocks = append(out.Blocks, blockStat{
			Name:    stats[p+"name"],
			RdBytes: atou(stats[p+"rd.bytes"]),
			WrBytes: atou(stats[p+"wr.bytes"]),
		})
	}
	writeJSON(w, 200, out)
}

func atou(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return n
}

// domStateName maps the libvirt numeric domain state to a label.
func domStateName(code string) string {
	switch code {
	case "1":
		return "running"
	case "3":
		return "paused"
	case "4":
		return "shutdown"
	case "5":
		return "shut off"
	case "6":
		return "crashed"
	case "7":
		return "suspended"
	default:
		return "unknown"
	}
}

// ---------- Snapshot schedules ----------

func (s *Server) scheduleCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	writeJSON(w, 200, map[string]any{"data": s.Sched.List()})
}

// scheduleItem: GET/PUT/DELETE /api/schedule/<vm> — per-VM snapshot schedule.
func (s *Server) scheduleItem(w http.ResponseWriter, r *http.Request) {
	vm := strings.TrimPrefix(r.URL.Path, "/api/schedule/")
	if vm == "" || !validName(vm) {
		writeErr(w, 400, "invalid vm name")
		return
	}
	switch r.Method {
	case "GET":
		writeJSON(w, 200, s.Sched.Get(vm))
	case "PUT":
		var in schedule.Entry
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		in.VM = vm
		saved, err := s.Sched.Upsert(in)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, saved)
	case "DELETE":
		if err := s.Sched.Delete(vm); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

// ---------- Backup / export ----------

func (s *Server) backupCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	writeJSON(w, 200, map[string]any{"data": s.Backup.List()})
}

// backupItem: POST /api/backup/<vm> — start an async backup (body: {dest_dir}).
func (s *Server) backupItem(w http.ResponseWriter, r *http.Request) {
	vm := strings.TrimPrefix(r.URL.Path, "/api/backup/")
	if vm == "" || !validName(vm) {
		writeErr(w, 400, "invalid vm name")
		return
	}
	if r.Method != "POST" {
		writeErr(w, 405, "method not allowed")
		return
	}
	var in struct {
		DestDir string `json:"dest_dir"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	dest := strings.TrimSpace(in.DestDir)
	if dest == "" {
		dest = filepath.Join(filepath.Dir(s.SnapshotRoot), "backups")
	}
	if !filepath.IsAbs(dest) || strings.Contains(dest, "..") {
		writeErr(w, 400, "dest_dir must be an absolute path without '..'")
		return
	}
	if forbiddenSnapshotDir(dest) {
		writeErr(w, 400, "dest_dir is not allowed (system path)")
		return
	}
	job, err := s.Backup.Start(vm, dest)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, job)
}

// ---------- Networking ----------

func validMAC(s string) bool {
	if len(s) != 17 {
		return false
	}
	for i, r := range s {
		if (i+1)%3 == 0 {
			if r != ':' {
				return false
			}
		} else if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func validNICModel(m string) bool {
	switch m {
	case "virtio", "e1000", "e1000e", "rtl8139":
		return true
	}
	return false
}

func (s *Server) netNetworks(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	nets, err := s.Virsh.NetworkList()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"data": nets})
}

// netDomain routes:
//   GET /api/net/<vm>        — list the VM's NICs
//   PUT /api/net/<vm>/<mac>  — switch a NIC to a libvirt network (body: {network, model})
//
// Switching only ever targets an existing libvirt network and only edits the
// persistent config — it never creates host bridges, never changes a running
// VM live, and so cannot take the host off the network.
func (s *Server) netDomain(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/api/net/")
	if tail == "networks" {
		s.netNetworks(w, r) // defensive: already routed above
		return
	}
	parts := strings.Split(strings.TrimSuffix(tail, "/"), "/")
	if parts[0] == "" {
		writeErr(w, 400, "vm name required")
		return
	}
	vm := parts[0]
	if !validName(vm) {
		writeErr(w, 400, "invalid vm name")
		return
	}

	// GET /<vm> — the VM's NICs.
	if len(parts) == 1 && r.Method == "GET" {
		ifaces, err := s.Virsh.DomainInterfaces(vm)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"data": ifaces})
		return
	}

	// PUT /<vm>/<mac> — switch a NIC.
	if len(parts) == 2 && r.Method == "PUT" {
		mac := strings.ToLower(parts[1])
		if !validMAC(mac) {
			writeErr(w, 400, "invalid mac address")
			return
		}
		var in struct {
			Network string `json:"network"`
			Model   string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if !validName(in.Network) {
			writeErr(w, 400, "invalid network name")
			return
		}
		if in.Model != "" && !validNICModel(in.Model) {
			writeErr(w, 400, "model must be one of virtio, e1000, e1000e, rtl8139")
			return
		}
		// The target must be a real libvirt network.
		nets, err := s.Virsh.NetworkList()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		known := false
		for _, n := range nets {
			if n.Name == in.Network {
				known = true
				break
			}
		}
		if !known {
			writeErr(w, 400, "unknown libvirt network: "+in.Network)
			return
		}
		// Find the NIC's current type — detach-interface needs it.
		ifaces, err := s.Virsh.DomainInterfaces(vm)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		oldType := ""
		for _, i := range ifaces {
			if strings.EqualFold(i.MAC, mac) {
				oldType = i.Type
				break
			}
		}
		if oldType == "" {
			writeErr(w, 404, "no NIC with that mac on this VM")
			return
		}
		if err := s.Virsh.SwitchInterface(vm, oldType, mac, in.Network, in.Model); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"updated": true, "note": "takes effect on next VM start"})
		return
	}

	writeErr(w, 405, "method not allowed")
}

// ---------- VNC console password ----------

// vncDomain routes the per-VM VNC console password:
//
//	GET    /api/vnc/<vm>  — status {present, protected, listen, pinned}
//	POST   /api/vnc/<vm>  — set the password (body: {password}) and pin it
//	DELETE /api/vnc/<vm>  — clear the password and unpin
//
// Setting a password only edits the persistent domain config, so it never
// disturbs a running VM; it takes effect on the next VM start. A persistent
// set also pins the password so the reconciler re-applies it whenever the
// official ZVM UI strips it on a re-save.
func (s *Server) vncDomain(w http.ResponseWriter, r *http.Request) {
	vm := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/vnc/"), "/")
	if vm == "" {
		writeErr(w, 400, "vm name required")
		return
	}
	if !validName(vm) {
		writeErr(w, 400, "invalid vm name")
		return
	}

	switch r.Method {
	case "GET":
		present, hasPw, listen, err := s.Virsh.VNCInfo(vm)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		_, pinned := s.VNC.Get(vm)
		writeJSON(w, 200, map[string]any{
			"vm": vm, "present": present, "protected": hasPw,
			"listen": listen, "pinned": pinned,
		})

	case "POST":
		var in struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if !vnc.ValidPassword(in.Password) {
			writeErr(w, 400, "password must be 1–8 characters, printable ASCII, without quotes or <>&")
			return
		}
		if err := s.Virsh.SetVNCPassword(vm, in.Password); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		// Pin it so the reconciler keeps it applied across ZVM re-saves.
		if err := s.VNC.Set(vnc.Entry{VM: vm, Password: in.Password}); err != nil {
			writeErr(w, 500, "password set, but pinning failed: "+err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"protected": true, "note": "takes effect on next VM start"})

	case "DELETE":
		// Unpin first so the reconciler will not immediately re-apply it.
		_ = s.VNC.Remove(vm)
		if err := s.Virsh.SetVNCPassword(vm, ""); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"cleared": true})

	default:
		writeErr(w, 405, "method not allowed")
	}
}
