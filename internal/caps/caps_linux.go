//go:build linux

// Package caps raises file capabilities granted to the binary so pathosd can
// bind port 179 and open raw ICMP sockets without running as root.
package caps

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const capVersion3 = 0x20080522

// RaiseEffective copies the permitted capability set into the effective set
// for every thread of the process. A binary carrying file capabilities with
// only the permitted bit (setcap ... =+p) starts with an empty effective set;
// unlike the +ep form, exec never fails when the container's bounding set
// lacks one of them, so the daemon raises what it was actually granted.
// Returns true when something changed.
func RaiseEffective() (bool, error) {
	hdr := unix.CapUserHeader{Version: capVersion3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return false, fmt.Errorf("capget: %w", err)
	}
	if data[0].Effective == data[0].Permitted && data[1].Effective == data[1].Permitted {
		return false, nil
	}
	data[0].Effective = data[0].Permitted
	data[1].Effective = data[1].Permitted
	// capset applies to the calling thread only; AllThreadsSyscall runs it
	// on every thread of the Go runtime (cgo-free builds only).
	if _, _, errno := syscall.AllThreadsSyscall(unix.SYS_CAPSET, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0); errno != 0 {
		return false, fmt.Errorf("capset: %w", errno)
	}
	return true, nil
}
