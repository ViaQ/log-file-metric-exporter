package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/log-file-metric-exporter/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The metric labels are parsed out of the path, so the tree has to look like
// the real /var/log/pods: <namespace>_<pod>_<uuid>/<container>/<restart>.log
const (
	testUUID      = "19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e"
	testNamespace = "testns"
	testPod       = "testpod"
	testContainer = "testcontainer"
)

const settle = 5 * time.Second

// start brings up a watcher over a fresh tree and returns the container
// directory to write logs into, plus the registry its metrics land in.
func start(t *testing.T) (containerDir string, reg *prometheus.Registry) {
	t.Helper()

	root := t.TempDir()
	containerDir = filepath.Join(root,
		fmt.Sprintf("%s_%s_%s", testNamespace, testPod, testUUID), testContainer)
	require.NoError(t, os.MkdirAll(containerDir, 0755))

	m := metrics.New()
	reg = prometheus.NewRegistry()
	require.NoError(t, m.Register(reg))

	w, err := New(root, m)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(settle):
			t.Log("watcher did not stop within the timeout")
		}
	})

	return containerDir, reg
}

// logged returns the byte count recorded for the test container, and whether a
// series exists for it at all.
func logged(t *testing.T, reg *prometheus.Registry) (float64, bool) {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range families {
		if mf.GetName() != "log_logged_bytes_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "containername" && l.GetValue() == testContainer {
					return m.GetCounter().GetValue(), true
				}
			}
		}
	}
	return 0, false
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	require.NoError(t, err)
	_, err = f.WriteString(line)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

// eventuallyLogged waits for the recorded byte count to reach want. Events are
// handled asynchronously, so every assertion here has to be a poll.
func eventuallyLogged(t *testing.T, reg *prometheus.Registry, want float64, msg string) {
	t.Helper()
	var last float64
	ok := assert.Eventually(t, func() bool {
		v, found := logged(t, reg)
		last = v
		return found && v == want
	}, settle, 20*time.Millisecond)
	if !ok {
		t.Fatalf("%s: recorded %v bytes, want %v", msg, last, want)
	}
}

// A log file created after the watcher is running should be picked up and its
// bytes counted.
func TestWatcherCountsNewFile(t *testing.T) {
	dir, reg := start(t)

	line := "hello from a container\n"
	appendLine(t, filepath.Join(dir, "0.log"), line)

	eventuallyLogged(t, reg, float64(len(line)), "after creating a log file")
}

// Appending to a watched file must add only what was appended.
func TestWatcherCountsAppends(t *testing.T) {
	dir, reg := start(t)
	path := filepath.Join(dir, "0.log")

	first := "first line\n"
	appendLine(t, path, first)
	eventuallyLogged(t, reg, float64(len(first)), "after the first write")

	second := "second line\n"
	appendLine(t, path, second)
	eventuallyLogged(t, reg, float64(len(first)+len(second)), "after appending")
}

// Rotation moves logging to a new file. All rotations of one container share a
// single series, so the total must carry across rather than restart.
func TestWatcherAccumulatesAcrossRotation(t *testing.T) {
	dir, reg := start(t)

	before := "before rotation\n"
	appendLine(t, filepath.Join(dir, "0.log"), before)
	eventuallyLogged(t, reg, float64(len(before)), "before rotation")

	after := "after rotation\n"
	appendLine(t, filepath.Join(dir, "1.log"), after)
	eventuallyLogged(t, reg, float64(len(before)+len(after)), "after rotating to 1.log")
}

// When a log file is removed the series should go with it, so a deleted
// container stops being reported rather than lingering at its last value.
func TestWatcherForgetsDeletedFile(t *testing.T) {
	dir, reg := start(t)
	path := filepath.Join(dir, "0.log")

	line := "some output\n"
	appendLine(t, path, line)
	eventuallyLogged(t, reg, float64(len(line)), "before deleting")

	require.NoError(t, os.Remove(path))

	assert.Eventually(t, func() bool {
		_, found := logged(t, reg)
		return !found
	}, settle, 20*time.Millisecond, "series should be dropped once the file is gone")
}

// Files that already exist when the watcher starts must be counted too, not
// only those created later.
func TestWatcherCountsPreExistingFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root,
		fmt.Sprintf("%s_%s_%s", testNamespace, testPod, testUUID), testContainer)
	require.NoError(t, os.MkdirAll(dir, 0755))

	line := "written before the watcher started\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "0.log"), []byte(line), 0644))

	m := metrics.New()
	reg := prometheus.NewRegistry()
	require.NoError(t, m.Register(reg))

	w, err := New(root, m)
	require.NoError(t, err)
	defer w.Close()

	v, found := logged(t, reg)
	require.True(t, found, "pre-existing log file was not counted")
	assert.Equal(t, float64(len(line)), v)
}
