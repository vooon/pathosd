package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const diffBase = `schema: v1
router: {asn: 65001, router_id: 192.0.2.1, local_address: 192.0.2.1}
api: {listen: "127.0.0.1:59179"}
logging: {level: info}
bgp:
  neighbors:
    - {name: p, address: 192.0.2.2, peer_asn: 65002}
vips:
  - name: web
    prefix: 10.9.0.1/32
    checks_policy: "http || tcp"
    checks:
      - {type: http, interval: 2s, timeout: 1s, http: {url: /healthz, host: 127.0.0.1, port: 8080}}
      - {type: tcp, tcp: {host: 127.0.0.1, port: 8080}}
    policy: {fail_action: withdraw}
  - name: dns
    prefix: 10.9.0.2/32
    check: {type: tcp, tcp: {host: 127.0.0.1, port: 53}}
    policy: {fail_action: withdraw}
`

func loadDiff(t *testing.T, yaml string) *Config {
	t.Helper()
	cfg, err := loadBytes([]byte(yaml), "x.yaml")
	require.NoError(t, err)
	return cfg
}

func TestRestartRequired(t *testing.T) {
	base := loadDiff(t, diffBase)
	assert.Empty(t, RestartRequired(base, loadDiff(t, diffBase)))

	cases := map[string][]string{
		"router: {asn: 65009, router_id: 192.0.2.1, local_address: 192.0.2.1}": {"router"},
		`api: {listen: "127.0.0.1:1234"}`:                                      {"api"},
		"logging: {level: debug}":                                              nil,
		"logging: {level: info, format: json}":                                 {"logging (other than level)"},
		"    - {name: p, address: 192.0.2.3, peer_asn: 65002}":                 {"bgp.neighbors"},
	}
	for line, want := range cases {
		t.Run(line, func(t *testing.T) {
			var y string
			switch {
			case line[:4] == "    ":
				y = replaceLine(diffBase, "    - {name: p, address: 192.0.2.2, peer_asn: 65002}", line)
			case line[:7] == "router:":
				y = replaceLine(diffBase, "router: {asn: 65001, router_id: 192.0.2.1, local_address: 192.0.2.1}", line)
			case line[:4] == "api:":
				y = replaceLine(diffBase, `api: {listen: "127.0.0.1:59179"}`, line)
			default:
				y = replaceLine(diffBase, "logging: {level: info}", line)
			}
			assert.Equal(t, want, RestartRequired(base, loadDiff(t, y)))
		})
	}

	t.Run("bgp listen", func(t *testing.T) {
		y := replaceLine(diffBase, "bgp:\n", "bgp:\n  listen_port: -1\n")
		assert.Equal(t, []string{"bgp"}, RestartRequired(base, loadDiff(t, y)))
	})
}

func replaceLine(s, old, repl string) string {
	n := 0
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			n++
		}
	}
	if n != 1 {
		panic("replaceLine: " + old + " occurs " + string(rune('0'+n)) + " times")
	}
	i := indexOf(s, old)
	return s[:i] + repl + s[i+len(old):]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestDiffVIPs(t *testing.T) {
	base := loadDiff(t, diffBase)

	t.Run("identical", func(t *testing.T) {
		d := DiffVIPs(base.VIPs, loadDiff(t, diffBase).VIPs)
		assert.True(t, d.IsEmpty())
		assert.Equal(t, []string{"web", "dns"}, d.Unchanged)
	})

	t.Run("check tweak and policy", func(t *testing.T) {
		y := replaceLine(diffBase, "interval: 2s, timeout: 1s", "interval: 3s, timeout: 1s")
		y = replaceLine(y, `checks_policy: "http || tcp"`, `checks_policy: "http && tcp"`)
		d := DiffVIPs(base.VIPs, loadDiff(t, y).VIPs)
		require.Len(t, d.Changed, 1)
		ch := d.Changed[0]
		assert.Equal(t, "web", ch.New.Name)
		assert.True(t, ch.PolicyChanged)
		require.Len(t, ch.ChecksChanged, 1)
		assert.Equal(t, "http", ch.ChecksChanged[0].EffectiveName())
		assert.Empty(t, ch.ChecksRemoved)
		assert.Equal(t, []string{"dns"}, d.Unchanged)
	})

	t.Run("policy only", func(t *testing.T) {
		y := replaceLine(diffBase, "    policy: {fail_action: withdraw}\n  - name: dns", "    policy: {fail_action: withdraw, announce: {as_path_prepend: 2}}\n  - name: dns")
		d := DiffVIPs(base.VIPs, loadDiff(t, y).VIPs)
		require.Len(t, d.Changed, 1)
		assert.True(t, d.Changed[0].PolicyChanged)
		assert.Empty(t, d.Changed[0].ChecksChanged)
	})

	t.Run("guard change touches every check", func(t *testing.T) {
		y := replaceLine(diffBase, "    prefix: 10.9.0.1/32\n", "    prefix: 10.9.0.1/32\n    require_local_address: true\n")
		d := DiffVIPs(base.VIPs, loadDiff(t, y).VIPs)
		require.Len(t, d.Changed, 1)
		assert.False(t, d.Changed[0].PolicyChanged)
		assert.Len(t, d.Changed[0].ChecksChanged, 2)
	})

	t.Run("check removed and added", func(t *testing.T) {
		y := replaceLine(diffBase, "      - {type: tcp, tcp: {host: 127.0.0.1, port: 8080}}\n", "      - {type: tcp, name: alt, tcp: {host: 127.0.0.1, port: 8081}}\n")
		y = replaceLine(y, `checks_policy: "http || tcp"`, `checks_policy: "http || alt"`)
		d := DiffVIPs(base.VIPs, loadDiff(t, y).VIPs)
		require.Len(t, d.Changed, 1)
		require.Len(t, d.Changed[0].ChecksChanged, 1)
		assert.Equal(t, "alt", d.Changed[0].ChecksChanged[0].EffectiveName())
		require.Len(t, d.Changed[0].ChecksRemoved, 1)
		assert.Equal(t, "tcp", d.Changed[0].ChecksRemoved[0].EffectiveName())
	})

	t.Run("prefix change is remove plus add", func(t *testing.T) {
		y := replaceLine(diffBase, "prefix: 10.9.0.2/32", "prefix: 10.9.0.3/32")
		d := DiffVIPs(base.VIPs, loadDiff(t, y).VIPs)
		require.Len(t, d.Removed, 1)
		require.Len(t, d.Added, 1)
		assert.Equal(t, "10.9.0.2/32", d.Removed[0].Prefix)
		assert.Equal(t, "10.9.0.3/32", d.Added[0].Prefix)
		assert.Empty(t, d.Changed)
	})

	t.Run("vip removed and added", func(t *testing.T) {
		y := replaceLine(diffBase, "  - name: dns\n    prefix: 10.9.0.2/32\n", "  - name: api\n    prefix: 10.9.0.9/32\n")
		d := DiffVIPs(base.VIPs, loadDiff(t, y).VIPs)
		require.Len(t, d.Removed, 1)
		assert.Equal(t, "dns", d.Removed[0].Name)
		require.Len(t, d.Added, 1)
		assert.Equal(t, "api", d.Added[0].Name)
	})
}
