// Package storage enumerates filesystem mountpoints suitable as snapshot
// targets. Read-only and pseudo filesystems are filtered out; duplicates
// (e.g. bind-mounts pointing at the same device) are deduplicated by source.
package storage

import (
	"bufio"
	"os"
	"sort"
	"strings"
	"syscall"
)

// Target is one writable mount the UI can offer as snapshot location.
type Target struct {
	Path       string `json:"path"`         // mountpoint
	FsType     string `json:"fstype"`       // ext4, exfat, cifs, nfs4, ...
	Source     string `json:"source"`       // device or remote spec
	AvailBytes uint64 `json:"avail_bytes"`  // free bytes via statfs
	TotalBytes uint64 `json:"total_bytes"`
	IsRemote   bool   `json:"is_remote"`    // cifs/nfs/sshfs/etc.
	Suggested  bool   `json:"suggested"`    // app-recommended default
}

// pseudo / kernel filesystems we never want to show.
var pseudoFS = map[string]bool{
	"proc": true, "sysfs": true, "tmpfs": true, "devtmpfs": true,
	"cgroup": true, "cgroup2": true, "devpts": true, "mqueue": true,
	"fusectl": true, "rpc_pipefs": true, "overlay": true, "overlayfs": true,
	"squashfs": true, "nfsd": true, "binfmt_misc": true, "configfs": true,
	"debugfs": true, "tracefs": true, "pstore": true, "bpf": true,
	"hugetlbfs": true, "ramfs": true, "autofs": true, "securityfs": true,
	"fuse.gvfsd-fuse": true,
}

// remoteFS marks paths that come from another host.
var remoteFS = map[string]bool{
	"cifs": true, "smbfs": true, "nfs": true, "nfs4": true,
	"sshfs": true, "fuse.sshfs": true, "fuse.rclone": true,
}

// systemPaths are mountpoints we hide because they hold OS state, not user data.
var systemPaths = []string{
	"/proc", "/sys", "/run", "/dev", "/boot", "/usr", "/etc",
	"/var/log", "/var/lib/bluetooth", "/var/lib/casaos", "/var/lib/casaos_data",
	"/var/lib/docker", "/var/lib/icewhale", "/var/lib/libvirt",
	"/var/lib/extensions", "/var/lib/rauc", "/var/lib/zerotier-one",
	"/var", "/opt", "/mnt/boot", "/mnt/overlay",
}

func isSystemPath(p string) bool {
	for _, s := range systemPaths {
		if p == s || strings.HasPrefix(p, s+"/") {
			return true
		}
	}
	return false
}

// List parses /proc/mounts and returns writable, user-facing mountpoints.
// The first target on the user-data device (typically /DATA) is marked Suggested.
func List() ([]Target, error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var raw []Target
	seenSource := map[string]bool{} // dedupe by source device
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		src, mnt, fstype, opts := fields[0], fields[1], fields[2], fields[3]
		if pseudoFS[fstype] {
			continue
		}
		if strings.HasPrefix(fstype, "fuse.") && fstype != "fuse.sshfs" && fstype != "fuse.rclone" {
			continue
		}
		if isSystemPath(mnt) {
			continue
		}
		// Read-only? Skip.
		ro := false
		for _, o := range strings.Split(opts, ",") {
			if o == "ro" {
				ro = true
				break
			}
		}
		if ro {
			continue
		}
		if seenSource[src] {
			// Same device, different mountpoint — keep the first (which is /DATA on ZimaOS).
			continue
		}
		seenSource[src] = true

		t := Target{
			Path:     mnt,
			FsType:   fstype,
			Source:   src,
			IsRemote: remoteFS[fstype],
		}
		// statfs for free/total
		var st syscall.Statfs_t
		if err := syscall.Statfs(mnt, &st); err == nil {
			t.AvailBytes = st.Bavail * uint64(st.Bsize)
			t.TotalBytes = st.Blocks * uint64(st.Bsize)
		}
		raw = append(raw, t)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// Sort: /DATA first, then by path.
	sort.Slice(raw, func(i, j int) bool {
		di := raw[i].Path == "/DATA"
		dj := raw[j].Path == "/DATA"
		if di != dj {
			return di
		}
		return raw[i].Path < raw[j].Path
	})
	// Mark /DATA (or first entry) as suggested.
	if len(raw) > 0 {
		raw[0].Suggested = true
	}
	return raw, nil
}
