package handlers

import "testing"

func TestValidName(t *testing.T) {
	good := []string{"0da603ce", "Arch_Linux", "vm.1", "my-vm+x", "53495d4e"}
	for _, s := range good {
		if !validName(s) {
			t.Errorf("validName(%q) = false, want true", s)
		}
	}
	// Empty, flag-injection and shell-unsafe inputs must be rejected.
	bad := []string{"", "-rf", "--metadata", "vm name", "vm;rm", "vm/..", "a`b", "x$y", "a\nb"}
	for _, s := range bad {
		if validName(s) {
			t.Errorf("validName(%q) = true, want false", s)
		}
	}
}

// Snapshot names are validated more loosely than domain names: a space is
// legal for libvirt, the UI has always let users type one, and v0.6.3 then
// refused to delete the result.
func TestValidSnapshotName(t *testing.T) {
	good := []string{"pre-update", "before Windows update", "v1.0", "snap_2", "a+b"}
	for _, s := range good {
		if !validSnapshotName(s) {
			t.Errorf("validSnapshotName(%q) = false, want true", s)
		}
	}
	bad := []string{
		"",            // empty
		"-x",          // flag injection
		" lead",       // invisible leading space
		"trail ",      // invisible trailing space
		"two  spaces", // breaks the snapshot-list column parser
		"..",          // traversal
		"a..b",        // traversal
		"a/b", "a\\b", // path separators
		"a;b", "a$b", // shell metacharacters
		"a\nb", "a\tb", // control characters
	}
	for _, s := range bad {
		if validSnapshotName(s) {
			t.Errorf("validSnapshotName(%q) = true, want false", s)
		}
	}
}

func TestValidHexID(t *testing.T) {
	good := []string{"1d6b", "0bda", "ABCD", "0000", "ffff"}
	for _, s := range good {
		if !validHexID(s) {
			t.Errorf("validHexID(%q) = false, want true", s)
		}
	}
	bad := []string{"", "1d6", "1d6b0", "ghij", "0x12", "12 4"}
	for _, s := range bad {
		if validHexID(s) {
			t.Errorf("validHexID(%q) = true, want false", s)
		}
	}
}

func TestForbiddenSnapshotDir(t *testing.T) {
	forbidden := []string{
		"/", "/usr", "/usr/lib", "/var", "/var/lib/x", "/etc",
		"/proc", "/tmp", "/boot", "/dev", "/sys", "/run",
	}
	for _, p := range forbidden {
		if !forbiddenSnapshotDir(p) {
			t.Errorf("forbiddenSnapshotDir(%q) = false, want true", p)
		}
	}
	allowed := []string{
		"/DATA", "/DATA/AppData/zima-vm-extras/snapshots",
		"/media/sda/snaps", "/mnt/backup",
	}
	for _, p := range allowed {
		if forbiddenSnapshotDir(p) {
			t.Errorf("forbiddenSnapshotDir(%q) = true, want false", p)
		}
	}
}
