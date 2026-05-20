// Package watchdog self-installs a boot-time watchdog timer on the
// persistent root filesystem (/etc/systemd/system/).
//
// Why this is needed: this daemon's systemd unit lives inside the sysext
// image. At boot, multi-user.target resolves its WantedBy= symlinks before
// systemd-sysext.service has finished merging /usr — so the in-sysext unit
// can be missed entirely (left inactive, with no error logged). A timer
// unit on the persistent /etc with OnBootSec= fires after the overlay is
// guaranteed merged and starts the service on demand. /etc survives both
// reboots and `systemd-sysext refresh`. This mirrors the workaround used by
// the zima_cron module.
package watchdog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// systemctl runs `systemctl <args...>` with a 30s timeout so a hung or slow
// systemd cannot block the caller indefinitely.
func systemctl(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", args...).Run()
}

const unitDir = "/etc/systemd/system"

const serviceName = "zima-vm-extras-watchdog.service"
const timerName = "zima-vm-extras-watchdog.timer"

const serviceUnit = `[Unit]
Description=Start zima-vm-extras if the sysext unit was missed at boot

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'systemctl is-active --quiet zima-vm-extras.service || systemctl start zima-vm-extras.service'
`

const timerUnit = `[Unit]
Description=Ensure zima-vm-extras is running after the sysext overlay is merged

[Timer]
OnBootSec=15

[Install]
WantedBy=timers.target
`

// EnsureInstalled writes the watchdog units if missing or outdated, reloads
// systemd, and enables the timer. Best effort: it returns the first error
// but the caller should treat failure as non-fatal (the daemon is already
// running — the watchdog only matters for the *next* boot).
func EnsureInstalled(logf func(string, ...any)) error {
	changed := false
	for name, content := range map[string]string{
		serviceName: serviceUnit,
		timerName:   timerUnit,
	} {
		path := filepath.Join(unitDir, name)
		if cur, err := os.ReadFile(path); err == nil && string(cur) == content {
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		if err := systemctl("daemon-reload"); err != nil {
			return err
		}
		if logf != nil {
			logf("watchdog units installed in %s", unitDir)
		}
	}
	// enable --now is idempotent — safe to run on every daemon start.
	return systemctl("enable", "--now", timerName)
}

// Remove deletes the watchdog units and disables the timer. Used by uninstall.
func Remove() error {
	_ = systemctl("disable", "--now", timerName)
	var firstErr error
	for _, name := range []string{serviceName, timerName} {
		if err := os.Remove(filepath.Join(unitDir, name)); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	_ = systemctl("daemon-reload")
	return firstErr
}
