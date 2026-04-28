package policy

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vooon/pathosd/internal/checks"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/metrics"
	"github.com/vooon/pathosd/internal/model"
)

const (
	withdrawVIPName  = "vip-withdraw"
	withdrawPrefix   = "10.0.0.1/32"
	pessimizeVIPName = "vip-lower"
	pessimizePrefix  = "10.0.0.2/32"
)

type fakeBGPNotifier struct {
	mu         sync.Mutex
	announces  []string
	withdraws  []string
	pessimizes []struct {
		prefix      string
		prepend     int
		communities []string
	}
}

func (f *fakeBGPNotifier) AnnounceVIP(_ context.Context, prefix string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.announces = append(f.announces, prefix)
	return nil
}

func (f *fakeBGPNotifier) WithdrawVIP(_ context.Context, prefix string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.withdraws = append(f.withdraws, prefix)
	return nil
}

func (f *fakeBGPNotifier) PessimizeVIP(_ context.Context, prefix string, prepend int, communities []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	communityCopy := make([]string, len(communities))
	copy(communityCopy, communities)

	f.pessimizes = append(f.pessimizes, struct {
		prefix      string
		prepend     int
		communities []string
	}{
		prefix:      prefix,
		prepend:     prepend,
		communities: communityCopy,
	})
	return nil
}

func newTestMetrics() *metrics.Metrics {
	return metrics.New([]float64{0.1, 0.5, 1.0})
}

func testVIPConfigs() []config.VIPConfig {
	return []config.VIPConfig{
		{
			Name:   withdrawVIPName,
			Prefix: withdrawPrefix,
			Check: config.CheckConfig{
				Type: config.CheckTypeHTTP,
			},
			Policy: config.PolicyConfig{
				FailAction: "withdraw",
			},
		},
		{
			Name:   pessimizeVIPName,
			Prefix: pessimizePrefix,
			Check: config.CheckConfig{
				Type: config.CheckTypeDNS,
			},
			Policy: config.PolicyConfig{
				FailAction: "lower_priority",
				LowerPriority: &config.LowerPriorityConfig{
					ASPathPrepend: new(2),
					Communities:   []string{"65535:100"},
				},
			},
		},
	}
}

func testCustomPessimizeConfig(prepend int, communities []string) []config.VIPConfig {
	return []config.VIPConfig{
		{
			Name:   pessimizeVIPName,
			Prefix: pessimizePrefix,
			Check: config.CheckConfig{
				Type: config.CheckTypeHTTP,
			},
			Policy: config.PolicyConfig{
				FailAction: "lower_priority",
				LowerPriority: &config.LowerPriorityConfig{
					ASPathPrepend: new(prepend),
					Communities:   communities,
				},
			},
		},
	}
}

func testVIPConfigWithLowerPriorityFile(path string, prepend int, communities []string) []config.VIPConfig {
	return []config.VIPConfig{
		{
			Name:   pessimizeVIPName,
			Prefix: pessimizePrefix,
			Check: config.CheckConfig{
				Type: config.CheckTypeHTTP,
			},
			Policy: config.PolicyConfig{
				FailAction:        "withdraw",
				LowerPriorityFile: path,
				LowerPriority: &config.LowerPriorityConfig{
					ASPathPrepend: new(prepend),
					Communities:   communities,
				},
			},
		},
	}
}

func statusByName(t *testing.T, statuses []model.VIPStatus) map[string]model.VIPStatus {
	t.Helper()
	out := make(map[string]model.VIPStatus, len(statuses))
	for _, status := range statuses {
		out[status.Name] = status
	}
	return out
}

func getVIPStatus(t *testing.T, mgr *Manager, vipName string) model.VIPStatus {
	t.Helper()
	statuses := statusByName(t, mgr.GetVIPStatuses())
	status, ok := statuses[vipName]
	require.True(t, ok, "VIP %q not found in status output", vipName)
	return status
}

func TestNewManager(t *testing.T) {
	m := newTestMetrics()
	cfgs := testVIPConfigs()
	mgr := NewManager(cfgs, m, nil)

	statuses := mgr.GetVIPStatuses()
	require.Len(t, statuses, len(cfgs))
	statusMap := statusByName(t, statuses)

	tests := []struct {
		name   string
		vip    string
		prefix string
	}{
		{name: "withdraw vip", vip: withdrawVIPName, prefix: withdrawPrefix},
		{name: "lower priority vip", vip: pessimizeVIPName, prefix: pessimizePrefix},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, ok := statusMap[tc.vip]
			require.True(t, ok)

			assert.Equal(t, model.StateWithdrawn, status.State)
			assert.Equal(t, model.StateWithdrawn.String(), status.StateName)
			assert.Equal(t, model.HealthUnknown, status.Health)
			assert.Equal(t, model.HealthUnknown.String(), status.HealthName)
			assert.Equal(t, "initial", status.LastTransitionReason)

			assert.Equal(t, float64(0), testutil.ToFloat64(m.VIPState.WithLabelValues(tc.vip, tc.prefix)))
			assert.Equal(t, float64(1), testutil.ToFloat64(m.VIPPriority.WithLabelValues(tc.vip, tc.prefix)))
		})
	}
}

