// Package watchvolume measures how many inotify events the watcher receives for
// a given amount of logging.
//
// The watcher asks for IN_ONESHOT, so a watch reports the first write and then
// removes itself. Writes that land before the exporter re-arms produce nothing
// at all, which means event volume follows the re-arm rate rather than whatever
// the containers happen to log. That is what keeps the inotify queue bounded:
// see overflow_test.go, and docs/watcher-defects.md for what the IN_MODIFY
// watcher this replaced produced on the same workload.
//
// Nothing is lost by that coalescing. The handler stats the file, so one event
// still accounts for every write behind it.
package watchvolume

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/log-file-metric-exporter/internal/inotify"
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

// runWatcher drives the watcher this repository ships, re-arming after every
// event exactly as the watcher package does in production.
func runWatcher(t *testing.T, perEvent time.Duration) volumeResult {
	t.Helper()
	root, files := makeFiles(t)

	n, err := inotify.New(root)
	require.NoError(t, err)
	for _, f := range files {
		require.NoError(t, n.WatchLogFile(f))
	}
	go n.ReadLoop()

	var events, overflows atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range n.Events() {
			if e.IsOverFlowErr() {
				overflows.Add(1)
				continue
			}
			if !e.IsModify() && !e.IsCloseWrite() {
				continue
			}
			events.Add(1)
			if perEvent > 0 {
				time.Sleep(perEvent)
			}
			_ = n.WatchLogFile(e.Path)
		}
	}()

	writeBurst(t, files)
	time.Sleep(volSettle)
	_ = n.Close()
	<-done

	return volumeResult{events: events.Load(), overflows: overflows.Load()}
}

// TestEventVolume records how much the watcher is asked to handle. Under
// IN_ONESHOT the count is set by how fast the exporter re-arms, so a busier
// reader sees fewer events rather than falling further behind.
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

	require.Positive(t, idle.events, "the watcher reported nothing at all")
	assert.Less(t, idle.perWrite(), 0.9,
		"expected fewer events than writes; a ONESHOT watch should coalesce a burst")
	assert.Less(t, busy.events, idle.events,
		"a slower reader should re-arm less often and so see fewer events, not more")
}

func queueCapacity(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("/proc/sys/fs/inotify/max_queued_events")
	if err != nil || len(b) == 0 {
		return "unknown"
	}
	return string(b[:len(b)-1])
}
