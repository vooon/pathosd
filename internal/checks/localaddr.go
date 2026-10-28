package checks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"
)

// LocalAddress is one address configured on a local interface.
type LocalAddress struct {
	Interface string
	Addr      netip.Addr
}

// ListLocalAddresses returns every IP address configured on the host's
// interfaces.
func ListLocalAddresses() ([]LocalAddress, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []LocalAddress
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("interface %s: %w", iface.Name, err)
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			default:
				continue
			}
			if addr, ok := netip.AddrFromSlice(ip); ok {
				out = append(out, LocalAddress{Interface: iface.Name, Addr: addr.Unmap()})
			}
		}
	}
	return out, nil
}

// LocalAddressGuard reports failure while the VIP address is not configured
// on a local interface (optionally a specific one), so the route is never
// announced for an address this host cannot serve. One guard serves all
// checks of a VIP: Wrap returns a Checker that runs the inner check only once
// the address is present, and state-change logging happens once per VIP.
type LocalAddressGuard struct {
	vipName string
	prefix  string
	addr    netip.Addr
	iface   string
	list    func() ([]LocalAddress, error)
	onState func(assigned bool)

	mu       sync.Mutex
	known    bool
	assigned bool
}

// NewLocalAddressGuard builds a guard for the VIP prefix. iface may be empty
// to accept any interface. onState is called whenever the assigned state is
// observed to change (and on the first observation); it may be nil.
func NewLocalAddressGuard(vipName, prefix, iface string, onState func(assigned bool)) (*LocalAddressGuard, error) {
	p, err := netip.ParsePrefix(prefix)
	if err != nil {
		return nil, fmt.Errorf("invalid prefix %q: %w", prefix, err)
	}
	return &LocalAddressGuard{
		vipName: vipName,
		prefix:  prefix,
		addr:    p.Addr().Unmap(),
		iface:   iface,
		list:    ListLocalAddresses,
		onState: onState,
	}, nil
}

// Wrap returns a Checker that verifies the VIP address is present before
// running inner.
func (g *LocalAddressGuard) Wrap(inner Checker) Checker {
	return &guardedChecker{guard: g, inner: inner}
}

type guardedChecker struct {
	guard *LocalAddressGuard
	inner Checker
}

func (c *guardedChecker) Type() string { return c.inner.Type() }

func (c *guardedChecker) Check(ctx context.Context) Result {
	if r, ok := c.guard.check(); !ok {
		return r
	}
	return c.inner.Check(ctx)
}

// check returns (failure result, false) while the address is missing, or
// (zero, true) when the inner check may run.
func (g *LocalAddressGuard) check() (Result, bool) {
	start := time.Now()
	addrs, err := g.list()
	if err != nil {
		return Result{
			Success:  false,
			Detail:   "listing local addresses: " + err.Error(),
			Duration: time.Since(start),
			Err:      err,
		}, false
	}
	assigned := false
	for _, a := range addrs {
		if a.Addr == g.addr && (g.iface == "" || a.Interface == g.iface) {
			assigned = true
			break
		}
	}
	g.observe(assigned)
	if !assigned {
		detail := g.missingDetail()
		return Result{
			Success:  false,
			Detail:   detail,
			Duration: time.Since(start),
			Err:      errors.New(detail),
		}, false
	}
	return Result{}, true
}

func (g *LocalAddressGuard) missingDetail() string {
	if g.iface != "" {
		return fmt.Sprintf("VIP address %s is not configured on interface %s", g.addr, g.iface)
	}
	return fmt.Sprintf("VIP address %s is not configured on any interface", g.addr)
}

func (g *LocalAddressGuard) observe(assigned bool) {
	g.mu.Lock()
	changed := !g.known || g.assigned != assigned
	g.known = true
	g.assigned = assigned
	g.mu.Unlock()
	if !changed {
		return
	}
	attrs := []any{"vip", g.vipName, "prefix", g.prefix}
	if g.iface != "" {
		attrs = append(attrs, "interface", g.iface)
	}
	if assigned {
		slog.Info("VIP address present on host", attrs...)
	} else {
		slog.Warn("VIP address missing on host; check fails until it is configured", attrs...)
	}
	if g.onState != nil {
		g.onState(assigned)
	}
}
