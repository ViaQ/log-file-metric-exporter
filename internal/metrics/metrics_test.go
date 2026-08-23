package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	pod0Log = "/var/log/pods/test-ns_my-pod_19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e/my-container/0.log"
	pod1Log = "/var/log/pods/test-ns_my-pod_19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e/my-container/1.log"
	pod2Log = "/var/log/pods/test-ns_my-pod_19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e/my-container/2.log"
)

func newRegistered(t *testing.T) (*Metrics, *prometheus.Registry) {
	t.Helper()
	m := New()
	reg := prometheus.NewRegistry()
	require.NoError(t, m.Register(reg))
	return m, reg
}

// series returns every metric in the log_logged_bytes_total family.
func series(t *testing.T, reg *prometheus.Registry) []*dto.Metric {
	t.Helper()
	gathered, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range gathered {
		if mf.GetName() == "log_logged_bytes_total" {
			return mf.GetMetric()
		}
	}
	return nil
}

func TestMetricsUpdate(t *testing.T) {
	m, reg := newRegistered(t)
	require.NoError(t, m.Update(pod0Log, 1024))

	got := series(t, reg)
	require.Len(t, got, 1)

	assert.Equal(t, "test-ns", getLabelValue(got[0], "namespace"))
	assert.Equal(t, "my-pod", getLabelValue(got[0], "podname"))
	assert.Equal(t, "19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e", getLabelValue(got[0], "poduuid"))
	assert.Equal(t, "my-container", getLabelValue(got[0], "containername"))
	assert.Equal(t, float64(1024), got[0].GetCounter().GetValue())
}

// A growing file contributes only its growth, not its full size, on each update.
func TestMetricsUpdateAccumulatesGrowth(t *testing.T) {
	m, reg := newRegistered(t)

	require.NoError(t, m.Update(pod0Log, 1000))
	require.NoError(t, m.Update(pod0Log, 1500))
	require.NoError(t, m.Update(pod0Log, 1800))

	got := series(t, reg)
	require.Len(t, got, 1)
	assert.Equal(t, float64(1800), got[0].GetCounter().GetValue())
}

// Truncation restarts the file at a smaller size; the counter must not go
// backwards, and the new content counts in full.
func TestMetricsUpdateHandlesTruncation(t *testing.T) {
	m, reg := newRegistered(t)

	require.NoError(t, m.Update(pod0Log, 1000))
	require.NoError(t, m.Update(pod0Log, 200))

	got := series(t, reg)
	require.Len(t, got, 1)
	assert.Equal(t, float64(1200), got[0].GetCounter().GetValue())
}

// Rotation moves writes to a new file name, but all rotations of one container
// share a single series whose total keeps climbing.
func TestMetricsRotationSharesOneSeries(t *testing.T) {
	m, reg := newRegistered(t)

	require.NoError(t, m.Update(pod0Log, 1000))
	require.NoError(t, m.Update(pod1Log, 500))
	require.NoError(t, m.Update(pod2Log, 300))

	got := series(t, reg)
	require.Len(t, got, 1, "rotations must not fan out into separate series")
	assert.Equal(t, float64(1800), got[0].GetCounter().GetValue())
}

func TestMetricsForget(t *testing.T) {
	m, reg := newRegistered(t)

	otherContainer := "/var/log/pods/test-ns_my-pod_19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e/other-container/0.log"
	require.NoError(t, m.Update(pod0Log, 1024))
	require.NoError(t, m.Update(otherContainer, 2048))
	require.Len(t, series(t, reg), 2)

	m.Forget(pod0Log)
	require.Len(t, series(t, reg), 1)
}

// After forgetting a path the remembered size is cleared, so a later update
// counts the file's full size rather than a delta against stale state.
func TestMetricsForgetClearsRememberedSize(t *testing.T) {
	m, reg := newRegistered(t)

	require.NoError(t, m.Update(pod0Log, 1000))
	m.Forget(pod0Log)
	require.NoError(t, m.Update(pod0Log, 400))

	got := series(t, reg)
	require.Len(t, got, 1)
	assert.Equal(t, float64(400), got[0].GetCounter().GetValue())
}

func TestMetricsSeparateContainers(t *testing.T) {
	m, reg := newRegistered(t)

	paths := []string{
		"/var/log/pods/test-ns_my-pod_19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e/container-a/0.log",
		"/var/log/pods/test-ns_my-pod_19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e/container-b/0.log",
		"/var/log/pods/test-ns_my-pod_19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e/container-c/0.log",
	}
	for i, path := range paths {
		require.NoError(t, m.Update(path, int64(1000+i*100)))
	}

	assert.Len(t, series(t, reg), 3)
}

func TestMetricsUpdateInvalidPath(t *testing.T) {
	m, _ := newRegistered(t)

	err := m.Update("/var/log/pods/invalid/path/file.txt", 1024)
	assert.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "does not match log file pattern"))
}

// Helper function to get label value from a metric
func getLabelValue(metric *dto.Metric, labelName string) string {
	for _, label := range metric.GetLabel() {
		if label.GetName() == labelName {
			return label.GetValue()
		}
	}
	return ""
}
