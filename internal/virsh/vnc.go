package virsh

// ---------- VNC graphics console ----------
//
// ZimaOS's ZVM generates every VM's <graphics type='vnc'> with listen='::'
// and no 'passwd' attribute, leaving the console open to the whole LAN with
// no authentication. These helpers read and repair that: setting a 'passwd'
// attribute on the persistent domain config closes the hole, and the VNC
// reconciler re-applies it whenever ZVM strips it on a re-save.
//
// The domain XML is edited by string surgery on the single <graphics> opening
// tag — never by round-tripping the whole document through encoding/xml, which
// would drop libvirt's namespaced <metadata> (the x-casaos block) and reorder
// elements.
//
// Every dumpxml here passes --security-info. Without it libvirt *masks* the
// graphics passwd attribute, so a protected console reads back as unprotected:
// the UI reported "not protected" on a VM whose console did require a
// password, and the reconciler — seeing a missing password it had itself just
// written — re-defined the domain on every single pass, once a minute,
// indefinitely. Reading back what we wrote is what makes the reconciler
// idempotent; it must therefore read the unmasked XML.

import (
	"fmt"
	"strings"
)

// vncOpenTag locates the VNC <graphics …> opening tag in domain XML. It
// returns the tag text, its start offset, the offset just past its '>', and
// whether a VNC graphics device was found at all.
func vncOpenTag(domXML string) (tag string, start, end int, found bool) {
	off := 0
	for {
		i := strings.Index(domXML[off:], "<graphics")
		if i < 0 {
			return "", 0, 0, false
		}
		abs := off + i
		gt := strings.IndexByte(domXML[abs:], '>')
		if gt < 0 {
			return "", 0, 0, false
		}
		tagEnd := abs + gt + 1
		t := domXML[abs:tagEnd]
		if strings.Contains(t, "type='vnc'") || strings.Contains(t, `type="vnc"`) {
			return t, abs, tagEnd, true
		}
		off = tagEnd
	}
}

// tagAttr extracts the value of attr (attr='v' or attr="v") from a single
// element tag. Returns "" when the attribute is absent.
func tagAttr(tag, attr string) string {
	for _, q := range []byte{'\'', '"'} {
		key := attr + "=" + string(q)
		i := strings.Index(tag, key)
		if i < 0 {
			continue
		}
		rest := tag[i+len(key):]
		j := strings.IndexByte(rest, q)
		if j < 0 {
			continue
		}
		return rest[:j]
	}
	return ""
}

// tagDropAttr removes ` attr='…'` (with its leading space) from a tag.
func tagDropAttr(tag, attr string) string {
	for _, q := range []byte{'\'', '"'} {
		key := " " + attr + "=" + string(q)
		i := strings.Index(tag, key)
		if i < 0 {
			continue
		}
		rest := tag[i+len(key):]
		j := strings.IndexByte(rest, q)
		if j < 0 {
			continue
		}
		return tag[:i] + rest[j+1:]
	}
	return tag
}

// vncGraphicsInfo reports whether domain XML has a VNC graphics device,
// whether that device sets a password, and its listen address.
func vncGraphicsInfo(domXML string) (found, hasPassword bool, listen string) {
	tag, _, _, ok := vncOpenTag(domXML)
	if !ok {
		return false, false, ""
	}
	return true, tagAttr(tag, "passwd") != "", tagAttr(tag, "listen")
}

// setGraphicsPasswordXML returns domXML with the VNC graphics device's passwd
// attribute set to pw (pw == "" removes it). It errors if there is no VNC
// graphics device to edit.
func setGraphicsPasswordXML(domXML, pw string) (string, error) {
	tag, start, end, ok := vncOpenTag(domXML)
	if !ok {
		return "", fmt.Errorf("domain has no VNC graphics device")
	}
	newTag := tagDropAttr(tag, "passwd")
	if pw != "" {
		const head = "<graphics"
		newTag = head + " passwd='" + pw + "'" + newTag[len(head):]
	}
	return domXML[:start] + newTag + domXML[end:], nil
}

// VNCInfo reports the VNC console state from the domain's persistent config:
// whether a VNC device exists, whether it is password-protected, and its
// listen address.
func (c *Client) VNCInfo(domain string) (present, hasPassword bool, listen string, err error) {
	out, err := c.run("dumpxml", "--inactive", "--security-info", domain)
	if err != nil {
		return false, false, "", err
	}
	present, hasPassword, listen = vncGraphicsInfo(out)
	return present, hasPassword, listen, nil
}

// VNCHasPassword reports whether the domain's VNC console is password-protected.
// A domain with no VNC device reports true: there is nothing for the
// reconciler to repair.
func (c *Client) VNCHasPassword(domain string) (bool, error) {
	present, hasPw, _, err := c.VNCInfo(domain)
	if err != nil {
		return false, err
	}
	if !present {
		return true, nil
	}
	return hasPw, nil
}

// VNCLiveInfo reports the VNC console state of the *running* instance, which
// is not the same thing as the persistent config: qemu is launched with the
// graphics options the domain had at start time, so a password written after
// boot sits in the persistent config while the live console still accepts
// anyone. Reporting only the persistent state would put a green
// "password set" badge over a console that is wide open right now.
//
// running is false for a domain that is not running, in which case there is no
// live console to report on and hasPassword is meaningless.
func (c *Client) VNCLiveInfo(domain string) (running, hasPassword bool, err error) {
	state, err := c.State(domain)
	if err != nil {
		return false, false, err
	}
	if state != "running" {
		return false, false, nil
	}
	// Without --inactive this dumps the live domain; --security-info is as
	// necessary here as everywhere else (see the note at the top of the file).
	out, err := c.run("dumpxml", "--security-info", domain)
	if err != nil {
		return false, false, err
	}
	present, hasPw, _ := vncGraphicsInfo(out)
	if !present {
		// No console at all — nothing is exposed, so treat it as protected.
		return true, true, nil
	}
	return true, hasPw, nil
}

// SetVNCPassword sets (pw != "") or clears (pw == "") the VNC console password
// in the domain's persistent config. It is applied with `virsh define`, which
// only rewrites the persistent configuration and never disturbs a running VM;
// the change therefore takes effect on the next VM start.
func (c *Client) SetVNCPassword(domain, pw string) error {
	out, err := c.run("dumpxml", "--inactive", "--security-info", domain)
	if err != nil {
		return err
	}
	modified, err := setGraphicsPasswordXML(out, pw)
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
