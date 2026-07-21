package virsh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// zvmDomain mimics how ZimaOS's ZVM writes a domain: a namespaced
// <metadata> block (the x-casaos data) and a VNC console with a LAN-wide
// listen address and no password.
const zvmDomain = `<domain type='kvm'>
  <name>0da603ce</name>
  <metadata>
    <ns0:casaos xmlns:ns0='http://casaos'><ns0:os_type>windows</ns0:os_type></ns0:casaos>
  </metadata>
  <devices>
    <graphics type='vnc' port='-1' autoport='yes' listen='::'>
      <listen type='address' address='::'/>
    </graphics>
  </devices>
</domain>`

func TestVNCGraphicsInfo(t *testing.T) {
	found, hasPw, listen := vncGraphicsInfo(zvmDomain)
	if !found {
		t.Fatal("found = false, want true")
	}
	if hasPw {
		t.Error("hasPassword = true, want false (ZVM sets none)")
	}
	if listen != "::" {
		t.Errorf("listen = %q, want \"::\"", listen)
	}

	// A domain with no graphics at all.
	if f, _, _ := vncGraphicsInfo(`<domain><devices></devices></domain>`); f {
		t.Error("found = true for a domain with no graphics")
	}
	// A SPICE-only domain must not be mistaken for VNC.
	if f, _, _ := vncGraphicsInfo(`<domain><devices><graphics type='spice' port='-1'/></devices></domain>`); f {
		t.Error("found = true for a SPICE-only domain")
	}
}

func TestSetGraphicsPasswordXML(t *testing.T) {
	// Add a password.
	out, err := setGraphicsPasswordXML(zvmDomain, "s3cret")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if !strings.Contains(out, "passwd='s3cret'") {
		t.Errorf("result has no passwd attribute:\n%s", out)
	}
	found, hasPw, listen := vncGraphicsInfo(out)
	if !found || !hasPw {
		t.Errorf("after set: found=%v hasPw=%v, want true true", found, hasPw)
	}
	if listen != "::" {
		t.Errorf("listen changed to %q, want it preserved as \"::\"", listen)
	}
	// The x-casaos metadata block must survive the surgery untouched.
	if !strings.Contains(out, "<ns0:os_type>windows</ns0:os_type>") {
		t.Error("metadata block was lost during edit")
	}

	// Replace an existing password — exactly one passwd attribute must remain.
	out2, err := setGraphicsPasswordXML(out, "newpw")
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if strings.Count(out2, "passwd=") != 1 || !strings.Contains(out2, "passwd='newpw'") {
		t.Errorf("replace did not produce exactly one new passwd:\n%s", out2)
	}

	// Clear the password.
	out3, err := setGraphicsPasswordXML(out2, "")
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if strings.Contains(out3, "passwd=") {
		t.Errorf("clear left a passwd attribute:\n%s", out3)
	}

	// A domain with no VNC device cannot be edited.
	if _, err := setGraphicsPasswordXML(`<domain><devices/></domain>`, "x"); err == nil {
		t.Error("expected an error for a domain with no VNC graphics")
	}
}

func TestSetGraphicsPasswordSelfClosing(t *testing.T) {
	// A self-closing <graphics …/> tag (no <listen> child) must edit cleanly.
	dom := `<domain><devices><graphics type='vnc' port='5901' listen='127.0.0.1'/></devices></domain>`
	out, err := setGraphicsPasswordXML(dom, "abc")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if !strings.Contains(out, "passwd='abc'") || !strings.Contains(out, "/>") {
		t.Errorf("self-closing tag not handled:\n%s", out)
	}
	_, hasPw, listen := vncGraphicsInfo(out)
	if !hasPw || listen != "127.0.0.1" {
		t.Errorf("hasPw=%v listen=%q, want true \"127.0.0.1\"", hasPw, listen)
	}
}

func TestSetGraphicsPasswordPicksVNCAmongMany(t *testing.T) {
	// SPICE first, VNC second — the edit must land on the VNC device.
	dom := `<domain><devices>` +
		`<graphics type='spice' port='-1' autoport='yes'/>` +
		`<graphics type='vnc' port='-1' listen='::'/>` +
		`</devices></domain>`
	out, err := setGraphicsPasswordXML(dom, "pw")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if !strings.Contains(out, "passwd='pw' type='vnc'") {
		t.Errorf("password did not land on the VNC graphics device:\n%s", out)
	}
	if strings.Contains(out, "type='spice' passwd=") {
		t.Errorf("password wrongly applied to the SPICE device:\n%s", out)
	}
}

