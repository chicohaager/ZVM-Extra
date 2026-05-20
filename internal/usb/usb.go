// Package usb enumerates USB devices on the host via `lsusb` and builds
// libvirt hostdev XML for passing them into a domain.
package usb

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Device is one host USB device.
type Device struct {
	Bus         string `json:"bus"`         // "001"
	DeviceID    string `json:"device_id"`   // "002"
	VendorID    string `json:"vendor_id"`   // "1d6b"
	ProductID   string `json:"product_id"`  // "0002"
	Description string `json:"description"` // "Linux Foundation 2.0 root hub"
}

// blockedVendors contains USB vendor IDs that must never be passed through to
// a guest. 1d6b is the Linux Foundation — all its products are kernel root hubs.
// Passing a root hub to a guest can make USB invisible to the host after reboot.
var blockedVendors = map[string]struct{}{
	"1d6b": {},
}

// IsRootHub returns true when the vendor ID belongs to a blocked device class
// (currently: Linux Foundation kernel root hubs, vendor 1d6b).
func IsRootHub(vendorID string) bool {
	_, blocked := blockedVendors[strings.ToLower(vendorID)]
	return blocked
}

// HostList returns all devices visible to `lsusb`, with kernel root hubs
// removed. Root hubs must never be passed through to a guest.
func HostList(lsusbBin string) ([]Device, error) {
	if lsusbBin == "" {
		lsusbBin = "/usr/bin/lsusb"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, lsusbBin)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("lsusb: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	all := parseLsusb(stdout.String())
	out := all[:0]
	for _, d := range all {
		if !IsRootHub(d.VendorID) {
			out = append(out, d)
		}
	}
	return out, nil
}

// parseLsusb parses lines like:
//   Bus 001 Device 002: ID 1d6b:0002 Linux Foundation 2.0 root hub
func parseLsusb(out string) []Device {
	var devs []Device
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Tokenize loosely.
		// Expected fields: ["Bus","XXX","Device","YYY:","ID","vvvv:pppp","desc..."]
		parts := strings.Fields(line)
		if len(parts) < 6 || parts[0] != "Bus" || parts[2] != "Device" || parts[4] != "ID" {
			continue
		}
		idParts := strings.SplitN(parts[5], ":", 2)
		if len(idParts) != 2 {
			continue
		}
		desc := ""
		if len(parts) > 6 {
			desc = strings.Join(parts[6:], " ")
		}
		devs = append(devs, Device{
			Bus:         parts[1],
			DeviceID:    strings.TrimSuffix(parts[3], ":"),
			VendorID:    strings.ToLower(idParts[0]),
			ProductID:   strings.ToLower(idParts[1]),
			Description: desc,
		})
	}
	return devs
}

// HostdevXML returns the <hostdev> XML snippet used by virsh attach-device.
// We pass by vendor/product (stable across re-plug), not bus/device (volatile).
func HostdevXML(vendorID, productID string) string {
	return fmt.Sprintf(
		`<hostdev mode='subsystem' type='usb' managed='yes'>
  <source>
    <vendor id='0x%s'/>
    <product id='0x%s'/>
  </source>
</hostdev>`,
		strings.ToLower(vendorID), strings.ToLower(productID),
	)
}