func TestManager_OnHealthTransition(t *testing.T) {
	t.Run("healthy transition withdrawn to announced", func(t *testing.T) {
		m := newTestMetrics()
		notifier := &fakeBGPNotifier{}
		mgr := NewManager(testVIPConfigs(), m, notifier)

		mgr.OnHealthTransition(checks.HealthTransition{
			VIPName: withdrawVIPName,
			Healthy: true,
			Reason:  "rise threshold reached",
		})

		status := getVIPStatus(t, mgr, withdrawVIPName)
		assert.Equal(t, model.StateAnnounced, status.State)
		assert.Equal(t, model.HealthHealthy, status.Health)
		assert.Equal(t, []string{withdrawPrefix}, notifier.announces)
		assert.Equal(t, float64(1), testutil.ToFloat64(m.VIPTransitions.WithLabelValues(withdrawVIPName, withdrawPrefix, model.StateAnnounced.String())))
		assert.Equal(t, float64(model.StateAnnounced), testutil.ToFloat64(m.VIPState.WithLabelValues(withdrawVIPName, withdrawPrefix)))
	})

	t.Run("unhealthy transition announced to withdrawn for withdraw policy", func(t *testing.T) {
		m := newTestMetrics()
		notifier := &fakeBGPNotifier{}
		mgr := NewManager(testVIPConfigs(), m, notifier)

		mgr.OnHealthTransition(checks.HealthTransition{
			VIPName: withdrawVIPName,
			Healthy: true,
			Reason:  "rise threshold reached",
		})
		mgr.OnHealthTransition(checks.HealthTransition{
			VIPName: withdrawVIPName,
			Healthy: false,
			Reason:  "fall threshold reached",
		})

		status := getVIPStatus(t, mgr, withdrawVIPName)
		assert.Equal(t, model.StateWithdrawn, status.State)
		assert.Equal(t, model.HealthUnhealthy, status.Health)
		assert.Equal(t, []string{withdrawPrefix}, notifier.withdraws)
		assert.Equal(t, float64(1), testutil.ToFloat64(m.VIPTransitions.WithLabelValues(withdrawVIPName, withdrawPrefix, model.StateWithdrawn.String())))
	})

	t.Run("unhealthy transition announced to pessimized for lower_priority policy", func(t *testing.T) {
		m := newTestMetrics()
		notifier := &fakeBGPNotifier{}
		mgr := NewManager(testVIPConfigs(), m, notifier)

		mgr.OnHealthTransition(checks.HealthTransition{
			VIPName: pessimizeVIPName,
			Healthy: true,
			Reason:  "rise threshold reached",
		})
		mgr.OnHealthTransition(checks.HealthTransition{
			VIPName: pessimizeVIPName,
			Healthy: false,
			Reason:  "fall threshold reached",
		})

		status := getVIPStatus(t, mgr, pessimizeVIPName)
		assert.Equal(t, model.StatePessimized, status.State)
		assert.Equal(t, model.HealthUnhealthy, status.Health)
		require.Len(t, notifier.pessimizes, 1)
		assert.Equal(t, pessimizePrefix, notifier.pessimizes[0].prefix)
		assert.Equal(t, 2, notifier.pessimizes[0].prepend)
		assert.Equal(t, []string{"65535:100"}, notifier.pessimizes[0].communities)
		assert.Equal(t, float64(2), testutil.ToFloat64(m.VIPPriority.WithLabelValues(pessimizeVIPName, pessimizePrefix)))
	})

	t.Run("healthy transition with lower_priority_file present forces pessimized state", func(t *testing.T) {
		m := newTestMetrics()
		notifier := &fakeBGPNotifier{}

		lockFile := filepath.Join(t.TempDir(), "drain.lock")
		require.NoError(t, os.WriteFile(lockFile, []byte("1"), 0o600))

		mgr := NewManager(testVIPConfigWithLowerPriorityFile(lockFile, 3, []string{"65535:666"}), m, notifier)
		mgr.OnHealthTransition(checks.HealthTransition{
			VIPName: pessimizeVIPName,
			Healthy: true,
			Reason:  "rise threshold reached",
		})

		status := getVIPStatus(t, mgr, pessimizeVIPName)
		assert.Equal(t, model.StatePessimized, status.State)
		assert.Equal(t, model.HealthHealthy, status.Health)
		assert.Empty(t, notifier.announces)
		require.Len(t, notifier.pessimizes, 1)
		assert.Equal(t, pessimizePrefix, notifier.pessimizes[0].prefix)
		assert.Equal(t, 3, notifier.pessimizes[0].prepend)
		assert.Equal(t, []string{"65535:666"}, notifier.pessimizes[0].communities)
		assert.Equal(t, float64(3), testutil.ToFloat64(m.VIPPriority.WithLabelValues(pessimizeVIPName, pessimizePrefix)))
	})

	t.Run("no-op same state does not notify and does not increment transition metric", func(t *testing.T) {
		m := newTestMetrics()
		notifier := &fakeBGPNotifier{}
		mgr := NewManager(testVIPConfigs(), m, notifier)

		mgr.OnHealthTransition(checks.HealthTransition{
			VIPName: withdrawVIPName,
			Healthy: true,
			Reason:  "rise threshold reached",
		})

		beforeTransitions := testutil.ToFloat64(m.VIPTransitions.WithLabelValues(withdrawVIPName, withdrawPrefix, model.StateAnnounced.String()))
		beforeAnnounces := len(notifier.announces)

		mgr.OnHealthTransition(checks.HealthTransition{
			VIPName: withdrawVIPName,
			Healthy: true,
			Reason:  "still healthy",
		})

		afterTransitions := testutil.ToFloat64(m.VIPTransitions.WithLabelValues(withdrawVIPName, withdrawPrefix, model.StateAnnounced.String()))
		assert.Equal(t, beforeTransitions, afterTransitions)
		assert.Len(t, notifier.announces, beforeAnnounces)
	})

	t.Run("unknown vip does not panic", func(t *testing.T) {
		m := newTestMetrics()
		mgr := NewManager(testVIPConfigs(), m, &fakeBGPNotifier{})

		require.NotPanics(t, func() {
			mgr.OnHealthTransition(checks.HealthTransition{
				VIPName: "missing-vip",
				Healthy: true,
				Reason:  "unknown",
			})
		})
	})

	t.Run("sequence withdrawn healthy announced unhealthy pessimized healthy announced", func(t *testing.T) {
		m := newTestMetrics()
		notifier := &fakeBGPNotifier{}
		mgr := NewManager(testVIPConfigs(), m, notifier)

		mgr.OnHealthTransition(checks.HealthTransition{
			VIPName: pessimizeVIPName,
			Healthy: true,
			Reason:  "rise threshold reached",
		})
		assert.Equal(t, model.StateAnnounced, getVIPStatus(t, mgr, pessimizeVIPName).State)

		mgr.OnHealthTransition(checks.HealthTransition{
			VIPName: pessimizeVIPName,
			Healthy: false,
			Reason:  "fall threshold reached",
		})
		assert.Equal(t, model.StatePessimized, getVIPStatus(t, mgr, pessimizeVIPName).State)

		mgr.OnHealthTransition(checks.HealthTransition{
			VIPName: pessimizeVIPName,
			Healthy: true,
			Reason:  "rise threshold reached",
		})
		assert.Equal(t, model.StateAnnounced, getVIPStatus(t, mgr, pessimizeVIPName).State)

		assert.Equal(t, []string{pessimizePrefix, pessimizePrefix}, notifier.announces)
		assert.Empty(t, notifier.withdraws)
		require.Len(t, notifier.pessimizes, 1)
		assert.Equal(t, pessimizePrefix, notifier.pessimizes[0].prefix)
	})

	t.Run("pessimize uses custom prepend and communities", func(t *testing.T) {
		m := newTestMetrics()
		notifier := &fakeBGPNotifier{}
		mgr := NewManager(testCustomPessimizeConfig(3, []string{"65535:666"}), m, notifier)

		mgr.OnHealthTransition(checks.HealthTransition{
			VIPName: pessimizeVIPName,
			Healthy: true,
			Reason:  "rise threshold reached",
		})
		mgr.OnHealthTransition(checks.HealthTransition{
			VIPName: pessimizeVIPName,
			Healthy: false,
			Reason:  "fall threshold reached",
		})

		require.Len(t, notifier.pessimizes, 1)
		assert.Equal(t, pessimizePrefix, notifier.pessimizes[0].prefix)
		assert.Equal(t, 3, notifier.pessimizes[0].prepend)
		assert.Equal(t, []string{"65535:666"}, notifier.pessimizes[0].communities)
		assert.Equal(t, float64(3), testutil.ToFloat64(m.VIPPriority.WithLabelValues(pessimizeVIPName, pessimizePrefix)))
	})
}

