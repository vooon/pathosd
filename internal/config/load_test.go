package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const minimalYAML = `
schema: v1
router:
  asn: 65001
  router_id: 10.0.0.1
api:
  listen: ":8080"
bgp:
  neighbors:
    - name: spine-1
      address: 10.0.0.254
      peer_asn: 65000
vips:
  - name: web-vip
    prefix: 10.10.1.1/32
    check:
      type: http
      http:
        url: /healthz
        method: GET
    policy:
      fail_action: withdraw
`

const minimalTOML = `
schema = "v1"

[router]
asn = 65001
router_id = "10.0.0.1"

[api]
listen = ":8080"

[[bgp.neighbors]]
name = "spine-1"
address = "10.0.0.254"
peer_asn = 65000

[[vips]]
name = "web-vip"
prefix = "10.10.1.1/32"

  [vips.check]
  type = "http"

    [vips.check.http]
    url = "/healthz"
    method = "GET"

  [vips.policy]
  fail_action = "withdraw"
`

func TestParse_YAML(t *testing.T) {
	cfg, err := Parse([]byte(minimalYAML), "yaml")
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "v1", cfg.Schema)
	assert.Equal(t, uint32(65001), cfg.Router.ASN)
	assert.Equal(t, "10.0.0.1", cfg.Router.RouterID)
	require.Len(t, cfg.BGP.Neighbors, 1)
	assert.Equal(t, "spine-1", cfg.BGP.Neighbors[0].Name)
	require.Len(t, cfg.VIPs, 1)
	assert.Equal(t, "web-vip", cfg.VIPs[0].Name)
	assert.Equal(t, "10.10.1.1/32", cfg.VIPs[0].Prefix)
}

func TestParse_TOML(t *testing.T) {
	cfg, err := Parse([]byte(minimalTOML), "toml")
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "v1", cfg.Schema)
	assert.Equal(t, uint32(65001), cfg.Router.ASN)
	assert.Equal(t, "10.0.0.1", cfg.Router.RouterID)
	require.Len(t, cfg.BGP.Neighbors, 1)
	assert.Equal(t, "spine-1", cfg.BGP.Neighbors[0].Name)
	require.Len(t, cfg.VIPs, 1)
	assert.Equal(t, "web-vip", cfg.VIPs[0].Name)
}

const minimalJSON = `{"schema":"v1","router":{"asn":65001,"router_id":"10.0.0.1"},"api":{"listen":":8080"},"bgp":{"neighbors":[{"name":"spine-1","address":"10.0.0.254","peer_asn":65000}]},"vips":[{"name":"web-vip","prefix":"10.10.1.1/32","check":{"type":"http","http":{"url":"/healthz","method":"GET"}},"policy":{"fail_action":"withdraw"}}]}`

func TestParse_JSON(t *testing.T) {
	cfg, err := Parse([]byte(minimalJSON), "yaml")
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "v1", cfg.Schema)
	assert.Equal(t, uint32(65001), cfg.Router.ASN)
	require.Len(t, cfg.VIPs, 1)
	assert.Equal(t, "web-vip", cfg.VIPs[0].Name)
}

func TestParse_UnsupportedFormat(t *testing.T) {
	_, err := Parse([]byte("{}"), "xml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported config format")
}

func TestParse_EmptyFormat(t *testing.T) {
	_, err := Parse([]byte(""), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported config format")
}

func TestParse_InvalidYAML(t *testing.T) {
	_, err := Parse([]byte("{\ninvalid yaml ["), "yaml")
	require.Error(t, err)
}

func TestParse_InvalidTOML(t *testing.T) {
	_, err := Parse([]byte("[[invalid toml"), "toml")
	require.Error(t, err)
}

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"config.yaml", "yaml"},
		{"config.yml", "yaml"},
		{"config.YAML", "yaml"},
		{"config.YML", "yaml"},
		{"config.toml", "toml"},
		{"config.TOML", "toml"},
		{"/etc/pathosd/pathosd.yaml", "yaml"},
		{"/etc/pathosd/pathosd.toml", "toml"},
		{"config.json", "yaml"},
		{"config.JSON", "yaml"},
		{"config", ""},
		{"config.ini", ".ini"},
	}

	for _, tc := range tests {
		// t.Run(tc.path, func(t *testing.T) {
		assert.Equal(t, tc.want, detectFormat(tc.path), "path: %s", tc.path)
		// })
	}
}

