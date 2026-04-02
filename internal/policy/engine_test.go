package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/model"
)

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name     string
		healthy  bool
		policy   config.PolicyConfig
		expected model.VIPState
	}{
		{
			name:     "healthy ignores fail_action withdraw",
			healthy:  true,
			policy:   config.PolicyConfig{FailAction: "withdraw"},
			expected: model.StateAnnounced,
		},
		{
			name:     "healthy ignores fail_action lower_priority",
			healthy:  true,
			policy:   config.PolicyConfig{FailAction: "lower_priority"},
			expected: model.StateAnnounced,
		},
		{
			name:     "healthy ignores unknown fail_action",
			healthy:  true,
			policy:   config.PolicyConfig{FailAction: ""},
			expected: model.StateAnnounced,
		},
		{
			name:     "unhealthy withdraw action",
			healthy:  false,
			policy:   config.PolicyConfig{FailAction: "withdraw"},
			expected: model.StateWithdrawn,
		},
		{
			name:     "unhealthy lower_priority action",
			healthy:  false,
			policy:   config.PolicyConfig{FailAction: "lower_priority"},
			expected: model.StatePessimized,
		},
		{
			name:     "unhealthy unknown action defaults to withdrawn",
			healthy:  false,
			policy:   config.PolicyConfig{FailAction: ""},
			expected: model.StateWithdrawn,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.healthy, &tc.policy)
			assert.Equal(t, tc.expected, got)
		})
	}
}
