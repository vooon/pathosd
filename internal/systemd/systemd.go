// Package systemd integrates with a systemd service manager when one is
// present: sd_notify readiness/status/stopping messages (Type=notify units)
// and detection of journald as the log sink. Everything is a no-op outside
// systemd, so the same binary runs unchanged in containers and on OpenWrt.
package systemd

import (
	"log/slog"
	"os"

	"github.com/coreos/go-systemd/v22/daemon"
	"github.com/coreos/go-systemd/v22/journal"
)

// NotifyAvailable reports whether systemd handed us a notify socket
// ($NOTIFY_SOCKET), i.e. the unit is Type=notify (or NotifyAccess= allows it).
func NotifyAvailable() bool {
	return os.Getenv("NOTIFY_SOCKET") != ""
}

// Notify sends one sd_notify(3) state string. It returns true when the
// message was delivered, false when there is no notify socket or sending
// failed (logged at debug level; never fatal).
func Notify(state string) bool {
	sent, err := daemon.SdNotify(false, state)
	if err != nil {
		slog.Debug("sd_notify failed", "state", state, "error", err)
		return false
	}
	return sent
}

// Ready tells systemd the service finished starting (READY=1).
func Ready() bool { return Notify(daemon.SdNotifyReady) }

// Stopping tells systemd the service began shutting down (STOPPING=1).
func Stopping() bool { return Notify(daemon.SdNotifyStopping) }

// Status sets the free-form status line shown by `systemctl status`.
func Status(text string) bool { return Notify("STATUS=" + text) }

// StderrIsJournal reports whether stderr is connected to the systemd journal
// ($JOURNAL_STREAM matches the stderr device/inode), in which case journald
// records its own timestamps and ours would be redundant.
func StderrIsJournal() bool {
	ok, err := journal.StderrIsJournalStream()
	return err == nil && ok
}
