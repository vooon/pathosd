package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/model"
)

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name                string
		healthy             bool
		lowerPriorityFileOn bool
		policy              config.PolicyConfig
		expected            model.VIPState
	}{
		{
			name:                "healthy ignores fail_action withdraw",
			healthy:             true,
			lowerPriorityFileOn: false,
			policy:              config.PolicyConfig{FailAction: "withdraw"},
			expected:            model.StateAnnounced,
		},
		{
			name:                "healthy ignores fail_action lower_priority",
			healthy:             true,
			lowerPriorityFileOn: false,
			policy:              config.PolicyConfig{FailAction: "lower_priority"},
			expected:            model.StateAnnounced,
		},
		{
			name:                "healthy ignores unknown fail_action",
			healthy:             true,
			lowerPriorityFileOn: false,
			policy:              config.PolicyConfig{FailAction: ""},
			expected:            model.StateAnnounced,
		},
		{
			name:                "healthy with lower_priority_file present is pessimized",
			healthy:             true,
			lowerPriorityFileOn: true,
			policy:              config.PolicyConfig{FailAction: "withdraw"},
			expected:            model.StatePessimized,
		},
		{
			name:                "unhealthy withdraw action",
			healthy:             false,
			lowerPriorityFileOn: false,
			policy:              config.PolicyConfig{FailAction: "withdraw"},
			expected:            model.StateWithdrawn,
		},
		{
			name:                "unhealthy lower_priority action",
			healthy:             false,
			lowerPriorityFileOn: false,
			policy:              config.PolicyConfig{FailAction: "lower_priority"},
			expected:            model.StatePessimized,
		},
		{
			name:                "unhealthy unknown action defaults to withdrawn",
			healthy:             false,
			lowerPriorityFileOn: true,
			policy:              config.PolicyConfig{FailAction: ""},
			expected:            model.StateWithdrawn,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.healthy, tc.lowerPriorityFileOn, &tc.policy)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestRouteAttrsFor(t *testing.T) {
	lower := &config.LowerPriorityConfig{ASPathPrepend: new(3), Communities: []string{"65535:666"}}

	tests := []struct {
		name   string
		policy config.PolicyConfig
		state  model.VIPState
		want   model.RouteAttrs
	}{
		{
			name:   "announced carries no extra attributes",
			policy: config.PolicyConfig{FailAction: "lower_priority", LowerPriority: lower},
			state:  model.StateAnnounced,
			want:   model.RouteAttrs{},
		},
		{
			name:   "withdrawn yields zero value",
			policy: config.PolicyConfig{FailAction: "lower_priority", LowerPriority: lower},
			state:  model.StateWithdrawn,
			want:   model.RouteAttrs{},
		},
		{
			name:   "pessimized uses lower_priority block",
			policy: config.PolicyConfig{FailAction: "lower_priority", LowerPriority: lower},
			state:  model.StatePessimized,
			want:   model.RouteAttrs{ASPathPrepend: 3, Communities: []string{"65535:666"}},
		},
		{
			name:   "pessimized without lower_priority block falls back to default prepend",
			policy: config.PolicyConfig{FailAction: "withdraw"},
			state:  model.StatePessimized,
			want:   model.RouteAttrs{ASPathPrepend: 6},
		},
		{
			name:   "pessimized with block but no prepend falls back to default prepend",
			policy: config.PolicyConfig{FailAction: "withdraw", LowerPriority: &config.LowerPriorityConfig{Communities: []string{"65535:1"}}},
			state:  model.StatePessimized,
			want:   model.RouteAttrs{ASPathPrepend: 6, Communities: []string{"65535:1"}},
		},
		{
			name: "announced carries the announce block",
			policy: config.PolicyConfig{
				FailAction:    "lower_priority",
				Announce:      &config.AnnounceConfig{ASPathPrepend: new(4), Communities: []string{"65535:100"}},
				LowerPriority: lower,
			},
			state: model.StateAnnounced,
			want:  model.RouteAttrs{ASPathPrepend: 4, Communities: []string{"65535:100"}},
		},
		{
			name: "pessimized adds base and lower_priority prepends and merges communities",
			policy: config.PolicyConfig{
				FailAction:    "lower_priority",
				Announce:      &config.AnnounceConfig{ASPathPrepend: new(4), Communities: []string{"65535:100", "65535:666"}},
				LowerPriority: lower,
			},
			state: model.StatePessimized,
			want:  model.RouteAttrs{ASPathPrepend: 7, Communities: []string{"65535:100", "65535:666"}},
		},
		{
			name: "announce block with zero prepend behaves like no block",
			policy: config.PolicyConfig{
				FailAction:    "lower_priority",
				Announce:      &config.AnnounceConfig{ASPathPrepend: new(0)},
				LowerPriority: lower,
			},
			state: model.StateAnnounced,
			want:  model.RouteAttrs{},
		},
		{
			name: "withdrawn ignores announce block",
			policy: config.PolicyConfig{
				FailAction: "withdraw",
				Announce:   &config.AnnounceConfig{ASPathPrepend: new(4), Communities: []string{"65535:100"}},
			},
			state: model.StateWithdrawn,
			want:  model.RouteAttrs{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RouteAttrsFor(&tc.policy, tc.state)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestMergeCommunities(t *testing.T) {
	assert.Nil(t, mergeCommunities(nil, nil))
	assert.Equal(t, []string{"1:1"}, mergeCommunities([]string{"1:1"}, nil))
	assert.Equal(t, []string{"1:1"}, mergeCommunities(nil, []string{"1:1"}))
	assert.Equal(t, []string{"1:1", "2:2", "3:3"}, mergeCommunities([]string{"1:1", "2:2"}, []string{"2:2", "3:3"}))
}
