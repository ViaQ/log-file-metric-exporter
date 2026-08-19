package logwatch

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
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
	dir, path := setupDir(t)
	require.True(t, labels.Parse(path))
	if initLog != nil {
		initLog(path)
	}
	var err error
	watcher, err = New(dir, 0)
	require.NoError(t, err)
	go watcher.Watch()
	t.Cleanup(func() { watcher.Close() })
	return watcher, path, labels
}

func setupDir(t *testing.T) (dir string, path string) {
	t.Helper()
	dir, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path = filepath.Join(dir, logname)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	return dir, path
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
	defer f.Close()
	_, err = f.Write([]byte(data))
	require.NoError(t, err)
}

func TestIgnoresSymlinkTargetOutsideRoot(t *testing.T) {
	dir, err := ioutil.TempDir("", t.Name())
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// A secret file outside the watch root, with content whose size must never be counted.
	outside, err := ioutil.TempDir("", t.Name()+"-outside")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	secret := filepath.Join(outside, "secret")
	require.NoError(t, ioutil.WriteFile(secret, []byte("leak-me\n"), 0600))

	// A pod-log-shaped symlink under the watch root pointing at the secret.
	var l LogLabels
	link := filepath.Join(dir, logname)
	require.True(t, l.Parse(link))
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0700))
	require.NoError(t, os.Symlink(secret, link))

	// New() runs the initial Walk, which calls Update on the symlink.
	w, err := New(dir, 0)
	require.NoError(t, err)
	t.Cleanup(func() { w.Close() })

	counter, err := w.metrics.GetMetricWithLabelValues(l.Namespace, l.Name, l.UUID, l.Container)
	require.NoError(t, err)
	assert.Equal(t, float64(0), getCounterValue(counter), "must not count bytes from an out-of-root symlink target")
}

func TestUpdateIgnoresDirectoryWithoutCreatingSeries(t *testing.T) {
	dir, _ := setupDir(t)

	// A path that parses as LogLabels but is itself a directory.
	p := filepath.Join(dir, "myns_mypod_9a5888d1-e009-4cc3-bc19-c5543b4b84f7/mycontainer/2.log")
	require.NoError(t, os.MkdirAll(p, 0700))

	w, err := New(dir, 0)
	require.NoError(t, err)
	t.Cleanup(func() { w.Close() })

	require.NoError(t, w.Update(p))

	// No series should exist for a directory path.
	assert.Equal(t, 0, testutil.CollectAndCount(w.metrics))
}

func TestReconcilePrunesStaleTuple(t *testing.T) {
	dir, path := setupDir(t)
	require.NoError(t, ioutil.WriteFile(path, []byte(data), 0600))

	w, err := New(dir, 0) // timer disabled; call reconcile manually
	require.NoError(t, err)
	t.Cleanup(func() { w.Close() })

	// New's initial walk populated one series.
	require.Equal(t, 1, testutil.CollectAndCount(w.metrics))

	// Simulate a missed Remove: delete the pod dir from disk WITHOUT events.
	podDir := filepath.Join(dir,
		"openshift-monitoring_prometheus-k8s-0_9a5888d1-e009-4cc3-bc19-c5543b4b84f7")
	require.NoError(t, os.RemoveAll(podDir))

	w.reconcile()

	assert.Equal(t, 0, testutil.CollectAndCount(w.metrics))
	var l LogLabels
	require.True(t, l.Parse(path))
	w.mutex.RLock()
	_, ok := w.sizes[l]
	w.mutex.RUnlock()
	assert.False(t, ok, "sizes entry should be pruned")
}

