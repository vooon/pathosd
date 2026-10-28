package checks

import (
	"context"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubChecker struct{ calls int }

func (s *stubChecker) Check(context.Context) Result {
	s.calls++
	return Result{Success: true, Detail: "inner ok"}
}
func (s *stubChecker) Type() string { return "stub" }

func TestLocalAddressGuard(t *testing.T) {
	addrs := []LocalAddress{
		{Interface: "lo", Addr: netip.MustParseAddr("127.0.0.1")},
		{Interface: "dummy-vip", Addr: netip.MustParseAddr("10.9.0.1")},
		{Interface: "dummy-vip", Addr: netip.MustParseAddr("2001:db8::1")},
	}
	newGuard := func(t *testing.T, prefix, iface string, inner Checker) (Checker, *[]bool) {
		t.Helper()
		var states []bool
		g, err := NewLocalAddressGuard("vip", prefix, iface, func(a bool) { states = append(states, a) })
		require.NoError(t, err)
		g.list = func() ([]LocalAddress, error) { return addrs, nil }
		return g.Wrap(inner), &states
	}

	t.Run("present on any interface runs inner check", func(t *testing.T) {
		inner := &stubChecker{}
		g, states := newGuard(t, "10.9.0.1/32", "", inner)
		r := g.Check(context.Background())
		assert.True(t, r.Success)
		assert.Equal(t, "inner ok", r.Detail)
		assert.Equal(t, 1, inner.calls)
		assert.Equal(t, []bool{true}, *states)
	})

	t.Run("present on the named interface", func(t *testing.T) {
		inner := &stubChecker{}
		g, _ := newGuard(t, "2001:db8::1/128", "dummy-vip", inner)
		assert.True(t, g.Check(context.Background()).Success)
		assert.Equal(t, 1, inner.calls)
	})

	t.Run("present but on another interface fails without running inner", func(t *testing.T) {
		inner := &stubChecker{}
		g, states := newGuard(t, "10.9.0.1/32", "eth0", inner)
		r := g.Check(context.Background())
		assert.False(t, r.Success)
		assert.Equal(t, "VIP address 10.9.0.1 is not configured on interface eth0", r.Detail)
		assert.Error(t, r.Err)
		assert.Equal(t, 0, inner.calls)
		assert.Equal(t, []bool{false}, *states)
	})

	t.Run("missing fails and state callback fires only on change", func(t *testing.T) {
		inner := &stubChecker{}
		g, states := newGuard(t, "10.9.0.2/32", "", inner)
		for range 3 {
			r := g.Check(context.Background())
			assert.False(t, r.Success)
			assert.Equal(t, "VIP address 10.9.0.2 is not configured on any interface", r.Detail)
		}
		assert.Equal(t, 0, inner.calls)
		assert.Equal(t, []bool{false}, *states, "one notification for three identical observations")

		addrs = append(addrs, LocalAddress{Interface: "dummy-vip", Addr: netip.MustParseAddr("10.9.0.2")})
		assert.True(t, g.Check(context.Background()).Success)
		assert.Equal(t, []bool{false, true}, *states)
	})

	t.Run("type is the inner type", func(t *testing.T) {
		g, _ := newGuard(t, "10.9.0.1/32", "", &stubChecker{})
		assert.Equal(t, "stub", g.Type())
	})

	t.Run("invalid prefix", func(t *testing.T) {
		_, err := NewLocalAddressGuard("vip", "nope", "", nil)
		assert.Error(t, err)
	})
}

func TestListLocalAddresses_Loopback(t *testing.T) {
	addrs, err := ListLocalAddresses()
	require.NoError(t, err)
	var found bool
	for _, a := range addrs {
		if a.Addr == netip.MustParseAddr("127.0.0.1") {
			found = true
			assert.NotEmpty(t, a.Interface)
		}
	}
	assert.True(t, found, "127.0.0.1 should be configured on the loopback interface")

	// The real lister through the guard: loopback is present, a TEST-NET address is not.
	g, err := NewLocalAddressGuard("vip", "127.0.0.1/32", "", nil)
	require.NoError(t, err)
	assert.True(t, g.Wrap(&stubChecker{}).Check(context.Background()).Success)
	g, err = NewLocalAddressGuard("vip", "203.0.113.7/32", "", nil)
	require.NoError(t, err)
	assert.False(t, g.Wrap(&stubChecker{}).Check(context.Background()).Success)
}
