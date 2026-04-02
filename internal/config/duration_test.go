package config

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDuration_MarshalJSON(t *testing.T) {
	d := Duration{Duration: 5 * time.Second}
	b, err := json.Marshal(d)
	require.NoError(t, err)
	assert.Equal(t, `"5s"`, string(b))
}

func TestDuration_UnmarshalJSON(t *testing.T) {
	var d Duration
	err := json.Unmarshal([]byte(`"1s"`), &d)
	require.NoError(t, err)
	assert.Equal(t, 1*time.Second, d.Duration)
}

func TestDuration_JSONRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		input time.Duration
		json  string
	}{
		{"1s", 1 * time.Second, `"1s"`},
		{"500ms", 500 * time.Millisecond, `"500ms"`},
		{"2m30s", 2*time.Minute + 30*time.Second, `"2m30s"`},
		{"100ms", 100 * time.Millisecond, `"100ms"`},
		{"90s", 90 * time.Second, `"1m30s"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := Duration{Duration: tc.input}

			b, err := json.Marshal(orig)
			require.NoError(t, err)
			assert.Equal(t, tc.json, string(b))

			var got Duration
			err = json.Unmarshal(b, &got)
			require.NoError(t, err)
			assert.Equal(t, tc.input, got.Duration)
		})
	}
}

func TestDuration_UnmarshalJSON_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"not a string (number)", `123`},
		{"invalid duration string", `"notaduration"`},
		{"empty string", `""`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var d Duration
			err := json.Unmarshal([]byte(tc.input), &d)
			assert.Error(t, err)
		})
	}
}

func TestDuration_MarshalText(t *testing.T) {
	d := Duration{Duration: 2 * time.Minute}
	b, err := d.MarshalText()
	require.NoError(t, err)
	assert.Equal(t, "2m0s", string(b))
}

func TestDuration_UnmarshalText(t *testing.T) {
	var d Duration
	err := d.UnmarshalText([]byte("30s"))
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, d.Duration)
}

func TestDuration_TextRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		input time.Duration
	}{
		{"1s", 1 * time.Second},
		{"500ms", 500 * time.Millisecond},
		{"10m0s", 10 * time.Minute},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := Duration{Duration: tc.input}

			b, err := orig.MarshalText()
			require.NoError(t, err)

			var got Duration
			err = got.UnmarshalText(b)
			require.NoError(t, err)
			assert.Equal(t, tc.input, got.Duration)
		})
	}
}

func TestDuration_UnmarshalText_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"invalid string", "notaduration"},
		{"empty string", ""},
		{"number only", "42"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var d Duration
			err := d.UnmarshalText([]byte(tc.input))
			assert.Error(t, err)
		})
	}
}

// yamlDurationWrapper is used to test YAML marshal/unmarshal of Duration via goccy/go-yaml.
type yamlDurationWrapper struct {
	Interval *Duration `yaml:"interval"`
	Timeout  *Duration `yaml:"timeout"`
}

func TestDuration_YAMLRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
		timeout  time.Duration
	}{
		{"1s and 500ms", 1 * time.Second, 500 * time.Millisecond},
		{"5s and 2s", 5 * time.Second, 2 * time.Second},
		{"90s and 30s", 90 * time.Second, 30 * time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := yamlDurationWrapper{
				Interval: &Duration{Duration: tc.interval},
				Timeout:  &Duration{Duration: tc.timeout},
			}

			b, err := yaml.Marshal(&orig)
			require.NoError(t, err)

			var got yamlDurationWrapper
			err = yaml.Unmarshal(b, &got)
			require.NoError(t, err)

			require.NotNil(t, got.Interval)
			assert.Equal(t, tc.interval, got.Interval.Duration)
			require.NotNil(t, got.Timeout)
			assert.Equal(t, tc.timeout, got.Timeout.Duration)
		})
	}
}

func TestDuration_YAMLUnmarshal_FromString(t *testing.T) {
	data := []byte(`interval: 5s
timeout: 500ms
`)
	var w yamlDurationWrapper
	err := yaml.Unmarshal(data, &w)
	require.NoError(t, err)
	require.NotNil(t, w.Interval)
	assert.Equal(t, 5*time.Second, w.Interval.Duration)
	require.NotNil(t, w.Timeout)
	assert.Equal(t, 500*time.Millisecond, w.Timeout.Duration)
}
