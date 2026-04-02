package config

import (
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

func TestParse_UnsupportedFormat(t *testing.T) {
	_, err := Parse([]byte("{}"), "json")
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
		{"config.json", ".json"},
		{"config", ""},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			assert.Equal(t, tc.want, detectFormat(tc.path))
		})
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
