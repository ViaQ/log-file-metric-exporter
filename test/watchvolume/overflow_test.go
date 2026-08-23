package watchvolume

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/log-file-metric-exporter/pkg/symnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The inotify queue holds fs.inotify.max_queued_events entries (16384 by
// default). An IN_MODIFY watch enqueues one event per write, so the only thing
// keeping the queue below the cap is the exporter draining it faster than the
// containers fill it. Nothing enforces that. When it stops holding, the kernel
// discards the backlog, reports a single IN_Q_OVERFLOW, and the byte counts
// those events represented are gone for good.
//
// Each round below stops reading for a few seconds while writers hammer. The
// stall is deliberate and larger than anything routine — it stands in for a
// severe pause, and makes the failure reproducible in a few seconds rather than
// requiring a sustained overload. The point it demonstrates is structural: the
// backlog is bounded by the write rate, which the exporter does not control.

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

	w, err := symnotify.NewWatcher()
	require.NoError(t, err)
	require.NoError(t, w.Add(root))

	stop := make(chan struct{})
	wait := hammer(files, stop)
	time.Sleep(ovfStall) // nothing is reading the queue
	close(stop)
	res := overflowResult{writes: wait()}
	time.Sleep(ovfSettle)

	var delivered, overflows int64
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			if _, err := w.Event(); err != nil {
				if errors.Is(err, fsnotify.ErrEventOverflow) {
					atomic.AddInt64(&overflows, 1)
					continue
				}
				return // watcher closed
			}
			atomic.AddInt64(&delivered, 1)
		}
	}()
	time.Sleep(ovfDrain)
	_ = w.Close() // unblocks the drain goroutine
	<-drained

	res.delivered, res.overflows = atomic.LoadInt64(&delivered), atomic.LoadInt64(&overflows)
	return res
}

// TestQueueOverflowUnderLoad shows the queue overflowing and the backlog being
// thrown away. Heavy, and skipped in short mode.
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
	t.Logf("  the events behind each overflow were discarded by the kernel; the bytes")
	t.Logf("  they represented are not recoverable from any later event")

	require.Positive(t, total.writes, "the writers produced nothing")
	assert.Positive(t, total.overflows,
		"expected the IN_MODIFY watch to overflow the inotify queue under load")
}
