//go:build linux || darwin || freebsd || openbsd || netbsd

package checks

import (
	"errors"
	"syscall"
)

// isConnRefused reports whether err is caused by an ICMP port-unreachable
// response delivered as ECONNREFUSED on a connected UDP socket.
func isConnRefused(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == syscall.ECONNREFUSED
}
