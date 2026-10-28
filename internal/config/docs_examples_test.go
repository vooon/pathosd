package config

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every ```yaml block in docs/checks/*.md that starts with "vips:" (or a
// top-level key followed by vips) must be a loadable config fragment, so
// the guides cannot drift from the validator.
func TestDocsCheckExamplesValidate(t *testing.T) {
	t.Setenv("DB_PASSWORD", "example")
	t.Setenv("KAFKA_PASSWORD", "example")
	t.Setenv("API_TOKEN", "example")
	t.Setenv("PROXY_PASSWORD", "example")

	const header = `schema: v1
router:
  asn: 65001
  router_id: 192.0.2.1
  local_address: 192.0.2.1
  local_address_ipv6: 2001:db8::1
bgp:
  neighbors:
    - name: peer
      address: 192.0.2.2
      peer_asn: 65002
`
	files, err := filepath.Glob("../../docs/checks/*.md")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	block := regexp.MustCompile("(?s)```yaml\n(.*?)```")
	examples := 0
	for _, f := range files {
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		for i, m := range block.FindAllSubmatch(src, -1) {
			body := m[1]
			if !bytes.HasPrefix(body, []byte("vips:")) && !bytes.HasPrefix(body, []byte("paths:")) {
				continue
			}
			examples++
			name := filepath.Base(f) + "#" + strconv.Itoa(i)
			t.Run(name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "example.yaml")
				require.NoError(t, os.WriteFile(path, append([]byte(header), body...), 0o600))
				cfg, err := Load(path)
				require.NoError(t, err, "example %d in %s:\n%s", i, f, body)
				require.Empty(t, Validate(cfg), "example %d in %s", i, f)
			})
		}
	}
	require.Greater(t, examples, 40, "expected the guides to carry many examples")
}