// fakeVirsh writes a stub virsh to a temp dir. It records every invocation's
// arguments one call per line in the returned log path, answers dumpxml with
// the given XML — a different document for the persistent (--inactive) and the
// live dump, which is the whole point of the live-state tests — and reports
// the given domstate.
func fakeVirsh(t *testing.T, state, inactiveXML, liveXML string) (bin, argLog string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "virsh")
	argLog = filepath.Join(dir, "args.log")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + argLog + "\n" +
		"case \"$1\" in\n" +
		"  domstate) echo '" + state + "' ;;\n" +
		"  dumpxml)\n" +
		"    case \"$*\" in\n" +
		"      *--inactive*) cat <<'INACTIVEEOF'\n" + inactiveXML + "\nINACTIVEEOF\n;;\n" +
		"      *) cat <<'LIVEEOF'\n" + liveXML + "\nLIVEEOF\n;;\n" +
		"    esac ;;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatalf("write stub virsh: %v", err)
	}
	return bin, argLog
}

// withPasswd returns the ZVM domain with a passwd attribute on its VNC device.
func withPasswd(pw string) string {
	return strings.Replace(zvmDomain,
		"<graphics type='vnc'", "<graphics passwd='"+pw+"' type='vnc'", 1)
}

// A password written after the VM booted lives in the persistent config only —
// the running qemu was launched without it and its console stays open. If this
// reported the persistent state, the UI would show a green "password set"
// badge over a console anyone on the LAN can still walk into.
func TestVNCLiveInfoSeesUnprotectedRunningConsole(t *testing.T) {
	bin, _ := fakeVirsh(t, "running", withPasswd("s3cret"), zvmDomain)
	c := &Client{Bin: bin, Timeout: 5 * time.Second}

	running, liveHasPw, err := c.VNCLiveInfo("0da603ce")
	if err != nil {
		t.Fatalf("VNCLiveInfo: %v", err)
	}
	if !running {
		t.Error("running = false for a domain in state running")
	}
	if liveHasPw {
		t.Error("live console reported as protected, but the running domain " +
			"has no passwd — this is the state that needs a VM restart")
	}

	// Once the VM restarts, the live domain carries the password too.
	bin2, _ := fakeVirsh(t, "running", withPasswd("s3cret"), withPasswd("s3cret"))
	c2 := &Client{Bin: bin2, Timeout: 5 * time.Second}
	if _, liveHasPw2, err := c2.VNCLiveInfo("0da603ce"); err != nil || !liveHasPw2 {
		t.Errorf("after restart: liveHasPassword = %v, err = %v; want true, nil", liveHasPw2, err)
	}
}

// A shut-off domain has no live console, so there is nothing to report.
func TestVNCLiveInfoShutOffDomain(t *testing.T) {
	bin, _ := fakeVirsh(t, "shut off", zvmDomain, zvmDomain)
	c := &Client{Bin: bin, Timeout: 5 * time.Second}

	running, _, err := c.VNCLiveInfo("0da603ce")
	if err != nil {
		t.Fatalf("VNCLiveInfo: %v", err)
	}
	if running {
		t.Error("running = true for a shut-off domain")
	}
}

// A protected console must read back as protected. libvirt masks the graphics
// passwd attribute unless dumpxml is given --security-info, so without that
// flag VNCInfo reported every protected VM as unprotected — which made the UI
// claim "not protected" on a VM whose console did ask for a password, and made
// the reconciler re-define the domain once a minute forever because the
// password it had just written kept reading back as absent.
func TestVNCInfoRequestsSecurityInfo(t *testing.T) {
	protected := strings.Replace(zvmDomain,
		"<graphics type='vnc'", "<graphics passwd='s3cret' type='vnc'", 1)
	bin, argLog := fakeVirsh(t, "running", protected, protected)
	c := &Client{Bin: bin, Timeout: 5 * time.Second}

	_, hasPw, _, err := c.VNCInfo("0da603ce")
	if err != nil {
		t.Fatalf("VNCInfo: %v", err)
	}
	if !hasPw {
		t.Error("hasPassword = false for a domain whose XML carries passwd")
	}

	args, err := os.ReadFile(argLog)
	if err != nil {
		t.Fatalf("read arg log: %v", err)
	}
	if !strings.Contains(string(args), "--security-info") {
		t.Errorf("dumpxml called without --security-info; libvirt masks passwd "+
			"and the console reads back as unprotected. args: %q", args)
	}
}

// The read half of SetVNCPassword must be unmasked too: it dumps, edits and
// re-defines, so a masked dump would round-trip other secrets away as well.
func TestSetVNCPasswordRequestsSecurityInfo(t *testing.T) {
	bin, argLog := fakeVirsh(t, "running", zvmDomain, zvmDomain)
	c := &Client{Bin: bin, Timeout: 5 * time.Second}

	if err := c.SetVNCPassword("0da603ce", "s3cret"); err != nil {
		t.Fatalf("SetVNCPassword: %v", err)
	}
	args, err := os.ReadFile(argLog)
	if err != nil {
		t.Fatalf("read arg log: %v", err)
	}
	for _, want := range []string{"--security-info", "define"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("virsh never called with %q; args: %q", want, args)
		}
	}
}
