package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompileChecksPolicy(t *testing.T) {
	names := []string{"web", "dns", "ping"}

	t.Run("default is all", func(t *testing.T) {
		ev, err := CompileChecksPolicy("", names)
		require.NoError(t, err)
		assert.Equal(t, ChecksPolicyAll, ev.Policy())
		assert.True(t, ev.Eval(map[string]bool{"web": true, "dns": true, "ping": true}))
		assert.False(t, ev.Eval(map[string]bool{"web": true, "dns": true}), "missing check counts as unhealthy")
	})

	t.Run("any", func(t *testing.T) {
		ev, err := CompileChecksPolicy(ChecksPolicyAny, names)
		require.NoError(t, err)
		assert.True(t, ev.Eval(map[string]bool{"ping": true}))
		assert.False(t, ev.Eval(map[string]bool{}))
	})

	t.Run("expression", func(t *testing.T) {
		ev, err := CompileChecksPolicy("web || (dns && ping)", names)
		require.NoError(t, err)
		assert.Equal(t, "web || (dns && ping)", ev.Policy())
		assert.True(t, ev.Eval(map[string]bool{"web": true}))
		assert.True(t, ev.Eval(map[string]bool{"dns": true, "ping": true}))
		assert.False(t, ev.Eval(map[string]bool{"dns": true}))
		assert.False(t, ev.Eval(nil))
	})

	t.Run("word operators", func(t *testing.T) {
		ev, err := CompileChecksPolicy("web and not dns", names)
		require.NoError(t, err)
		assert.True(t, ev.Eval(map[string]bool{"web": true}))
		assert.False(t, ev.Eval(map[string]bool{"web": true, "dns": true}))
	})

	t.Run("unknown check name is rejected", func(t *testing.T) {
		_, err := CompileChecksPolicy("wbe && dns", names)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown name wbe")
		assert.Contains(t, err.Error(), "known checks: dns, ping, web")
	})

	t.Run("non-boolean expression is rejected", func(t *testing.T) {
		_, err := CompileChecksPolicy("web ? 1 : 0", names)
		require.Error(t, err)
	})

	t.Run("syntax error is rejected", func(t *testing.T) {
		_, err := CompileChecksPolicy("web &&", names)
		require.Error(t, err)
	})
}

func TestValidateCheckName(t *testing.T) {
	for _, ok := range []string{"http", "web_2", "_x", "A"} {
		assert.NoError(t, ValidateCheckName(ok), ok)
	}
	for _, bad := range []string{"", "web-primary", "2nd", "a b", "all", "any", "ünï"} {
		assert.Error(t, ValidateCheckName(bad), bad)
	}
}
