package vnc

import (
	"strings"
	"testing"
)

func TestGetVNCConfig(t *testing.T) {
	xmlStr := `<domain type='kvm'>
  <name>test-vm</name>
  <devices>
    <graphics type='vnc' port='5901' autoport='no' listen='0.0.0.0' passwd='secret'>
      <listen type='address' address='0.0.0.0'/>
    </graphics>
  </devices>
</domain>`

	cfg, ok := GetVNCConfig(xmlStr)
	if !ok {
		t.Fatal("expected VNC config to be found")
	}
	if cfg.Port != 5901 {
		t.Errorf("expected port 5901, got %d", cfg.Port)
	}
	if cfg.Listen != "0.0.0.0" {
		t.Errorf("expected listen 0.0.0.0, got %s", cfg.Listen)
	}
	if cfg.Passwd != "secret" {
		t.Errorf("expected password 'secret', got %s", cfg.Passwd)
	}
}

func TestModifyGraphicsXML_Replace(t *testing.T) {
	xmlStr := `<domain type='kvm'>
  <name>test-vm</name>
  <devices>
    <graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1'>
      <listen type='address' address='127.0.0.1'/>
    </graphics>
  </devices>
</domain>`

	modified, err := ModifyGraphicsXML(xmlStr, 5900, "0.0.0.0", "newpass")
	if err != nil {
		t.Fatal(err)
	}

	cfg, ok := GetVNCConfig(modified)
	if !ok {
		t.Fatal("expected VNC config to be found after modify")
	}
	if cfg.Port != 5900 {
		t.Errorf("expected port 5900, got %d", cfg.Port)
	}
	if cfg.Listen != "0.0.0.0" {
		t.Errorf("expected listen 0.0.0.0, got %s", cfg.Listen)
	}
	if cfg.Passwd != "newpass" {
		t.Errorf("expected passwd 'newpass', got %s", cfg.Passwd)
	}
	if cfg.Autoport != "no" {
		t.Errorf("expected autoport 'no', got %s", cfg.Autoport)
	}

	// Verify it still parses as valid XML
	if !strings.Contains(modified, "<graphics type=\"vnc\" port=\"5900\" autoport=\"no\" listen=\"0.0.0.0\" passwd=\"newpass\">") {
		t.Errorf("unexpected XML content: %s", modified)
	}
}

func TestModifyGraphicsXML_Insert(t *testing.T) {
	xmlStr := `<domain type='kvm'>
  <name>test-vm</name>
  <devices>
    <disk type='file' device='disk'>
      <source file='/path/to/disk.qcow2'/>
      <target dev='vda' bus='virtio'/>
    </disk>
  </devices>
</domain>`

	modified, err := ModifyGraphicsXML(xmlStr, 5902, "10.0.0.5", "")
	if err != nil {
		t.Fatal(err)
	}

	cfg, ok := GetVNCConfig(modified)
	if !ok {
		t.Fatal("expected VNC config to be found after insert")
	}
	if cfg.Port != 5902 {
		t.Errorf("expected port 5902, got %d", cfg.Port)
	}
	if cfg.Listen != "10.0.0.5" {
		t.Errorf("expected listen 10.0.0.5, got %s", cfg.Listen)
	}
	if cfg.Passwd != "" {
		t.Errorf("expected empty passwd, got %s", cfg.Passwd)
	}

	if !strings.Contains(modified, "<graphics type=\"vnc\" port=\"5902\" autoport=\"no\" listen=\"10.0.0.5\">") {
		t.Errorf("unexpected XML content: %s", modified)
	}
}
