package systemd

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func listenNotify(t *testing.T) (*net.UnixConn, func() string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	t.Setenv("NOTIFY_SOCKET", path)
	return conn, func() string {
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
		buf := make([]byte, 512)
		n, err := conn.Read(buf)
		require.NoError(t, err)
		return string(buf[:n])
	}
}

func TestNotify_WithSocket(t *testing.T) {
	_, recv := listenNotify(t)
	assert.True(t, NotifyAvailable())

	assert.True(t, Ready())
	assert.Equal(t, "READY=1", recv())

	assert.True(t, Status("VIPs: 1 announced"))
	assert.Equal(t, "STATUS=VIPs: 1 announced", recv())

	assert.True(t, Stopping())
	assert.Equal(t, "STOPPING=1", recv())
}

func TestNotify_WithoutSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	assert.False(t, NotifyAvailable())
	assert.False(t, Ready())
	assert.False(t, Status("x"))
	assert.False(t, Stopping())
}

func TestStderrIsJournal_NotUnderSystemd(t *testing.T) {
	t.Setenv("JOURNAL_STREAM", "")
	assert.False(t, StderrIsJournal())
}
