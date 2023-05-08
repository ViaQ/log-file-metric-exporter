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

	log "github.com/ViaQ/logerr/v2/log/static"
)

const (
	logname = "openshift-monitoring_prometheus-k8s-0_9a5888d1-e009-4cc3-bc19-c5543b4b84f7/kube-rbac-proxy-thanos/2.log"
	data    = "hello\n"
)

func setup(t *testing.T, initLog func(string)) (watcher *Watcher, path string, labels LogLabels) {
	t.Helper()
	dir, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	t.Cleanup(func() {
		log.V(4).Info("Running test cleanup...removing dir", "dir", dir)
		_ = os.RemoveAll(dir)
	})
	require.NoError(t, err)
	path = filepath.Join(dir, logname)
	require.True(t, labels.Parse(path))
	os.MkdirAll(filepath.Dir(path), 0700)
	if initLog != nil {
		initLog(path)
	}
	watcher, err = New(dir)
	require.NoError(t, err)
	go watcher.Watch()
	t.Cleanup(func() { watcher.Close() })
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
	w, path, l := setup(t, nil)

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
func TestWatcherSeesAndWatchesExistingFiles(t *testing.T) {
	w, path, l := setup(t, func(path string) {
		writeToFile(t, path)
		require.NoError(t, ioutil.WriteFile(path, []byte(data), 0600))
	})

	counter, err := w.metrics.GetMetricWithLabelValues(l.Namespace, l.Name, l.UUID, l.Container)
	require.NoError(t, err)
	// assert we see the initial file size
	assert.Eventually(t,
		func() bool {
			v := getCounterValue(counter)
			log.V(3).Info("initial size", "counter", v)
			return float64(len(data)) == v
		},
		time.Second, time.Second/10, "%v != %v", len(data), getCounterValue(counter))

	writeToFile(t, path)
	writeToFile(t, path)
	// assert we see the change in the file size
	assert.Eventually(t,
		func() bool {
			v := getCounterValue(counter)
			log.V(3).Info("size after write", "counter", v)
			return float64(3*len(data)) == v
		},
		time.Second, time.Second/10, "%v != %v", 3*len(data), getCounterValue(counter))
}

func writeToFile(t *testing.T, path string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	require.NoError(t, err)
	_, err = f.Write([]byte(data))
	require.NoError(t, err)
}
