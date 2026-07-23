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
	"strconv"
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
//
// The listen address is read from the <listen> child element when there is
// one and from the `listen` attribute otherwise: libvirt writes both, and the
// child element is what it honours if the two ever disagree.
func vncGraphicsInfo(domXML string) (found, hasPassword bool, listen string) {
	tag, _, end, ok := vncOpenTag(domXML)
	if !ok {
		return false, false, ""
	}
	listen = tagAttr(tag, "listen")
	if child := vncListenChild(domXML, tag, end); child != "" {
		if addr := tagAttr(child, "address"); addr != "" {
			listen = addr
		}
	}
	return true, tagAttr(tag, "passwd") != "", listen
}

// vncGraphicsPort reports the console's TCP port and whether libvirt picks it
// automatically. An autoport domain has no fixed port until it starts, so the
// persistent config reports port -1 or none at all.
func vncGraphicsPort(domXML string) (port int, autoport bool) {
	tag, _, _, ok := vncOpenTag(domXML)
	if !ok {
		return 0, false
	}
	autoport = tagAttr(tag, "autoport") != "no"
	if p, err := strconv.Atoi(tagAttr(tag, "port")); err == nil && p > 0 {
		port = p
	}
	return port, autoport
}

// vncListenChild returns the <listen …> child element of the graphics element
// whose opening tag is `tag` (ending at offset `end`), or "" if there is none.
func vncListenChild(domXML, tag string, end int) string {
	if strings.HasSuffix(strings.TrimSpace(tag), "/>") {
		return "" // self-closing: no children
	}
	closeIdx := strings.Index(domXML[end:], "</graphics>")
	if closeIdx < 0 {
		return ""
	}
	body := domXML[end : end+closeIdx]
	i := strings.Index(body, "<listen")
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(body[i:], '>')
	if j < 0 {
		return ""
	}
	return body[i : i+j+1]
}

// setGraphicsListenXML sets the VNC console's listen address and port.
//
// The address is written in both places libvirt keeps it — the `listen`
// attribute on <graphics> and the <listen type='address'/> child element —
// because which of them a domain carries depends on who defined it, and the
// child element is the one libvirt honours. Rewriting only the attribute
// would look like it worked and change nothing.
//
// port <= 0 means autoport: libvirt assigns a free display on each start.
func setGraphicsListenXML(domXML, listen string, port int) (string, error) {
	tag, start, end, ok := vncOpenTag(domXML)
	if !ok {
		return "", fmt.Errorf("domain has no VNC graphics device")
	}

	newTag := tagDropAttr(tag, "listen")
	newTag = tagDropAttr(newTag, "port")
	newTag = tagDropAttr(newTag, "autoport")
	attrs := " listen='" + listen + "'"
	if port > 0 {
		attrs += " port='" + strconv.Itoa(port) + "' autoport='no'"
	} else {
		attrs += " autoport='yes'"
	}
	const head = "<graphics"
	newTag = head + attrs + newTag[len(head):]
	out := domXML[:start] + newTag + domXML[end:]

	// Re-locate the element in the rewritten document and replace its
	// <listen> child wholesale — a child of type='network' or type='socket'
	// would otherwise survive and keep overriding the address we just set.
	tag2, _, end2, ok := vncOpenTag(out)
	if !ok {
		return out, nil
	}
	child := vncListenChild(out, tag2, end2)
	if child == "" {
		return out, nil // attribute only; libvirt regenerates the child on define
	}
	idx := strings.Index(out[end2:], child)
	if idx < 0 {
		return out, nil
	}
	abs := end2 + idx
	replacement := "<listen type='address' address='" + listen + "'/>"
	return out[:abs] + replacement + out[abs+len(child):], nil
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

// VNCListenInfo reports the console's listen address and port from the
// domain's persistent config, plus whether libvirt picks the port itself.
func (c *Client) VNCListenInfo(domain string) (listen string, port int, autoport bool, err error) {
	out, err := c.run("dumpxml", "--inactive", "--security-info", domain)
	if err != nil {
		return "", 0, false, err
	}
	present, _, listen := vncGraphicsInfo(out)
	if !present {
		return "", 0, false, nil
	}
	port, autoport = vncGraphicsPort(out)
	return listen, port, autoport, nil
}

// VNCLiveListen reports the listen address the *running* qemu is actually
// bound to. As with the password, the persistent config is a statement of
// intent: a domain edited after boot still serves the old address until it
// restarts, and saying otherwise would put a "local only" label on a console
// that is still on the LAN.
func (c *Client) VNCLiveListen(domain string) (running bool, listen string, port int, err error) {
	state, err := c.State(domain)
	if err != nil {
		return false, "", 0, err
	}
	if state != "running" {
		return false, "", 0, nil
	}
	out, err := c.run("dumpxml", "--security-info", domain)
	if err != nil {
		return false, "", 0, err
	}
	present, _, listen := vncGraphicsInfo(out)
	if !present {
		return true, "", 0, nil
	}
	port, _ = vncGraphicsPort(out)
	return true, listen, port, nil
}

// SetVNCListen writes the console's listen address and port to the persistent
// domain config. port <= 0 selects autoport. Like SetVNCPassword this uses
// `virsh define`, so a running VM is untouched and the change lands on its
// next start.
func (c *Client) SetVNCListen(domain, listen string, port int) error {
	out, err := c.run("dumpxml", "--inactive", "--security-info", domain)
	if err != nil {
		return err
	}
	modified, err := setGraphicsListenXML(out, listen, port)
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
