package virsh

import (
	"strings"
	"testing"
	"time"
)

// zvmUEFIDomain mimics a domain as ZVM writes it under ZimaOS 1.7.0-beta1:
// UEFI firmware, but the *non*-secure loader, and no TPM device at all —
// which is why Windows 11 refuses to install on a ZVM-created VM.
const zvmUEFIDomain = `<domain type='kvm'>
  <name>55f68434</name>
  <metadata>
    <ns0:casaos xmlns:ns0='http://casaos'><ns0:os_type>windows</ns0:os_type></ns0:casaos>
  </metadata>
  <os firmware='efi'>
    <loader readonly='yes' type='pflash'>/usr/share/qemu/edk2-x86_64-code.fd</loader>
    <nvram template='/usr/share/qemu/edk2-i386-vars.fd'>/var/lib/libvirt/qemu/nvram/55f68434_VARS.fd</nvram>
  </os>
  <devices>
    <graphics type='vnc' port='-1' listen='::'/>
  </devices>
</domain>`

func TestTPMInfoXMLOnZVMDomain(t *testing.T) {
	if present, _, _ := tpmInfoXML(zvmUEFIDomain); present {
		t.Error("present = true, but a ZVM-created domain has no TPM device")
	}
}

func TestSetTPMXMLAddAndRemove(t *testing.T) {
	added, err := setTPMXML(zvmUEFIDomain, true)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	present, model, version := tpmInfoXML(added)
	if !present || model != "tpm-crb" || version != "2.0" {
		t.Errorf("after add: present=%v model=%q version=%q; want true \"tpm-crb\" \"2.0\"",
			present, model, version)
	}
	// The device must land inside <devices>, not after it.
	if strings.Index(added, "<tpm") > strings.Index(added, "</devices>") {
		t.Errorf("TPM device landed outside <devices>:\n%s", added)
	}
	// The x-casaos metadata block must survive the surgery.
	if !strings.Contains(added, "<ns0:os_type>windows</ns0:os_type>") {
		t.Error("metadata block was lost during edit")
	}

	// Adding twice must not produce two devices.
	twice, err := setTPMXML(added, true)
	if err != nil {
		t.Fatalf("add twice: %v", err)
	}
	if strings.Count(twice, "<tpm") != 1 {
		t.Errorf("adding twice produced %d TPM devices:\n%s", strings.Count(twice, "<tpm"), twice)
	}

	removed, err := setTPMXML(added, false)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if strings.Contains(removed, "<tpm") {
		t.Errorf("remove left a TPM device:\n%s", removed)
	}
	if !strings.Contains(removed, "</devices>") {
		t.Error("remove damaged the devices section")
	}
}

// An operator may have configured a passthrough TPM or a 1.2 backend by hand.
// Enabling must leave that alone rather than silently rewriting it.
func TestSetTPMXMLKeepsHandConfiguredDevice(t *testing.T) {
	custom := strings.Replace(zvmUEFIDomain, "</devices>",
		"  <tpm model='tpm-tis'>\n      <backend type='passthrough'/>\n    </tpm>\n  </devices>", 1)
	out, err := setTPMXML(custom, true)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	_, model, _ := tpmInfoXML(out)
	if model != "tpm-tis" {
		t.Errorf("model = %q; a hand-configured TPM must not be rewritten", model)
	}
	if strings.Count(out, "<tpm") != 1 {
		t.Errorf("produced %d TPM devices, want 1", strings.Count(out, "<tpm"))
	}
}

func TestSetTPMXMLSelfClosingDevice(t *testing.T) {
	dom := `<domain><devices><tpm model='tpm-crb'/></devices></domain>`
	if present, model, _ := tpmInfoXML(dom); !present || model != "tpm-crb" {
		t.Errorf("self-closing <tpm/> not detected: present=%v model=%q", present, model)
	}
	out, err := setTPMXML(dom, false)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if strings.Contains(out, "<tpm") {
		t.Errorf("remove did not handle a self-closing device:\n%s", out)
	}
}

func TestSetTPMXMLNoDevicesSection(t *testing.T) {
	if _, err := setTPMXML(`<domain><name>x</name></domain>`, true); err == nil {
		t.Error("expected an error for a domain with no <devices> section")
	}
}

// A TPM added while the VM runs sits in the persistent config only; the
// running guest still has none, and a Windows 11 installer keeps refusing.
// Reporting the persistent state as "TPM active" would send the operator back
// to an installer that does not budge.
func TestTPMLiveInfoSeesRunningGuestWithoutTPM(t *testing.T) {
	withTPM, err := setTPMXML(zvmUEFIDomain, true)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	bin, _ := fakeVirsh(t, "running", withTPM, zvmUEFIDomain)
	c := &Client{Bin: bin, Timeout: 5 * time.Second}

	running, live, err := c.TPMLiveInfo("55f68434")
	if err != nil {
		t.Fatalf("TPMLiveInfo: %v", err)
	}
	if !running {
		t.Error("running = false for a domain in state running")
	}
	if live {
		t.Error("live TPM reported present, but the running domain has none — " +
			"this is the state that needs a VM restart")
	}

	// After a restart the running domain carries the device too.
	bin2, _ := fakeVirsh(t, "running", withTPM, withTPM)
	c2 := &Client{Bin: bin2, Timeout: 5 * time.Second}
	if _, live2, err := c2.TPMLiveInfo("55f68434"); err != nil || !live2 {
		t.Errorf("after restart: live = %v, err = %v; want true, nil", live2, err)
	}
}

// Windows 11 needs Secure Boot as well. ZVM points its VMs at the plain
// edk2-x86_64-code.fd even though ZimaOS ships a secure image next to it, so
// the tab has to be able to say so.
func TestFirmwareInfoDetectsNonSecureLoader(t *testing.T) {
	bin, _ := fakeVirsh(t, "running", zvmUEFIDomain, zvmUEFIDomain)
	c := &Client{Bin: bin, Timeout: 5 * time.Second}

	loader, secure, err := c.FirmwareInfo("55f68434")
	if err != nil {
		t.Fatalf("FirmwareInfo: %v", err)
	}
	if loader != "/usr/share/qemu/edk2-x86_64-code.fd" {
		t.Errorf("loader = %q, want the plain edk2 code image", loader)
	}
	if secure {
		t.Error("secureBoot = true for edk2-x86_64-code.fd, which is not the secure image")
	}

	secureDom := strings.Replace(zvmUEFIDomain,
		"edk2-x86_64-code.fd", "edk2-x86_64-secure-code-win11.fd", 1)
	bin2, _ := fakeVirsh(t, "running", secureDom, secureDom)
	c2 := &Client{Bin: bin2, Timeout: 5 * time.Second}
	if _, secure2, err := c2.FirmwareInfo("55f68434"); err != nil || !secure2 {
		t.Errorf("secureBoot = %v, err = %v for the win11 secure image; want true, nil", secure2, err)
	}
}

// A legacy-BIOS domain has no loader at all — Windows 11 cannot run there.
func TestFirmwareInfoLegacyBIOS(t *testing.T) {
	bin, _ := fakeVirsh(t, "shut off",
		`<domain><os><type>hvm</type></os><devices/></domain>`,
		`<domain><os><type>hvm</type></os><devices/></domain>`)
	c := &Client{Bin: bin, Timeout: 5 * time.Second}

	loader, secure, err := c.FirmwareInfo("x")
	if err != nil {
		t.Fatalf("FirmwareInfo: %v", err)
	}
	if loader != "" || secure {
		t.Errorf("loader=%q secure=%v for a legacy-BIOS domain; want \"\" false", loader, secure)
	}
}
