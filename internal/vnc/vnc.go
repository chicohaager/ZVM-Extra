// Package vnc keeps each VM's VNC console password applied.
//
// ZimaOS's ZVM generates every VM's VNC console with no password and a
// LAN-wide listen address, leaving the console open to anyone on the network.
// This package persists a chosen password per VM; a reconciler re-applies it
// whenever ZVM strips it from the domain config on a re-save — the same
// desired-state pattern the usb and pci packages use for passthroughs.
package vnc

// ValidPassword reports whether pw is acceptable as a VNC console password.
//
// VNC authentication only uses the first 8 bytes of the password, so longer
// values are pointless and rejected outright to avoid surprising the user.
// Quote and XML-significant characters are disallowed so the value is always
// safe to embed verbatim in the domain XML's passwd='…' attribute.
func ValidPassword(pw string) bool {
	if len(pw) < 1 || len(pw) > 8 {
		return false
	}
	for _, r := range pw {
		if r < 0x20 || r > 0x7e { // printable ASCII only
			return false
		}
		switch r {
		case '\'', '"', '<', '>', '&':
			return false
		}
	}
	return true
}