func TestPruneKeepsPodStillOnDisk(t *testing.T) {
	// Simulates the walk/prune race: a pod present on disk but absent from the
	// (stale) live set must NOT be pruned, thanks to the existence re-check.
	dir, path := setupDir(t)
	require.NoError(t, ioutil.WriteFile(path, []byte(data), 0600))

	w, err := New(dir, 0)
	require.NoError(t, err)
	t.Cleanup(func() { w.Close() })
	require.Equal(t, 1, testutil.CollectAndCount(w.metrics))

	// Prune with an EMPTY live set (as if the walk missed this pod). The pod's
	// files still exist on disk, so podDirExists must protect it.
	w.prune(map[LogLabels]struct{}{})

	assert.Equal(t, 1, testutil.CollectAndCount(w.metrics))
}

func TestReconcileLoopPrunesOnTimer(t *testing.T) {
	dir, path := setupDir(t)
	require.NoError(t, ioutil.WriteFile(path, []byte(data), 0600))

	w, err := New(dir, 50*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() { w.Close() })
	require.Equal(t, 1, testutil.CollectAndCount(w.metrics))

	// Delete the pod dir from disk without emitting events.
	podDir := filepath.Join(dir,
		"openshift-monitoring_prometheus-k8s-0_9a5888d1-e009-4cc3-bc19-c5543b4b84f7")
	require.NoError(t, os.RemoveAll(podDir))

	// The timer-driven reconcile should prune the stale series.
	assert.Eventually(t, func() bool {
		return testutil.CollectAndCount(w.metrics) == 0
	}, 2*time.Second, 20*time.Millisecond, "stale series should be pruned by the loop")
}

func TestWatchReturnsAfterClose(t *testing.T) {
	// Regression: Watch's loop had no exit condition, so after Close each
	// worker got io.EOF, returned instantly, and the loop respawned forever —
	// a busy-looping goroutine leak per watcher. Under -count/-cpu 1 the leaked
	// spinners starved the inotify-based tests past their Eventually windows.
	dir, _ := setupDir(t)

	w, err := New(dir, 0)
	require.NoError(t, err)

	done := make(chan struct{})
	go func() { _ = w.Watch(); close(done) }()

	// Let Watch spin up its worker goroutines before closing.
	time.Sleep(50 * time.Millisecond)
	w.Close()

	select {
	case <-done:
		// Watch returned: no infinite respawn loop.
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not return after Close; goroutine leaked")
	}
}

func TestTriggerReconcileDrivesLoopPrune(t *testing.T) {
	// The inotify-overflow branch in processNextEvent calls triggerReconcile();
	// this exercises that path end-to-end: an on-demand trigger must make the
	// reconcileLoop run a full reconcile that prunes stale series. (Fabricating
	// a real inotify queue overflow is not feasible in a unit test.)
	dir, path := setupDir(t)
	require.NoError(t, ioutil.WriteFile(path, []byte(data), 0600))

	w, err := New(dir, 0) // timer disabled; only the on-demand trigger drives reconcile
	require.NoError(t, err)
	t.Cleanup(func() { w.Close() })
	require.Equal(t, 1, testutil.CollectAndCount(w.metrics))

	// Delete the pod dir from disk without emitting events, then request a
	// reconcile the same way the overflow handler does.
	podDir := filepath.Join(dir,
		"openshift-monitoring_prometheus-k8s-0_9a5888d1-e009-4cc3-bc19-c5543b4b84f7")
	require.NoError(t, os.RemoveAll(podDir))

	w.triggerReconcile()

	assert.Eventually(t, func() bool {
		return testutil.CollectAndCount(w.metrics) == 0
	}, 2*time.Second, 20*time.Millisecond, "on-demand trigger should drive a reconcile that prunes")
}

func TestCloseIsIdempotent(t *testing.T) {
	dir, _ := setupDir(t)

	w, err := New(dir, 0)
	require.NoError(t, err)

	w.Close()
	assert.NotPanics(t, func() { w.Close() }, "second Close must not panic")
}

func TestTriggerReconcileCoalesces(t *testing.T) {
	w := &Watcher{reconcileNow: make(chan struct{}, 1)}
	w.triggerReconcile()
	w.triggerReconcile() // second call must not block or panic
	assert.Equal(t, 1, len(w.reconcileNow), "reconcile requests should coalesce to one")
}
