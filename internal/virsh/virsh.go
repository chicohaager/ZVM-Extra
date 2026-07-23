// Package virsh wraps the libvirt `virsh` CLI as a subprocess.
// Rationale: pure-Go, no cgo, no libvirt-dev headers needed at build time,
// and a stable interface that survives libvirt API churn.
package virsh

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Client is a minimal virsh wrapper. Zero value uses /usr/bin/virsh and a 10s timeout.
type Client struct {
	Bin     string        // path to virsh, default "/usr/bin/virsh"
	Timeout time.Duration // per-call timeout, default 10s
}

// New returns a Client with sensible defaults. binPath may be empty for default.
func New(binPath string) *Client {
	c := &Client{Bin: binPath, Timeout: 10 * time.Second}
	if c.Bin == "" {
		c.Bin = "/usr/bin/virsh"
	}
	return c
}

// run executes virsh with the given args and returns stdout. Errors include stderr.
func (c *Client) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	// Force the C locale so virsh table/field output is parsed deterministically.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("virsh %v: %w (stderr: %s)", args, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// ListDomains returns the names of all defined domains (running and stopped).
func (c *Client) ListDomains() ([]string, error) {
	out, err := c.run("list", "--all", "--name")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// IsAutostart returns whether the domain is configured to autostart.
func (c *Client) IsAutostart(name string) (bool, error) {
	out, err := c.run("dominfo", name)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Autostart:") {
			return strings.Contains(line, "enable"), nil
		}
	}
	return false, fmt.Errorf("could not parse Autostart for %s", name)
}

// SetAutostart enables/disables libvirt-native autostart.
func (c *Client) SetAutostart(name string, enable bool) error {
	args := []string{"autostart", name}
	if !enable {
		args = append(args, "--disable")
	}
	_, err := c.run(args...)
	return err
}

// State returns the current state ("running", "shut off", "paused", etc).
func (c *Client) State(name string) (string, error) {
	out, err := c.run("domstate", name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Start starts a stopped domain.
func (c *Client) Start(name string) error {
	_, err := c.run("start", name)
	return err
}

// Shutdown sends an ACPI shutdown to the domain. The guest decides when (and
// whether) to act on it — a guest without ACPI support simply ignores it.
func (c *Client) Shutdown(name string) error {
	_, err := c.run("shutdown", name)
	return err
}

// Reboot sends an ACPI reboot request. Same caveat as Shutdown.
func (c *Client) Reboot(name string) error {
	_, err := c.run("reboot", name)
	return err
}

// Destroy cuts the domain's power immediately. This is the equivalent of
// pulling the plug: the guest gets no chance to flush its filesystems.
func (c *Client) Destroy(name string) error {
	_, err := c.run("destroy", name)
	return err
}

// Suspend freezes a running domain in RAM.
func (c *Client) Suspend(name string) error {
	_, err := c.run("suspend", name)
	return err
}

// Resume unfreezes a suspended domain.
func (c *Client) Resume(name string) error {
	_, err := c.run("resume", name)
	return err
}

// SetMemStatsPeriod enables periodic collection of the guest's virtio-balloon
// memory statistics, in seconds (0 disables).
//
// Without this, `domstats` reports only balloon.current and balloon.maximum —
// and balloon.current is the balloon's *target* size, which equals the
// configured maximum whenever the balloon is not inflated. Reading that pair
// as "used / total" makes every VM look like it is at 100 % memory, which is
// exactly what v0.6.3 displayed. The in-guest figures (balloon.available,
// balloon.unused) only appear once a collection period is set.
//
// --live only: nothing is written to the persistent domain config.
func (c *Client) SetMemStatsPeriod(domain string, seconds int) error {
	_, err := c.run("dommemstat", domain, "--period", strconv.Itoa(seconds), "--live")
	return err
}

// DumpXML returns the persistent XML configuration.
func (c *Client) DumpXML(name string) (string, error) {
	return c.run("dumpxml", name)
}

// ---------- Snapshot operations ----------

// Snapshot is a single libvirt snapshot record.
type Snapshot struct {
	Name        string `json:"name"`
	CreatedAt   string `json:"created_at"`           // ISO from snapshot-info
	State       string `json:"state,omitempty"`      // "running" | "shutoff" | "disk-snapshot" | ...
	Parent      string `json:"parent,omitempty"`     // empty if root
	Description string `json:"description,omitempty"`
	Current     bool   `json:"current"`              // is HEAD
}

// ListSnapshots returns all snapshots of a domain, newest first.
// Uses --tree-less plain listing for simpler parsing.
func (c *Client) ListSnapshots(domain string) ([]Snapshot, error) {
	out, err := c.run("snapshot-list", domain, "--parent")
	if err != nil {
		return nil, err
	}
	// Output is a column table with a "---" separator on line 2.
	lines := strings.Split(out, "\n")
	var snaps []Snapshot
	sep := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "---") {
			sep = true
			continue
		}
		if !sep {
			continue
		}
		// Columns: Name | Creation Time | State | Parent
		fields := splitColumns(line, 4)
		if len(fields) < 3 || fields[0] == "" {
			continue
		}
		s := Snapshot{Name: fields[0], CreatedAt: fields[1], State: fields[2]}
		if len(fields) >= 4 {
			s.Parent = fields[3]
		}
		snaps = append(snaps, s)
	}
	// Mark current
	if cur, err := c.CurrentSnapshot(domain); err == nil && cur != "" {
		for i := range snaps {
			if snaps[i].Name == cur {
				snaps[i].Current = true
			}
		}
	}
	return snaps, nil
}

