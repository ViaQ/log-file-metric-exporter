package logwatch

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	logname = "openshift-monitoring_prometheus-k8s-0_9a5888d1-e009-4cc3-bc19-c5543b4b84f7/kube-rbac-proxy-thanos/2.log"
	data    = "hello\n"
)

func setup(t *testing.T) (watcher *Watcher, path string, labels LogLabels) {
	t.Helper()
	dir, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	watcher, err = New(dir)
	require.NoError(t, err)
	path = filepath.Join(dir, logname)
	require.True(t, labels.Parse(path))
	os.MkdirAll(filepath.Dir(path), 0700)
	go watcher.Watch()
	return watcher, path, labels
}

func getCounterValue(c prometheus.Counter) float64 {
	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		return 0
	}
	return m.Counter.GetValue()
}

func TestWatcherSeesFileChange(t *testing.T) {
	w, path, l := setup(t)

	counter, err := w.metrics.GetMetricWithLabelValues(l.Namespace, l.Name, l.UUID, l.Container)
	require.NoError(t, err)

	assert.Eventually(t,
		func() bool {
			require.NoError(t, ioutil.WriteFile(path, []byte(data), 0600))
			return float64(len(data)) == getCounterValue(counter)
		},
		time.Second, time.Second/10, "%v != %v", len(data), getCounterValue(counter))

	assert.NoError(t, os.Remove(path))
	assert.Eventually(t,
		func() bool {
			counter, err := w.metrics.GetMetricWithLabelValues(l.Namespace, l.Name, l.UUID, l.Container)
			require.NoError(t, err)
			return getCounterValue(counter) == 0
		},
		time.Second, time.Second/10, "%v != 0", len(data), getCounterValue(counter))
}