func TestManager_OnCheckResult(t *testing.T) {
	tests := []struct {
		name                string
		result              checks.Result
		expectedResultLabel string
		expectedLastResult  float64
		expectedTimeout     float64
	}{
		{
			name: "success result updates metrics",
			result: checks.Result{
				Success:  true,
				Detail:   "ok",
				Duration: 120 * time.Millisecond,
			},
			expectedResultLabel: "success",
			expectedLastResult:  1,
			expectedTimeout:     0,
		},
		{
			name: "failure result updates metrics",
			result: checks.Result{
				Success:  false,
				Detail:   "failed",
				Duration: 220 * time.Millisecond,
			},
			expectedResultLabel: "fail",
			expectedLastResult:  0,
			expectedTimeout:     0,
		},
		{
			name: "timed out result increments timeout metric",
			result: checks.Result{
				Success:  false,
				Detail:   "timeout",
				Duration: 500 * time.Millisecond,
				TimedOut: true,
			},
			expectedResultLabel: "fail",
			expectedLastResult:  0,
			expectedTimeout:     1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestMetrics()
			mgr := NewManager(testVIPConfigs(), m, nil)

			mgr.OnCheckResult(withdrawVIPName, tc.result)

			assert.Equal(t, tc.expectedLastResult, testutil.ToFloat64(m.CheckLastResult.WithLabelValues(withdrawVIPName, withdrawPrefix)))
			assert.Equal(t, float64(1), testutil.ToFloat64(m.CheckTotal.WithLabelValues(withdrawVIPName, withdrawPrefix, config.CheckTypeHTTP, tc.expectedResultLabel)))
			assert.Equal(t, tc.expectedTimeout, testutil.ToFloat64(m.CheckTimeoutExceeded.WithLabelValues(withdrawVIPName, withdrawPrefix)))

			status := getVIPStatus(t, mgr, withdrawVIPName)
			assert.Equal(t, tc.result.Success, status.LastCheckSuccess)
			assert.Equal(t, tc.result.Detail, status.LastCheckDetail)
			assert.False(t, status.LastCheckTime.IsZero())
		})
	}

	t.Run("lower_priority_file create and remove are treated as state transitions", func(t *testing.T) {
		m := newTestMetrics()
		notifier := &fakeBGPNotifier{}

		lockFile := filepath.Join(t.TempDir(), "drain.lock")
		mgr := NewManager(testVIPConfigWithLowerPriorityFile(lockFile, 3, []string{"65535:666"}), m, notifier)

		// Healthy without lock file -> announced.
		mgr.OnHealthTransition(checks.HealthTransition{
			VIPName: pessimizeVIPName,
			Healthy: true,
			Reason:  "rise threshold reached",
		})
		status := getVIPStatus(t, mgr, pessimizeVIPName)
		assert.Equal(t, model.StateAnnounced, status.State)
		assert.Len(t, notifier.announces, 1)

		// Create lock file and report a check result to force policy re-evaluation.
		require.NoError(t, os.WriteFile(lockFile, []byte("1"), 0o600))
		mgr.OnCheckResult(pessimizeVIPName, checks.Result{
			Success:  true,
			Detail:   "healthy while draining",
			Duration: 15 * time.Millisecond,
		})

		status = getVIPStatus(t, mgr, pessimizeVIPName)
		assert.Equal(t, model.StatePessimized, status.State)
		assert.Equal(t, "lower_priority_file created", status.LastTransitionReason)
		require.Len(t, notifier.pessimizes, 1)
		assert.Equal(t, 3, notifier.pessimizes[0].prepend)
		assert.Equal(t, []string{"65535:666"}, notifier.pessimizes[0].communities)
		assert.Equal(t, float64(1), testutil.ToFloat64(m.VIPTransitions.WithLabelValues(pessimizeVIPName, pessimizePrefix, model.StatePessimized.String())))

		// Remove lock file and report another check result -> back to announced.
		require.NoError(t, os.Remove(lockFile))
		mgr.OnCheckResult(pessimizeVIPName, checks.Result{
			Success:  true,
			Detail:   "healthy and drain removed",
			Duration: 10 * time.Millisecond,
		})

		status = getVIPStatus(t, mgr, pessimizeVIPName)
		assert.Equal(t, model.StateAnnounced, status.State)
		assert.Equal(t, "lower_priority_file removed", status.LastTransitionReason)
		assert.Len(t, notifier.announces, 2)
	})

	t.Run("unknown vip does not panic", func(t *testing.T) {
		m := newTestMetrics()
		mgr := NewManager(testVIPConfigs(), m, nil)

		require.NotPanics(t, func() {
			mgr.OnCheckResult("missing-vip", checks.Result{
				Success: true,
				Detail:  "ignored",
			})
		})
	})

	t.Run("last check result and time are updated", func(t *testing.T) {
		m := newTestMetrics()
		mgr := NewManager(testVIPConfigs(), m, nil)

		first := checks.Result{
			Success:  false,
			Detail:   "first failure",
			Duration: 10 * time.Millisecond,
		}
		second := checks.Result{
			Success:  true,
			Detail:   "second success",
			Duration: 20 * time.Millisecond,
		}

		mgr.OnCheckResult(withdrawVIPName, first)
		firstStatus := getVIPStatus(t, mgr, withdrawVIPName)

		mgr.OnCheckResult(withdrawVIPName, second)
		secondStatus := getVIPStatus(t, mgr, withdrawVIPName)

		assert.Equal(t, first.Success, firstStatus.LastCheckSuccess)
		assert.Equal(t, first.Detail, firstStatus.LastCheckDetail)
		assert.Equal(t, second.Success, secondStatus.LastCheckSuccess)
		assert.Equal(t, second.Detail, secondStatus.LastCheckDetail)
		assert.False(t, secondStatus.LastCheckTime.Before(firstStatus.LastCheckTime))
	})
}

