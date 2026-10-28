package config

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every ```yaml block in the docs that starts with "vips:" (or "paths:")
// must be a loadable config fragment, and every block that starts with
// "schema:" a complete config, so the guides cannot drift from the validator.
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
	var files []string
	for _, pattern := range []string{"../../README.md", "../../docs/*.md", "../../docs/checks/*.md"} {
		matches, err := filepath.Glob(pattern)
		require.NoError(t, err)
		files = append(files, matches...)
	}
	require.NotEmpty(t, files)

	block := regexp.MustCompile("(?s)```yaml\n(.*?)```")
	examples := 0
	for _, f := range files {
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		for i, m := range block.FindAllSubmatch(src, -1) {
			body := m[1]
			switch {
			case bytes.HasPrefix(body, []byte("schema:")):
				// complete config, no header needed
			case bytes.HasPrefix(body, []byte("vips:")), bytes.HasPrefix(body, []byte("paths:")):
				body = append([]byte(header), body...)
			default:
				continue
			}
			examples++
			name := strings.TrimPrefix(f, "../../") + "#" + strconv.Itoa(i)
			t.Run(name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "example.yaml")
				require.NoError(t, os.WriteFile(path, body, 0o600))
				cfg, err := Load(path)
				require.NoError(t, err, "example %d in %s:\n%s", i, f, body)
				require.Empty(t, Validate(cfg), "example %d in %s", i, f)
			})
		}
	}
	require.Greater(t, examples, 40, "expected the guides to carry many examples")
}
