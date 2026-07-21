package virsh

// ---------- TPM (Trusted Platform Module) ----------
//
// ZimaOS ships every ingredient for a Windows-11-capable VM and ZVM wires up
// none of them: swtpm 0.9.0 lives in /usr/bin, `virsh domcapabilities` reports
// TPM backend `emulator` with versions 1.2 and 2.0, and /usr/share/qemu even
// carries a firmware image named edk2-x86_64-secure-code-win11.fd. No domain
// ZVM creates has a <tpm> device, so Windows 11 refuses to install.
//
// Adding one is a two-line edit of the <devices> block:
//
//	<tpm model='tpm-crb'>
//	  <backend type='emulator' version='2.0'/>
//	</tpm>
//
// libvirt then starts a per-domain swtpm process and keeps its state under
// /var/lib/libvirt/swtpm/<uuid>/ — verified running on ZimaOS 1.7.0-beta1.
//
// As with the VNC password, the edit is applied with `virsh define`, which
// rewrites only the persistent config: a running VM keeps running without a
// TPM until it is restarted. Both states are therefore reported separately —
// telling an operator "TPM enabled" while the running guest has no TPM device
// would send them to a Windows installer that still refuses.
//
// The XML is edited by string surgery, never by round-tripping the document
// through encoding/xml, which would drop libvirt's namespaced <metadata> (the
// x-casaos block) and reorder elements.

import (
	"fmt"
	"regexp"
	"strings"
)

// tpmDeviceRe matches a whole <tpm>…</tpm> element, including a self-closing
// <tpm …/>, together with the whitespace on its line.
var tpmDeviceRe = regexp.MustCompile(`(?s)[ \t]*<tpm\b(?:[^>]*/>|.*?</tpm>)\s*\n?`)

// tpmDevice is the element VM Extras adds. tpm-crb is the modern CRB
// interface Windows 11 expects; tpm-tis is the older TIS interface. Both are
// supported by the host stack, but only one needs to be offered.
const tpmDevice = "    <tpm model='tpm-crb'>\n" +
	"      <backend type='emulator' version='2.0'/>\n" +
	"    </tpm>\n"

// tpmInfoXML reports whether domain XML declares a TPM, and its model and
// backend version when it does.
func tpmInfoXML(domXML string) (present bool, model, version string) {
	m := tpmDeviceRe.FindString(domXML)
	if m == "" {
		return false, "", ""
	}
	// The model sits on the <tpm> tag, the version on the <backend> tag.
	openTag := m
	if i := strings.IndexByte(m, '>'); i >= 0 {
		openTag = m[:i+1]
	}
	model = tagAttr(openTag, "model")
	if i := strings.Index(m, "<backend"); i >= 0 {
		backend := m[i:]
		if j := strings.IndexByte(backend, '>'); j >= 0 {
			version = tagAttr(backend[:j+1], "version")
		}
	}
	return true, model, version
}

// setTPMXML returns domXML with a TPM device added (enabled) or removed.
// Enabling a domain that already has one leaves the existing device alone:
// silently rewriting a TPM the operator configured by hand — a passthrough
// device, or a 1.2 backend — would be a destructive surprise.
func setTPMXML(domXML string, enabled bool) (string, error) {
	present, _, _ := tpmInfoXML(domXML)
	switch {
	case enabled && present:
		return domXML, nil
	case enabled:
		i := strings.LastIndex(domXML, "</devices>")
		if i < 0 {
			return "", fmt.Errorf("domain XML has no <devices> section")
		}
		return domXML[:i] + tpmDevice + domXML[i:], nil
	case present:
		return tpmDeviceRe.ReplaceAllString(domXML, ""), nil
	default:
		return domXML, nil
	}
}

// TPMInfo reports the TPM device in the domain's persistent config.
func (c *Client) TPMInfo(domain string) (present bool, model, version string, err error) {
	out, err := c.run("dumpxml", "--inactive", "--security-info", domain)
	if err != nil {
		return false, "", "", err
	}
	present, model, version = tpmInfoXML(out)
	return present, model, version, nil
}

// TPMLiveInfo reports the TPM device of the *running* instance. qemu is
// launched with the devices the domain had at start time, so a TPM added
// afterwards is in the persistent config while the running guest still has
// none — which is exactly the state where a Windows 11 installer keeps
// refusing. running is false when the domain is not running, in which case
// present is meaningless.
func (c *Client) TPMLiveInfo(domain string) (running, present bool, err error) {
	state, err := c.State(domain)
	if err != nil {
		return false, false, err
	}
	if state != "running" {
		return false, false, nil
	}
	out, err := c.run("dumpxml", "--security-info", domain)
	if err != nil {
		return false, false, err
	}
	present, _, _ = tpmInfoXML(out)
	return true, present, nil
}

// SetTPM adds (enabled) or removes a TPM 2.0 emulator device in the domain's
// persistent config. It takes effect on the VM's next start.
func (c *Client) SetTPM(domain string, enabled bool) error {
	out, err := c.run("dumpxml", "--inactive", "--security-info", domain)
	if err != nil {
		return err
	}
	modified, err := setTPMXML(out, enabled)
	if err != nil {
		return err
	}
	tmp, err := writeTempXML(modified)
	if err != nil {
		return err
	}
	defer removeTemp(tmp)
	_, err = c.run("define", tmp)
	return err
}

// ---------- Secure Boot (read-only) ----------
//
// Windows 11 wants Secure Boot as well as a TPM. Switching a VM's firmware is
// NOT offered: the loader and its NVRAM vars file are a matched pair, and
// pointing an existing VM at a different loader while keeping the NVRAM it
// booted with can leave it unable to boot. That is a repair job, not a toggle.
// So the firmware is reported, and the operator decides.

// secureLoaderRe matches the loader path of a firmware known to enforce
// Secure Boot. ZimaOS ships edk2-x86_64-secure-code.fd and a Windows-11
// specific edk2-x86_64-secure-code-win11.fd next to the plain code images.
var secureLoaderRe = regexp.MustCompile(`secure-code`)

// FirmwareInfo reports the domain's UEFI loader path and whether it is a
// Secure Boot capable image. A domain with no <loader> boots legacy BIOS,
// which Windows 11 does not support at all.
func (c *Client) FirmwareInfo(domain string) (loader string, secureBoot bool, err error) {
	out, err := c.run("dumpxml", "--inactive", "--security-info", domain)
	if err != nil {
		return "", false, err
	}
	i := strings.Index(out, "<loader")
	if i < 0 {
		return "", false, nil
	}
	rest := out[i:]
	j := strings.Index(rest, "</loader>")
	if j < 0 {
		return "", false, nil
	}
	tag := rest[:j]
	k := strings.IndexByte(tag, '>')
	if k < 0 {
		return "", false, nil
	}
	loader = strings.TrimSpace(tag[k+1:])
	return loader, secureLoaderRe.MatchString(loader), nil
}
