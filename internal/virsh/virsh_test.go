package virsh

import "testing"

func TestHasUSBHostdev(t *testing.T) {
	domXML := `<domain type='kvm'>
  <name>test</name>
  <devices>
    <disk type='file'/>
    <hostdev mode='subsystem' type='usb' managed='yes'>
      <source>
        <vendor id='0x8087'/>
        <product id='0x0029'/>
      </source>
    </hostdev>
  </devices>
</domain>`
	if !hasUSBHostdev(domXML, "8087", "0029") {
		t.Error("expected hostdev 8087:0029 to be found")
	}
	if !hasUSBHostdev(domXML, "8087", "0029") {
		t.Error("case-insensitive match failed")
	}
	if hasUSBHostdev(domXML, "1d6b", "0002") {
		t.Error("unrelated hostdev should not match")
	}
	if hasUSBHostdev("this is not xml", "8087", "0029") {
		t.Error("garbage input should not match")
	}
	empty := `<domain><devices><disk/></devices></domain>`
	if hasUSBHostdev(empty, "8087", "0029") {
		t.Error("a domain with no hostdev should not match")
	}
}

func TestSplitColumns(t *testing.T) {
	// A virsh snapshot-list row: the timestamp column itself contains spaces.
	cols := splitColumns(" Snap1     2026-05-18 17:09:28 +0200   disk-snapshot ", 4)
	if len(cols) < 3 {
		t.Fatalf("splitColumns: got %d columns: %#v", len(cols), cols)
	}
	if cols[0] != "Snap1" {
		t.Errorf("col0 = %q, want %q", cols[0], "Snap1")
	}
	if cols[1] != "2026-05-18 17:09:28 +0200" {
		t.Errorf("col1 = %q, want the full timestamp", cols[1])
	}
	if cols[2] != "disk-snapshot" {
		t.Errorf("col2 = %q, want %q", cols[2], "disk-snapshot")
	}
}
