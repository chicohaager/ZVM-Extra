// Package pci enumerates host PCI devices and builds libvirt hostdev XML for
// passing them through to a domain via VFIO. libvirt is left to do the
// vfio-pci driver bind/unbind itself (managed='yes'), so this package never
// touches /sys/bus/pci/drivers directly.
package pci

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Device is one host PCI device.
type Device struct {
	Address     string `json:"address"`      // "0000:00:02.0"
	VendorID    string `json:"vendor_id"`    // "8086"
	DeviceID    string `json:"device_id"`    // "a720"
	Class       string `json:"class"`        // "0300" (PCI class code)
	ClassName   string `json:"class_name"`   // "VGA compatible controller"
	Description string `json:"description"`  // "Intel Corporation Raptor Lake-P [UHD Graphics]"
	Driver      string `json:"driver"`       // current kernel driver, "" if unbound
	IOMMUGroup  string `json:"iommu_group"`  // "" when IOMMU is off
	Passable    bool   `json:"passable"`     // false for bridges / host bridge
}

// HostList enumerates PCI devices via `lspci -Dvmmnnk`, enriched with the
// IOMMU group read from sysfs.
func HostList(lspciBin string) ([]Device, error) {
	if lspciBin == "" {
		lspciBin = "/usr/bin/lspci"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, lspciBin, "-Dvmmnnk")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("lspci: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	devs := parseLspci(stdout.String())
	for i := range devs {
		devs[i].IOMMUGroup = iommuGroup(devs[i].Address)
	}
	return devs, nil
}

// parseLspci parses the machine-readable `lspci -Dvmmnnk` block format:
// blank-line-separated records of "Key:\tValue" lines.
func parseLspci(out string) []Device {
	var devs []Device
	var cur Device
	flush := func() {
		if cur.Address != "" {
			cur.Description = strings.TrimSpace(cur.Description)
			cur.Passable = isPassable(cur.Class)
			devs = append(devs, cur)
		}
		cur = Device{}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "Slot":
			cur.Address = val
		case "Class":
			cur.ClassName = stripLastBracket(val)
			cur.Class = lastBracketID(val)
		case "Vendor":
			cur.Description = stripLastBracket(val)
			cur.VendorID = lastBracketID(val)
		case "Device":
			cur.Description = strings.TrimSpace(cur.Description + " " + stripLastBracket(val))
			cur.DeviceID = lastBracketID(val)
		case "Driver":
			cur.Driver = val
		}
	}
	flush()
	return devs
}

// lastBracketID returns the content of the final "[...]" in s, e.g.
// "Raptor Lake-P [UHD Graphics] [a720]" -> "a720".
func lastBracketID(s string) string {
	close := strings.LastIndex(s, "]")
	if close < 0 {
		return ""
	}
	open := strings.LastIndex(s[:close], "[")
	if open < 0 {
		return ""
	}
	return s[open+1 : close]
}

// stripLastBracket removes the final " [...]" suffix from s.
func stripLastBracket(s string) string {
	close := strings.LastIndex(s, "]")
	if close < 0 {
		return strings.TrimSpace(s)
	}
	open := strings.LastIndex(s[:close], "[")
	if open < 0 {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(s[:open])
}

// isPassable reports whether a device of the given PCI class may sensibly be
// passed through. Bridges (class 06xx) and the host bridge must never be.
func isPassable(class string) bool {
	// An unknown (empty) class cannot be assessed — treat it as not passable.
	if class == "" {
		return false
	}
	return !strings.HasPrefix(strings.ToLower(class), "06")
}

// iommuGroup reads the IOMMU group number for a PCI address from sysfs.
// Returns "" when IOMMU is disabled.
func iommuGroup(address string) string {
	link, err := os.Readlink(filepath.Join("/sys/bus/pci/devices", address, "iommu_group"))
	if err != nil {
		return ""
	}
	return filepath.Base(link)
}

// ValidAddress reports whether s is a well-formed PCI address
// "domain:bus:slot.function", e.g. "0000:00:02.0".
func ValidAddress(s string) bool {
	dom, rest, ok := strings.Cut(s, ":")
	if !ok || len(dom) != 4 || !isHex(dom) {
		return false
	}
	bus, sf, ok := strings.Cut(rest, ":")
	if !ok || len(bus) != 2 || !isHex(bus) {
		return false
	}
	slot, fn, ok := strings.Cut(sf, ".")
	if !ok || len(slot) != 2 || !isHex(slot) {
		return false
	}
	// PCI function is a single digit 0–7.
	return len(fn) == 1 && fn[0] >= '0' && fn[0] <= '7'
}

func isHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return s != ""
}

// HostdevXML returns the libvirt <hostdev> snippet for a PCI passthrough.
// managed='yes' lets libvirt bind/unbind vfio-pci around the VM lifecycle.
func HostdevXML(address string) (string, error) {
	if !ValidAddress(address) {
		return "", fmt.Errorf("invalid pci address %q", address)
	}
	dom, rest, _ := strings.Cut(address, ":")
	bus, sf, _ := strings.Cut(rest, ":")
	slot, fn, _ := strings.Cut(sf, ".")
	return fmt.Sprintf(
		`<hostdev mode='subsystem' type='pci' managed='yes'>
  <source>
    <address domain='0x%s' bus='0x%s' slot='0x%s' function='0x%s'/>
  </source>
</hostdev>`, strings.ToLower(dom), strings.ToLower(bus), strings.ToLower(slot), strings.ToLower(fn)), nil
}
