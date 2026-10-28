package checks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/procfs"

	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/model"
)

// ListenChecker passes when a local socket is bound to the configured port:
// a TCP socket in LISTEN state, or any bound UDP socket. It reads the kernel
// socket tables (net/tcp, tcp6, udp, udp6 under procfs) and sends nothing,
// which makes it suitable for services that log every unexpected datagram
// (IPFIX, syslog, ...).
//
// /proc/net is a symlink to /proc/self/net, so by default the check sees the
// network namespace pathosd runs in. With NetnsPID set it reads
// <procfs>/<pid>/net instead, the namespace of that process, which is how a
// container with the host's /proc mounted at /host/proc observes the host.
type ListenChecker struct {
	cfg config.ListenCheckConfig
}

func NewListenChecker(cfg *config.ListenCheckConfig) *ListenChecker {
	c := &ListenChecker{cfg: *cfg}
	if c.cfg.Procfs == "" {
		c.cfg.Procfs = procfs.DefaultMountPoint
	}
	return c
}

func (c *ListenChecker) Type() string { return "listen" }

// tcpListen is the st column value of a listening TCP socket.
const tcpListen = 0x0A

// procRoot returns the directory whose net/ subdirectory holds the socket
// tables to read.
func (c *ListenChecker) procRoot() string {
	if c.cfg.NetnsPID > 0 {
		return filepath.Join(c.cfg.Procfs, strconv.Itoa(c.cfg.NetnsPID))
	}
	return c.cfg.Procfs
}

func (c *ListenChecker) Check(_ context.Context) Result {
	start := time.Now()

	if c.cfg.Port == 0 {
		err := fmt.Errorf("listen check: port is required")
		return Result{Duration: time.Since(start), Err: err, Detail: err.Error()}
	}
	proto := c.cfg.Proto
	if proto != "tcp" && proto != "udp" {
		err := fmt.Errorf("listen check: proto must be tcp or udp, got %q", proto)
		return Result{Duration: time.Since(start), Err: err, Detail: err.Error()}
	}
	var host netip.Addr
	if c.cfg.Host != "" {
		a, err := netip.ParseAddr(c.cfg.Host)
		if err != nil {
			return Result{Duration: time.Since(start), Err: err, Detail: fmt.Sprintf("listen check: host %q: %v", c.cfg.Host, err)}
		}
		host = a.Unmap()
	}

	pfs, err := procfs.NewFS(c.procRoot())
	if err != nil {
		return Result{Duration: time.Since(start), Err: err, Detail: fmt.Sprintf("listen check: procfs %s: %v", c.procRoot(), err)}
	}

	// procfs deprecates the TCP table readers in favour of netlink sock_diag,
	// but netlink only answers for the caller's own network namespace;
	// reading <procfs>/<pid>/net is what makes netns_pid work from a container.
	var tables []func() (procfs.NetIPSocket, error)
	if proto == "tcp" {
		tables = []func() (procfs.NetIPSocket, error){
			func() (procfs.NetIPSocket, error) { t, err := pfs.NetTCP(); return procfs.NetIPSocket(t), err },  //nolint:staticcheck // see above
			func() (procfs.NetIPSocket, error) { t, err := pfs.NetTCP6(); return procfs.NetIPSocket(t), err }, //nolint:staticcheck // see above
		}
	} else {
		tables = []func() (procfs.NetIPSocket, error){
			func() (procfs.NetIPSocket, error) { t, err := pfs.NetUDP(); return procfs.NetIPSocket(t), err },
			func() (procfs.NetIPSocket, error) { t, err := pfs.NetUDP6(); return procfs.NetIPSocket(t), err },
		}
	}

	var sockets []string
	for _, read := range tables {
		lines, err := read()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // address family not enabled on this host
			}
			return Result{Duration: time.Since(start), Err: err, Detail: fmt.Sprintf("listen check: %v", err)}
		}
		sockets = append(sockets, c.match(lines, proto == "tcp", host)...)
	}

	target := strconv.Itoa(int(c.cfg.Port))
	if c.cfg.Host != "" {
		target = c.cfg.Host + ":" + target
	}
	details := &model.ListenDetails{Sockets: sockets}
	if len(sockets) == 0 {
		detail := fmt.Sprintf("listen %s %s: no socket bound", proto, target)
		return Result{Duration: time.Since(start), Err: errors.New(detail), Detail: detail, Details: details}
	}
	return Result{
		Success:  true,
		Duration: time.Since(start),
		Detail:   fmt.Sprintf("listen %s %s: bound on %s", proto, target, strings.Join(sockets, ", ")),
		Details:  details,
	}
}

// match returns the local addresses of sockets bound to the port that serve
// host: an exact match or a wildcard bind. For TCP only LISTEN sockets count;
// an unspecified host matches any local address.
func (c *ListenChecker) match(lines procfs.NetIPSocket, tcp bool, host netip.Addr) []string {
	var out []string
	for _, l := range lines {
		if l == nil || l.LocalPort != uint64(c.cfg.Port) {
			continue
		}
		if tcp && l.St != tcpListen {
			continue
		}
		addr, ok := netip.AddrFromSlice(l.LocalAddr)
		if !ok {
			continue
		}
		addr = addr.Unmap()
		if host.IsValid() && !addr.IsUnspecified() && addr != host {
			continue
		}
		// A dual-stack wildcard ("::") also accepts IPv4; an IPv4 wildcard
		// cannot serve an IPv6 host.
		if host.IsValid() && host.Is6() && addr.IsUnspecified() && addr.Is4() {
			continue
		}
		out = append(out, netip.AddrPortFrom(addr, c.cfg.Port).String())
	}
	return out
}
