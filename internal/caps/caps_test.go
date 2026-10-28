package caps

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRaiseEffective_NoFileCaps(t *testing.T) {
	// Test binaries carry no file capabilities, so effective already equals
	// permitted (empty for a user, full for root) and nothing changes.
	raised, err := RaiseEffective()
	require.NoError(t, err)
	require.False(t, raised)
}
