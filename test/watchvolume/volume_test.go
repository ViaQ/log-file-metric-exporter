// Package watchvolume measures how many inotify events the watcher receives for
// a given amount of logging.
//
// The watcher asks for IN_MODIFY, which the kernel reports for every write. The
// event rate is therefore whatever the containers on the node happen to log,
// and the exporter has no say in it. The inotify queue holds
// fs.inotify.max_queued_events entries (16384 by default); when it fills, the
// kernel discards the backlog and reports a single IN_Q_OVERFLOW. See
// overflow_test.go for that failure, and docs/watcher-defects.md for the
// measurements this test produced.
package watchvolume

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/log-file-metric-exporter/pkg/symnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	volFiles      = 200
	volWritesEach = 300
	volWrites     = volFiles * volWritesEach
	volSettle     = 2 * time.Second

	// idleReader drains as fast as it can, so the count is what the kernel
	// generated. busyReader spends a little time per event, which is what an
	// exporter doing real work on a loaded node looks like.
	idleReader = 0
	busyReader = 200 * time.Microsecond
)

type volumeResult struct {
	events    int64
	overflows int64
}

func (r volumeResult) perWrite() float64 { return float64(r.events) / float64(volWrites) }

func makeFiles(t *testing.T) (root string, files []string) {
	t.Helper()
	root = t.TempDir()
	for i := 0; i < volFiles; i++ {
		p := filepath.Join(root, fmt.Sprintf("%d.log", i))
		require.NoError(t, os.WriteFile(p, []byte("init\n"), 0644))
		files = append(files, p)
	}
	return root, files
}

// writeBurst appends to every file in turn, which is what a node full of chatty
// containers looks like. Round-robin across files defeats the kernel's
// coalescing of consecutive identical events, as separate containers would.
func writeBurst(t *testing.T, files []string) {
	t.Helper()
	handles := make([]*os.File, len(files))
	for i, p := range files {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0644)
		require.NoError(t, err)
		handles[i] = f
	}
	defer func() {
		for _, f := range handles {
			_ = f.Close()
		}
	}()

	line := []byte("a log line that a container just emitted\n")
	for w := 0; w < volWritesEach; w++ {
		for _, f := range handles {
			_, _ = f.Write(line)
		}
	}
}

// runWatcher drives the watcher this repository ships, counting what it sees.
func runWatcher(t *testing.T, perEvent time.Duration) volumeResult {
	t.Helper()
	root, files := makeFiles(t)

	w, err := symnotify.NewWatcher()
	require.NoError(t, err)
	require.NoError(t, w.Add(root))

	var events, overflows atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, err := w.Event(); err != nil {
				if errors.Is(err, fsnotify.ErrEventOverflow) {
					overflows.Add(1)
					continue
				}
				return // watcher closed
			}
			events.Add(1)
			if perEvent > 0 {
				time.Sleep(perEvent)
			}
		}
	}()

	writeBurst(t, files)
	time.Sleep(volSettle)
	_ = w.Close()
	<-done

	return volumeResult{events: events.Load(), overflows: overflows.Load()}
}

// TestEventVolume records how much the watcher is asked to handle. One event per
// write means the load is set by the containers, not by the exporter, and a
// reader that cannot keep up simply falls behind by the difference.
func TestEventVolume(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping event volume measurement in short mode")
	}

	idle := runWatcher(t, idleReader)
	busy := runWatcher(t, busyReader)

	t.Logf("%d writes across %d files (inotify queue holds %s)", volWrites, volFiles, queueCapacity(t))
	t.Logf("  idle reader: %6d events (%.2f per write), %d overflows",
		idle.events, idle.perWrite(), idle.overflows)
	t.Logf("  busy reader: %6d events (%.2f per write), %d overflows",
		busy.events, busy.perWrite(), busy.overflows)
	t.Logf("backlog under a busy reader: %d of %d generated events never consumed",
		idle.events-busy.events, idle.events)

	assert.Greater(t, idle.perWrite(), 0.9,
		"expected roughly one event per write from an IN_MODIFY watch")
}

func queueCapacity(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("/proc/sys/fs/inotify/max_queued_events")
	if err != nil || len(b) == 0 {
		return "unknown"
	}
	return string(b[:len(b)-1])
}