func TestManager_GetVIPStatuses(t *testing.T) {
	m := newTestMetrics()
	notifier := &fakeBGPNotifier{}
	mgr := NewManager(testVIPConfigs(), m, notifier)

	mgr.OnHealthTransition(checks.HealthTransition{
		VIPName: withdrawVIPName,
		Healthy: true,
		Reason:  "rise threshold reached",
	})
	mgr.OnHealthTransition(checks.HealthTransition{
		VIPName: pessimizeVIPName,
		Healthy: true,
		Reason:  "rise threshold reached",
	})
	mgr.OnHealthTransition(checks.HealthTransition{
		VIPName: pessimizeVIPName,
		Healthy: false,
		Reason:  "fall threshold reached",
	})

	mgr.OnCheckResult(withdrawVIPName, checks.Result{
		Success:  true,
		Detail:   "withdraw vip healthy",
		Duration: 50 * time.Millisecond,
	})
	mgr.OnCheckResult(pessimizeVIPName, checks.Result{
		Success:  false,
		Detail:   "pessimize vip unhealthy",
		Duration: 75 * time.Millisecond,
		TimedOut: true,
	})

	statuses := mgr.GetVIPStatuses()
	require.Len(t, statuses, 2)
	statusMap := statusByName(t, statuses)

	t.Run("withdraw vip status fields", func(t *testing.T) {
		status, ok := statusMap[withdrawVIPName]
		require.True(t, ok)
		assert.Equal(t, withdrawPrefix, status.Prefix)
		assert.Equal(t, model.StateAnnounced, status.State)
		assert.Equal(t, model.StateAnnounced.String(), status.StateName)
		assert.Equal(t, model.HealthHealthy, status.Health)
		assert.Equal(t, model.HealthHealthy.String(), status.HealthName)
		assert.True(t, status.LastCheckSuccess)
		assert.Equal(t, "withdraw vip healthy", status.LastCheckDetail)
		assert.False(t, status.LastCheckTime.IsZero())
		assert.False(t, status.LastTransitionTime.IsZero())
		assert.Equal(t, "rise threshold reached", status.LastTransitionReason)
		assert.Equal(t, config.CheckTypeHTTP, status.CheckType)
	})

	t.Run("pessimize vip status fields", func(t *testing.T) {
		status, ok := statusMap[pessimizeVIPName]
		require.True(t, ok)
		assert.Equal(t, pessimizePrefix, status.Prefix)
		assert.Equal(t, model.StatePessimized, status.State)
		assert.Equal(t, model.StatePessimized.String(), status.StateName)
		assert.Equal(t, model.HealthUnhealthy, status.Health)
		assert.Equal(t, model.HealthUnhealthy.String(), status.HealthName)
		assert.False(t, status.LastCheckSuccess)
		assert.Equal(t, "pessimize vip unhealthy", status.LastCheckDetail)
		assert.False(t, status.LastCheckTime.IsZero())
		assert.False(t, status.LastTransitionTime.IsZero())
		assert.Equal(t, "fall threshold reached", status.LastTransitionReason)
		assert.Equal(t, config.CheckTypeDNS, status.CheckType)
	})
}