// splitColumns splits a virsh table row into n string columns using runs of
// 2+ spaces as separator. Trims each.
func splitColumns(line string, n int) []string {
	out := make([]string, 0, n)
	for _, part := range regexpSplitMulti(line) {
		p := strings.TrimSpace(part)
		out = append(out, p)
	}
	return out
}

// regexpSplitMulti splits on runs of 2+ whitespace without importing regexp.
func regexpSplitMulti(s string) []string {
	var out []string
	var cur strings.Builder
	spaces := 0
	for _, r := range s {
		if r == ' ' || r == '\t' {
			spaces++
			if spaces >= 2 && cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			continue
		}
		if spaces > 0 && cur.Len() > 0 {
			cur.WriteByte(' ')
		}
		spaces = 0
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// CurrentSnapshot returns the name of the current snapshot, or "" if none.
func (c *Client) CurrentSnapshot(domain string) (string, error) {
	out, err := c.run("snapshot-current", domain, "--name")
	if err != nil {
		// virsh returns non-zero if no current snapshot — treat as empty.
		if strings.Contains(err.Error(), "has no current snapshot") {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CreateSnapshot makes a revertable snapshot.
//
// external=false: a plain internal snapshot (stored inside the qcow2) — used
// for shut-off VMs.
//
// external=true: external disk overlays under externalDir. For a running or
// paused VM a memory-state file is added too (--memspec), making it a full
// system checkpoint. This matters: a --disk-only external snapshot has state
// 'disk-snapshot' and libvirt *refuses* to revert to it ("Invalid target
// domain state 'disk-snapshot'"). Saving the memory state gives the snapshot
// a real machine state, so `snapshot-revert` works.
func (c *Client) CreateSnapshot(domain, name, description string, external bool, externalDir string) error {
	args := []string{"snapshot-create-as", domain, name}
	if description != "" {
		args = append(args, "--description", description)
	}
	if !external {
		_, err := c.run(args...)
		return err
	}

	if externalDir == "" {
		return fmt.Errorf("externalDir required for external snapshot")
	}
	disks, err := c.DomainDisks(domain)
	if err != nil {
		return err
	}
	stem := fileStem(name)
	for i, d := range disks {
		// Read-only removable media must not be snapshotted — a CD-ROM /
		// floppy overlay is useless and can make the operation fail.
		if d.Device == "cdrom" || d.Device == "floppy" {
			args = append(args, "--diskspec", d.Target+",snapshot=no")
			continue
		}
		path := fmt.Sprintf("%s/%s-%s-%d.qcow2", strings.TrimRight(externalDir, "/"), stem, d.Target, i)
		args = append(args, "--diskspec",
			fmt.Sprintf("%s,snapshot=external,file=%s", d.Target, path))
	}
	if state, _ := c.State(domain); state == "running" || state == "paused" {
		mem := fmt.Sprintf("%s/%s-memory.save", strings.TrimRight(externalDir, "/"), stem)
		args = append(args, "--memspec", "file="+mem+",snapshot=external")
	}
	args = append(args, "--atomic")
	_, err = c.run(args...)
	return err
}

// fileStem turns a snapshot name into a filename component. libvirt is happy
// with spaces in a snapshot name, so the name and the overlay filename are
// kept as two separate things: the snapshot keeps what the user typed, the
// files on disk get a boring stem. Anything outside [A-Za-z0-9._-] becomes an
// underscore, consecutive replacements collapse, and an empty result falls
// back to "snap".
func fileStem(name string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	s := strings.Trim(b.String(), "_.")
	if s == "" {
		return "snap"
	}
	return s
}

// RevertSnapshot reverts to a snapshot. If force=true, allows reverting from
// a state that libvirt considers risky (e.g. memory state mismatch).
func (c *Client) RevertSnapshot(domain, snapshot string, force bool) error {
	args := []string{"snapshot-revert", domain, snapshot}
	if force {
		args = append(args, "--force")
	}
	_, err := c.run(args...)
	return err
}

// DeleteSnapshot removes a snapshot. If withChildren=true, recursive delete.
func (c *Client) DeleteSnapshot(domain, snapshot string, withChildren bool) error {
	args := []string{"snapshot-delete", domain, snapshot}
	if withChildren {
		args = append(args, "--children")
	}
	_, err := c.run(args...)
	return err
}

// ---------- Domain disk / firmware introspection ----------

// Disk describes one entry from `virsh domblklist --details`.
type Disk struct {
	Type   string `json:"type"`   // "file" | "block"
	Device string `json:"device"` // "disk" | "cdrom" | "floppy"
	Target string `json:"target"` // e.g. "vda", "sda", "hdc"
	Source string `json:"source"` // backing file path
}

// DomainDisks parses `virsh domblklist --details <name>`. Entries with no
// source (e.g. an empty CD-ROM tray) are omitted. The Device field lets
// callers tell real disks apart from removable media.
func (c *Client) DomainDisks(domain string) ([]Disk, error) {
	out, err := c.run("domblklist", "--details", domain)
	if err != nil {
		return nil, err
	}
	var disks []Disk
	sep := false
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "---") {
			sep = true
			continue
		}
		if !sep {
			continue
		}
		fields := splitColumns(line, 4)
		if len(fields) < 4 || fields[2] == "" {
			continue
		}
		if fields[3] == "-" { // unbacked (empty tray) — nothing to snapshot
			continue
		}
		disks = append(disks, Disk{
			Type: fields[0], Device: fields[1], Target: fields[2], Source: fields[3],
		})
	}
	return disks, nil
}

// DomainStats returns a flat key=value map of `virsh domstats <name>`
// (cpu.time, balloon.current, net.N.*, block.N.* …). Counters are
// cumulative; callers compute rates from two samples over time.
func (c *Client) DomainStats(domain string) (map[string]string, error) {
	out, err := c.run("domstats", domain)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return m, nil
}

// HasUEFI returns true if the domain uses pflash (UEFI / OVMF). Such VMs
// can't take internal snapshots while running.
func (c *Client) HasUEFI(domain string) (bool, error) {
	xml, err := c.DumpXML(domain)
	if err != nil {
		return false, err
	}
	return strings.Contains(xml, "<loader") && strings.Contains(xml, "pflash"), nil
}

// Title returns the libvirt <title> element (human-readable name), or "" if absent.
func (c *Client) Title(domain string) (string, error) {
	xml, err := c.DumpXML(domain)
	if err != nil {
		return "", err
	}
	open := strings.Index(xml, "<title>")
	close := strings.Index(xml, "</title>")
	if open < 0 || close < 0 || close <= open {
		return "", nil
	}
	return strings.TrimSpace(xml[open+len("<title>") : close]), nil
}

// ---------- USB hostdev introspection ----------

// HasUSBHostdev reports whether the domain's persistent (inactive) config
// already declares a USB <hostdev> for the given vendor/product pair. The
// persistent config is what determines survival across VM reboots, so that
// is what the USB reconciler checks and repairs.
func (c *Client) HasUSBHostdev(domain, vendorID, productID string) (bool, error) {
	out, err := c.run("dumpxml", "--inactive", domain)
	if err != nil {
		return false, err
	}
	return hasUSBHostdev(out, vendorID, productID), nil
}

// hasUSBHostdev parses domain XML and looks for a matching USB hostdev.
func hasUSBHostdev(domainXML, vendorID, productID string) bool {
	type idAttr struct {
		ID string `xml:"id,attr"`
	}
	var doc struct {
		Hostdevs []struct {
			Type   string `xml:"type,attr"`
			Source struct {
				Vendor  idAttr `xml:"vendor"`
				Product idAttr `xml:"product"`
			} `xml:"source"`
		} `xml:"devices>hostdev"`
	}
	if err := xml.Unmarshal([]byte(domainXML), &doc); err != nil {
		return false
	}
	wantV := "0x" + strings.ToLower(vendorID)
	wantP := "0x" + strings.ToLower(productID)
	for _, h := range doc.Hostdevs {
		if h.Type != "usb" {
			continue
		}
		if strings.ToLower(h.Source.Vendor.ID) == wantV &&
			strings.ToLower(h.Source.Product.ID) == wantP {
			return true
		}
	}
	return false
}

// AttachDeviceConfig attaches an XML-described device to the domain's
// persistent config only (--config), leaving the live domain untouched.
// Used by the USB reconciler to repair config without disturbing a running VM.
func (c *Client) AttachDeviceConfig(domain, xmlSnippet string) error {
	tmp, err := writeTempXML(xmlSnippet)
	if err != nil {
		return err
	}
	defer removeTemp(tmp)
	_, err = c.run("attach-device", domain, tmp, "--config")
	return err
}

// ---------- PCI hostdev introspection ----------

// HasPCIHostdev reports whether the domain's persistent (inactive) config
// declares a PCI <hostdev> for the given PCI address ("0000:00:02.0").
func (c *Client) HasPCIHostdev(domain, address string) (bool, error) {
	out, err := c.run("dumpxml", "--inactive", domain)
	if err != nil {
		return false, err
	}
	return hasPCIHostdev(out, address), nil
}

func hasPCIHostdev(domainXML, address string) bool {
	wd, wb, ws, wf, ok := parsePCIAddr(address)
	if !ok {
		return false
	}
	var doc struct {
		Hostdevs []struct {
			Type   string `xml:"type,attr"`
			Source struct {
				Address struct {
					Domain   string `xml:"domain,attr"`
					Bus      string `xml:"bus,attr"`
					Slot     string `xml:"slot,attr"`
					Function string `xml:"function,attr"`
				} `xml:"address"`
			} `xml:"source"`
		} `xml:"devices>hostdev"`
	}
	if err := xml.Unmarshal([]byte(domainXML), &doc); err != nil {
		return false
	}
	for _, h := range doc.Hostdevs {
		if h.Type != "pci" {
			continue
		}
		a := h.Source.Address
		if parseHexU(a.Domain) == wd && parseHexU(a.Bus) == wb &&
			parseHexU(a.Slot) == ws && parseHexU(a.Function) == wf {
			return true
		}
	}
	return false
}

// parsePCIAddr splits "0000:00:02.0" into numeric domain/bus/slot/function.
func parsePCIAddr(addr string) (dom, bus, slot, fn uint64, ok bool) {
	d, rest, ok1 := strings.Cut(addr, ":")
	b, sf, ok2 := strings.Cut(rest, ":")
	s, f, ok3 := strings.Cut(sf, ".")
	if !ok1 || !ok2 || !ok3 {
		return 0, 0, 0, 0, false
	}
	return parseHexU(d), parseHexU(b), parseHexU(s), parseHexU(f), true
}

// parseHexU parses a hex string, tolerating an optional "0x" prefix.
func parseHexU(s string) uint64 {
	s = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "0x")
	n, _ := strconv.ParseUint(s, 16, 64)
	return n
}

// ---------- Generic attach/detach for hostdev XML ----------

// AttachDeviceXML attaches a device described by an XML snippet.
// If persistent, also applies to the persistent config (--config); otherwise
// only to the live domain (--live). For running VMs persistent attach uses
// both --live and --config.
func (c *Client) AttachDeviceXML(domain, xmlSnippet string, persistent bool) error {
	tmp, err := writeTempXML(xmlSnippet)
	if err != nil {
		return err
	}
	defer removeTemp(tmp)
	args := []string{"attach-device", domain, tmp}
	state, _ := c.State(domain)
	if state == "running" {
		args = append(args, "--live")
	}
	if persistent {
		args = append(args, "--config")
	}
	_, err = c.run(args...)
	return err
}

// DetachDeviceXML detaches the matching device.
func (c *Client) DetachDeviceXML(domain, xmlSnippet string, persistent bool) error {
	tmp, err := writeTempXML(xmlSnippet)
	if err != nil {
		return err
	}
	defer removeTemp(tmp)
	args := []string{"detach-device", domain, tmp}
	state, _ := c.State(domain)
	if state == "running" {
		args = append(args, "--live")
	}
	if persistent {
		args = append(args, "--config")
	}
	_, err = c.run(args...)
	return err
}

func writeTempXML(content string) (string, error) {
	f, err := os.CreateTemp("", "zvmx-*.xml")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), f.Close()
}

func removeTemp(path string) { _ = os.Remove(path) }

// ---------- Networking ----------

// Network is one libvirt-managed virtual network.
type Network struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// NetworkList returns all libvirt networks (`virsh net-list --all`).
func (c *Client) NetworkList() ([]Network, error) {
	out, err := c.run("net-list", "--all")
	if err != nil {
		return nil, err
	}
	var nets []Network
	sep := false
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "---") {
			sep = true
			continue
		}
		if !sep {
			continue
		}
		f := splitColumns(line, 4)
		if len(f) < 2 || f[0] == "" {
			continue
		}
		nets = append(nets, Network{Name: f[0], Active: f[1] == "active"})
	}
	return nets, nil
}

