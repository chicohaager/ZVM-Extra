package pci

import (
	"strings"
	"testing"
)

func TestParseLspci(t *testing.T) {
	out := "Slot:\t0000:00:02.0\n" +
		"Class:\tVGA compatible controller [0300]\n" +
		"Vendor:\tIntel Corporation [8086]\n" +
		"Device:\tRaptor Lake-P [UHD Graphics] [a720]\n" +
		"Driver:\ti915\n" +
		"\n" +
		"Slot:\t0000:00:1c.0\n" +
		"Class:\tPCI bridge [0604]\n" +
		"Vendor:\tIntel Corporation [8086]\n" +
		"Device:\tDevice [51b8]\n" +
		"Driver:\tpcieport\n"
	devs := parseLspci(out)
	if len(devs) != 2 {
		t.Fatalf("parseLspci: got %d devices, want 2", len(devs))
	}
	d := devs[0]
	if d.Address != "0000:00:02.0" || d.VendorID != "8086" || d.DeviceID != "a720" {
		t.Errorf("dev0 = %+v", d)
	}
	if d.Class != "0300" || d.Driver != "i915" {
		t.Errorf("dev0 class/driver = %q / %q", d.Class, d.Driver)
	}
	if !d.Passable {
		t.Error("VGA controller should be passable")
	}
	if !strings.Contains(d.Description, "UHD Graphics") {
		t.Errorf("dev0 description = %q", d.Description)
	}
	if devs[1].Passable {
		t.Error("PCI bridge (class 0604) must not be passable")
	}
}

func TestValidAddress(t *testing.T) {
	good := []string{"0000:00:02.0", "0000:01:00.0", "abcd:ff:1f.7"}
	for _, s := range good {
		if !ValidAddress(s) {
			t.Errorf("ValidAddress(%q) = false, want true", s)
		}
	}
	bad := []string{"", "00:02.0", "0000:00:02", "0000:00:02.8", "zzzz:00:02.0", "0000:0:02.0"}
	for _, s := range bad {
		if ValidAddress(s) {
			t.Errorf("ValidAddress(%q) = true, want false", s)
		}
	}
}

func TestHostdevXML(t *testing.T) {
	xml, err := HostdevXML("0000:01:00.0")
	if err != nil {
		t.Fatalf("HostdevXML: %v", err)
	}
	for _, want := range []string{
		"type='pci'", "managed='yes'",
		"domain='0x0000'", "bus='0x01'", "slot='0x00'", "function='0x0'",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("HostdevXML missing %q in:\n%s", want, xml)
		}
	}
	if _, err := HostdevXML("bogus"); err == nil {
		t.Error("HostdevXML should reject a bogus address")
	}
}
