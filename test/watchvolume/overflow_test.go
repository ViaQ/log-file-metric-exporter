package watchvolume

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/log-file-metric-exporter/internal/inotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The inotify queue holds fs.inotify.max_queued_events entries (16384 by
// default). The IN_MODIFY watcher this replaced enqueued one event per write,
// so nothing bounded the backlog except the exporter keeping up; when that
// stopped holding, the kernel discarded the backlog and the byte counts behind
// it were gone. docs/watcher-defects.md has that measurement.
//
// A ONESHOT watch removes itself after the first event, so a file with an
// unhandled event contributes nothing further however hard it is written. The
// backlog is bounded by the number of watched files rather than by the write
// rate, which is why the queue cannot run away.
//
// This test applies the load that broke the old watcher: the reader stops for
// seconds at a time while writers hammer. The stall is deliberate and larger
// than anything routine.

const (
	ovfWriters = 4
	ovfRounds  = 3
	ovfStall   = 4 * time.Second        // reader stopped; writers hammering
	ovfDrain   = 2 * time.Second        // long enough to empty a full queue
	ovfSettle  = 200 * time.Millisecond // let the last writes reach the kernel
)

type overflowResult struct {
	writes    int64
	delivered int64
	overflows int64
}

func (r *overflowResult) add(o overflowResult) {
	r.writes += o.writes
	r.delivered += o.delivered
	r.overflows += o.overflows
}

// hammer appends to every file as fast as the CPU allows until stop is closed,
// and reports how many writes it managed.
func hammer(files []string, stop <-chan struct{}) (wait func() int64) {
	var writes int64
	var wg sync.WaitGroup
	for w := 0; w < ovfWriters; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			var handles []*os.File
			for i := id; i < len(files); i += ovfWriters {
				f, err := os.OpenFile(files[i], os.O_APPEND|os.O_WRONLY, 0644)
				if err != nil {
					return
				}
				handles = append(handles, f)
			}
			defer func() {
				for _, f := range handles {
					_ = f.Close()
				}
			}()
			line := []byte("a log line that a container just emitted\n")
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, f := range handles {
					if _, err := f.Write(line); err == nil {
						atomic.AddInt64(&writes, 1)
					}
				}
			}
		}(w)
	}
	return func() int64 {
		wg.Wait()
		return atomic.LoadInt64(&writes)
	}
}

// overflowRound stalls the reader through one burst, then drains what survived.
// Each round gets a fresh watcher so nothing drains during the next stall.
func overflowRound(t *testing.T) overflowResult {
	t.Helper()
	root, files := makeFiles(t)

	n, err := inotify.New(root)
	require.NoError(t, err)
	for _, f := range files {
		require.NoError(t, n.WatchLogFile(f))
	}

	// ReadLoop only starts once the burst is over, so nothing is draining the
	// kernel queue during the stall.
	stop := make(chan struct{})
	wait := hammer(files, stop)
	time.Sleep(ovfStall)
	close(stop)
	res := overflowResult{writes: wait()}
	time.Sleep(ovfSettle)

	var delivered, overflows int64
	drained := make(chan struct{})
	go n.ReadLoop()
	go func() {
		defer close(drained)
		for e := range n.Events() {
			if e.IsOverFlowErr() {
				atomic.AddInt64(&overflows, 1)
				continue
			}
			if !e.IsModify() && !e.IsCloseWrite() {
				continue
			}
			atomic.AddInt64(&delivered, 1)
			_ = n.WatchLogFile(e.Path)
		}
	}()
	time.Sleep(ovfDrain)
	_ = n.Close()
	<-drained

	res.delivered, res.overflows = atomic.LoadInt64(&delivered), atomic.LoadInt64(&overflows)
	return res
}

// TestQueueOverflowUnderLoad applies the load that overflowed the queue for the
// IN_MODIFY watcher. Heavy, and skipped in short mode.
func TestQueueOverflowUnderLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping heavy queue overflow test in short mode")
	}

	var total overflowResult
	for round := 0; round < ovfRounds; round++ {
		total.add(overflowRound(t))
	}

	t.Logf("inotify queue holds %s events; %d files, %d rounds of %s under load",
		queueCapacity(t), volFiles, ovfRounds, ovfStall)
	t.Logf("  %d writes -> %d events delivered, %d overflows",
		total.writes, total.delivered, total.overflows)

	require.Positive(t, total.writes, "the writers produced nothing")
	assert.Zero(t, total.overflows,
		"a ONESHOT watch holds at most one pending event per file, so the queue should stay far below the cap")
	assert.LessOrEqual(t, total.delivered, int64(volFiles*ovfRounds),
		"delivered more events than there are watched files, so a watch fired more than once")
}