func TestLoad_ExampleYAML(t *testing.T) {
	cfg, err := Load("../../examples/pathosd.yaml")
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "v1", cfg.Schema)
	assert.NotEmpty(t, cfg.BGP.Neighbors)
	assert.NotEmpty(t, cfg.VIPs)
}

func TestLoad_ExampleTOML(t *testing.T) {
	cfg, err := Load("../../examples/pathosd.toml")
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "v1", cfg.Schema)
	assert.NotEmpty(t, cfg.BGP.Neighbors)
	assert.NotEmpty(t, cfg.VIPs)
}

func TestLoad_NonexistentFile(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
}

func TestLoad_AppliesDefaults(t *testing.T) {
	// Parse without defaults to confirm they're absent, then Load which applies them.
	raw, err := Parse([]byte(minimalYAML), "yaml")
	require.NoError(t, err)
	assert.Nil(t, raw.BGP.GracefulRestart, "GracefulRestart should be nil before ApplyDefaults")

	// Load applies defaults.
	cfg, err := Load("../../examples/pathosd.yaml")
	require.NoError(t, err)
	assert.NotNil(t, cfg.BGP.GracefulRestart, "GracefulRestart should be set after Load")
}

func TestExpandEnvPlaceholders(t *testing.T) {
	t.Setenv("TEST_IP", "10.0.0.10")
	got, err := expandEnvPlaceholders([]byte("address: %{TEST_IP}\n"))
	require.NoError(t, err)
	assert.Equal(t, "address: 10.0.0.10\n", string(got))
}

func TestExpandEnvPlaceholders_MissingVariable(t *testing.T) {
	got, err := expandEnvPlaceholders([]byte("address: %{MISSING_IP}\n"))
	require.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "MISSING_IP")
}

func TestExpandEnvPlaceholders_EscapedPlaceholder(t *testing.T) {
	t.Setenv("TEST_IP", "10.0.0.10")
	got, err := expandEnvPlaceholders([]byte("literal: %%{TEST_IP}\nactual: %{TEST_IP}\n"))
	require.NoError(t, err)
	assert.Equal(t, "literal: %{TEST_IP}\nactual: 10.0.0.10\n", string(got))
}

func TestLoad_EnvPlaceholdersYAML(t *testing.T) {
	t.Setenv("PATHOSD_TEST_ROUTER_ID", "10.0.0.44")
	t.Setenv("PATHOSD_TEST_NEIGHBOR_IP", "10.0.0.254")

	content := `
schema: v1
router:
  asn: 65001
  router_id: "%{PATHOSD_TEST_ROUTER_ID}"
api:
  listen: ":8080"
bgp:
  neighbors:
    - name: spine-1
      address: "%{PATHOSD_TEST_NEIGHBOR_IP}"
      peer_asn: 65000
vips:
  - name: web-vip
    prefix: 10.10.1.1/32
    check:
      type: http
      http:
        url: /healthz
    policy:
      fail_action: withdraw
`

	cfgPath := filepath.Join(t.TempDir(), "pathosd.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0o644))

	cfg, err := Load(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.44", cfg.Router.RouterID)
	assert.Equal(t, "10.0.0.254", cfg.BGP.Neighbors[0].Address)
}

func TestLoad_EnvPlaceholdersMissingVariable(t *testing.T) {
	content := `
schema: v1
router:
  asn: 65001
  router_id: "%{PATHOSD_TEST_ROUTER_ID_MISSING}"
api:
  listen: ":8080"
bgp:
  neighbors:
    - name: spine-1
      address: 10.0.0.254
      peer_asn: 65000
vips:
  - name: web-vip
    prefix: 10.10.1.1/32
    check:
      type: http
      http:
        url: /healthz
    policy:
      fail_action: withdraw
`

	cfgPath := filepath.Join(t.TempDir(), "pathosd.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0o644))

	_, err := Load(cfgPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expanding environment placeholders")
	assert.Contains(t, err.Error(), "PATHOSD_TEST_ROUTER_ID_MISSING")
}
