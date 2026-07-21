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

	fw, err := c.FirmwareInfo("55f68434")
	if err != nil {
		t.Fatalf("FirmwareInfo: %v", err)
	}
	if fw.Loader != "/usr/share/qemu/edk2-x86_64-code.fd" {
		t.Errorf("loader = %q, want the plain edk2 code image", fw.Loader)
	}
	if fw.SecureBoot {
		t.Error("SecureBoot = true for edk2-x86_64-code.fd, which is not the secure image")
	}
}

// This is the domain ZVM writes for a Windows 11 guest, measured on ZimaOS
// 1.7.0-beta1: Secure Boot switched on, but paired with the empty
// edk2-i386-vars.fd, so no keys are enrolled and the firmware validates
// nothing. Reporting that as plain "secure boot" would be a green badge over
// a firmware in setup mode.
const zvmWin11Domain = `<domain type='kvm'>
  <name>bdc44a1d</name>
  <os firmware='efi'>
    <type arch='x86_64' machine='pc-q35-10.2'>hvm</type>
    <firmware>
      <feature enabled='no' name='enrolled-keys'/>
      <feature enabled='yes' name='secure-boot'/>
    </firmware>
    <loader readonly='yes' secure='yes' type='pflash'>/usr/share/qemu/edk2-x86_64-secure-code-win11.fd</loader>
    <nvram template='/usr/share/qemu/edk2-i386-vars.fd'>/var/lib/libvirt/qemu/nvram/bdc44a1d_VARS.fd</nvram>
  </os>
  <devices>
    <tpm model='tpm-tis'>
      <backend type='emulator' version='2.0'/>
    </tpm>
  </devices>
</domain>`

func TestFirmwareInfoSecureBootWithoutEnrolledKeys(t *testing.T) {
	bin, _ := fakeVirsh(t, "running", zvmWin11Domain, zvmWin11Domain)
	c := &Client{Bin: bin, Timeout: 5 * time.Second}

	fw, err := c.FirmwareInfo("bdc44a1d")
	if err != nil {
		t.Fatalf("FirmwareInfo: %v", err)
	}
	if !fw.SecureBoot {
		t.Error("SecureBoot = false, but the firmware block enables it")
	}
	if fw.EnrolledKeys {
		t.Error("EnrolledKeys = true, but the firmware block says enabled='no' — " +
			"reporting this as fully protected would be a green badge over a " +
			"firmware that validates nothing")
	}
	// The TPM half must still read correctly on the same domain.
	present, model, version, err := c.TPMInfo("bdc44a1d")
	if err != nil || !present || model != "tpm-tis" || version != "2.0" {
		t.Errorf("TPMInfo = (%v, %q, %q, %v); want (true, \"tpm-tis\", \"2.0\", nil)",
			present, model, version, err)
	}
}

// A legacy-BIOS domain has no loader at all — Windows 11 cannot run there.
func TestFirmwareInfoLegacyBIOS(t *testing.T) {
	bin, _ := fakeVirsh(t, "shut off",
		`<domain><os><type>hvm</type></os><devices/></domain>`,
		`<domain><os><type>hvm</type></os><devices/></domain>`)
	c := &Client{Bin: bin, Timeout: 5 * time.Second}

	fw, err := c.FirmwareInfo("x")
	if err != nil {
		t.Fatalf("FirmwareInfo: %v", err)
	}
	if fw.Loader != "" || fw.SecureBoot {
		t.Errorf("loader=%q secure=%v for a legacy-BIOS domain; want \"\" false",
			fw.Loader, fw.SecureBoot)
	}
}
