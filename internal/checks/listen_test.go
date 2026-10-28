package checks

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/model"
)

// fakeProcNet writes socket tables into a temp dir. Lines use the real
// /proc/net layout: "sl local rem st ...".
func fakeProcNet(t *testing.T, tables map[string][]string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "net"), 0o700))
	for name, lines := range tables {
		content := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
		for i, l := range lines {
			content += "   " + string(rune('0'+i)) + ": " + l + " 00000000:00000000 00:00000000 00000000  1000        0 1 2 0 0\n"
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, "net", name), []byte(content), 0o600))
	}
	return dir
}

func TestListenChecker_Tables(t *testing.T) {
	dir := fakeProcNet(t, map[string][]string{
		"tcp": {
			"0100007F:1F90 00000000:0000 0A", // 127.0.0.1:8080 LISTEN
			"0A00000A:0050 00000000:0000 0A", // 10.0.0.10:80 LISTEN
			"0A00000A:0051 0B00000A:D431 01", // 10.0.0.10:81 ESTABLISHED (not listening)
		},
		"tcp6": {
			"00000000000000000000000000000000:01BB 00000000000000000000000000000000:0000 0A", // [::]:443 LISTEN
		},
		"udp": {
			"00000000:1283 00000000:0000 07", // 0.0.0.0:4739 bound
			"0A00000A:0202 00000000:0000 07", // 10.0.0.10:514 bound
		},
		"udp6": {
			"B80D0120000000000000000001000000:0035 00000000000000000000000000000000:0000 07", // [2001:db8::1]:53
		},
	})
	run := func(proto, host string, port uint16) Result {
		c := NewListenChecker(&config.ListenCheckConfig{Proto: proto, Host: host, Port: port, Procfs: dir})
		return c.Check(context.Background())
	}
	sockets := func(r Result) []string {
		d, ok := r.Details.(*model.ListenDetails)
		require.True(t, ok)
		return d.Sockets
	}

	t.Run("tcp listen on exact host", func(t *testing.T) {
		r := run("tcp", "10.0.0.10", 80)
		assert.True(t, r.Success, r.Detail)
		assert.Equal(t, []string{"10.0.0.10:80"}, sockets(r))
	})
	t.Run("tcp established socket does not count", func(t *testing.T) {
		r := run("tcp", "10.0.0.10", 81)
		assert.False(t, r.Success)
		assert.Equal(t, "listen tcp 10.0.0.10:81: no socket bound", r.Detail)
	})
	t.Run("tcp bound to another address", func(t *testing.T) {
		assert.False(t, run("tcp", "10.0.0.10", 8080).Success)
	})
	t.Run("ipv6 wildcard serves ipv4 host", func(t *testing.T) {
		r := run("tcp", "10.0.0.10", 443)
		assert.True(t, r.Success, r.Detail)
		assert.Equal(t, []string{"[::]:443"}, sockets(r))
	})
	t.Run("ipv4 wildcard does not serve ipv6 host", func(t *testing.T) {
		assert.False(t, run("udp", "2001:db8::2", 4739).Success)
		assert.True(t, run("udp", "10.0.0.10", 4739).Success)
	})
	t.Run("udp bound socket", func(t *testing.T) {
		r := run("udp", "10.0.0.10", 514)
		assert.True(t, r.Success, r.Detail)
		assert.Equal(t, "listen udp 10.0.0.10:514: bound on 10.0.0.10:514", r.Detail)
	})
	t.Run("udp ipv6 exact", func(t *testing.T) {
		assert.True(t, run("udp", "2001:db8::1", 53).Success)
		assert.False(t, run("udp", "2001:db8::2", 53).Success)
	})
	t.Run("empty host matches any address", func(t *testing.T) {
		r := run("tcp", "", 8080)
		assert.True(t, r.Success, r.Detail)
		assert.Equal(t, "listen tcp 8080: bound on 127.0.0.1:8080", r.Detail)
	})
	t.Run("missing family table is tolerated", func(t *testing.T) {
		require.NoError(t, os.Remove(filepath.Join(dir, "net", "udp6")))
		assert.True(t, run("udp", "10.0.0.10", 514).Success)
	})
	t.Run("netns_pid reads <procfs>/<pid>/net", func(t *testing.T) {
		pidDir := fakeProcNet(t, map[string][]string{"udp": {"00000000:0D05 00000000:0000 07"}}) // 0.0.0.0:3333
		root := t.TempDir()
		require.NoError(t, os.Symlink(pidDir, filepath.Join(root, "42")))
		c := NewListenChecker(&config.ListenCheckConfig{Proto: "udp", Host: "10.0.0.10", Port: 3333, Procfs: root, NetnsPID: 42})
		r := c.Check(context.Background())
		assert.True(t, r.Success, r.Detail)
		missing := NewListenChecker(&config.ListenCheckConfig{Proto: "udp", Port: 3333, Procfs: root, NetnsPID: 43})
		r = missing.Check(context.Background())
		assert.False(t, r.Success)
		assert.Contains(t, r.Detail, "procfs")
	})
	t.Run("invalid config", func(t *testing.T) {
		assert.False(t, run("tcp", "10.0.0.10", 0).Success)
		assert.False(t, run("sctp", "10.0.0.10", 80).Success)
		assert.Contains(t, run("tcp", "nope", 80).Detail, `host "nope"`)
	})
}

func TestListenChecker_RealSockets(t *testing.T) {
	if _, err := os.Stat("/proc/net/tcp"); err != nil {
		t.Skip("no /proc/net on this platform")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	tcpPort := uint16(ln.Addr().(*net.TCPAddr).Port)

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	udpPort := uint16(pc.LocalAddr().(*net.UDPAddr).Port)

	tcp := NewListenChecker(&config.ListenCheckConfig{Proto: "tcp", Host: "127.0.0.1", Port: tcpPort})
	assert.Equal(t, "listen", tcp.Type())
	assert.True(t, tcp.Check(context.Background()).Success)
	udp := NewListenChecker(&config.ListenCheckConfig{Proto: "udp", Host: "127.0.0.1", Port: udpPort})
	assert.True(t, udp.Check(context.Background()).Success)

	_ = ln.Close()
	_ = pc.Close()
	assert.False(t, tcp.Check(context.Background()).Success, "closed listener")
	assert.False(t, udp.Check(context.Background()).Success, "closed socket")
}