// Interface is one VM network interface (NIC).
type Interface struct {
	MAC    string `json:"mac"`
	Type   string `json:"type"`   // network | bridge | direct
	Source string `json:"source"` // network name / bridge name / host dev
	Model  string `json:"model"`  // virtio | e1000 | ...
}

// DomainInterfaces parses the <interface> devices from a domain's
// persistent (inactive) config.
func (c *Client) DomainInterfaces(domain string) ([]Interface, error) {
	xmlStr, err := c.run("dumpxml", "--inactive", domain)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Ifaces []struct {
			Type string `xml:"type,attr"`
			MAC  struct {
				Address string `xml:"address,attr"`
			} `xml:"mac"`
			Source struct {
				Network string `xml:"network,attr"`
				Bridge  string `xml:"bridge,attr"`
				Dev     string `xml:"dev,attr"`
			} `xml:"source"`
			Model struct {
				Type string `xml:"type,attr"`
			} `xml:"model"`
		} `xml:"devices>interface"`
	}
	if err := xml.Unmarshal([]byte(xmlStr), &doc); err != nil {
		return nil, err
	}
	out := make([]Interface, 0, len(doc.Ifaces))
	for _, i := range doc.Ifaces {
		src := i.Source.Network
		if src == "" {
			src = i.Source.Bridge
		}
		if src == "" {
			src = i.Source.Dev
		}
		out = append(out, Interface{
			MAC: i.MAC.Address, Type: i.Type, Source: src, Model: i.Model.Type,
		})
	}
	return out, nil
}

// SwitchInterface repoints a NIC (identified by MAC) to a libvirt network,
// optionally changing its model. The change is applied to the persistent
// config only (--config): it never disrupts a running VM or the host, and
// takes effect on the next VM start. oldType is the NIC's current type.
func (c *Client) SwitchInterface(domain, oldType, mac, network, model string) error {
	if _, err := c.run("detach-interface", domain, oldType, "--mac", mac, "--config"); err != nil {
		return fmt.Errorf("detach old NIC: %w", err)
	}
	args := []string{"attach-interface", domain, "network", network, "--mac", mac, "--config"}
	if model != "" {
		args = append(args, "--model", model)
	}
	if _, err := c.run(args...); err != nil {
		return fmt.Errorf("attach new NIC: %w", err)
	}
	return nil
}
