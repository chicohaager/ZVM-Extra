package usb

import (
	"strings"
	"testing"
)

func TestParseLsusb(t *testing.T) {
	out := `Bus 001 Device 002: ID 1d6b:0002 Linux Foundation 2.0 root hub
Bus 003 Device 003: ID 8087:0029 Intel Corp. AX200 Bluetooth
garbage line that should be skipped
Bus 004 Device 002: ID 0bda:9210 Realtek Semiconductor Corp. RTL9210`
	devs := parseLsusb(out)
	if len(devs) != 3 {
		t.Fatalf("parseLsusb: got %d devices, want 3", len(devs))
	}
	if devs[0].VendorID != "1d6b" || devs[0].ProductID != "0002" {
		t.Errorf("dev0 = %+v", devs[0])
	}
	if devs[1].Description != "Intel Corp. AX200 Bluetooth" {
		t.Errorf("dev1 description = %q", devs[1].Description)
	}
	if devs[2].Bus != "004" || devs[2].DeviceID != "002" {
		t.Errorf("dev2 = %+v", devs[2])
	}
}

func TestIsRootHub(t *testing.T) {
	if !IsRootHub("1d6b") || !IsRootHub("1D6B") {
		t.Error("vendor 1d6b should be detected as a root hub")
	}
	if IsRootHub("8087") {
		t.Error("vendor 8087 should not be a root hub")
	}
}

func TestHostdevXML(t *testing.T) {
	xml := HostdevXML("8087", "0029")
	if !strings.Contains(xml, "0x8087") || !strings.Contains(xml, "0x0029") {
		t.Errorf("HostdevXML missing ids: %s", xml)
	}
	if !strings.Contains(xml, "type='usb'") {
		t.Errorf("HostdevXML not a usb hostdev: %s", xml)
	}
}
