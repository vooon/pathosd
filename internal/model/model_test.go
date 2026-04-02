package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type enumStringCase[T comparable] struct {
	value    T
	expected string
}

func TestVIPState_String(t *testing.T) {
	cases := []enumStringCase[VIPState]{
		{value: StateWithdrawn, expected: "withdrawn"},
		{value: StateAnnounced, expected: "announced"},
		{value: StatePessimized, expected: "pessimized"},
		{value: VIPState(99), expected: "unknown"},
		{value: VIPState(-1), expected: "unknown"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.expected, tc.value.String())
	}
}

func TestHealthStatus_String(t *testing.T) {
	cases := []enumStringCase[HealthStatus]{
		{value: HealthUnknown, expected: "unknown"},
		{value: HealthHealthy, expected: "healthy"},
		{value: HealthUnhealthy, expected: "unhealthy"},
		{value: HealthStatus(99), expected: "unknown"},
		{value: HealthStatus(-1), expected: "unknown"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.expected, tc.value.String())
	}
}

func TestEnumValues(t *testing.T) {
	assert.Equal(t, VIPState(0), StateWithdrawn)
	assert.Equal(t, VIPState(1), StateAnnounced)
	assert.Equal(t, VIPState(2), StatePessimized)

	assert.Equal(t, HealthStatus(0), HealthUnknown)
	assert.Equal(t, HealthStatus(1), HealthHealthy)
	assert.Equal(t, HealthStatus(2), HealthUnhealthy)
}
