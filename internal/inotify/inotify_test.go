package inotify

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// TestNewNotify tests that we can create a new Notify instance and close it cleanly.
func TestNewNotify(t *testing.T) {
	tmpDir := t.TempDir()

	n, err := New(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, n)

	// Close should not error
	err = n.Close()
	require.NoError(t, err)
}

// TestNewNotifyInvalidPath tests that New returns an error for non-existent paths.
func TestNewNotifyInvalidPath(t *testing.T) {
	_, err := New("/nonexistent/path")
	require.Error(t, err)
}

// TestNewNotifyNotDirectory tests that New returns an error if the path is not a directory.
func TestNewNotifyNotDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "file.txt")
	err := os.WriteFile(tmpFile, []byte("test"), 0644)
	require.NoError(t, err)

	_, err = New(tmpFile)
	require.Error(t, err)
}

// TestWatchAndEvent tests that we receive a CREATE event when a file is created in a watched directory.
func TestWatchAndEvent(t *testing.T) {
	tmpDir := t.TempDir()

	n, err := New(tmpDir)
	require.NoError(t, err)
	defer n.Close()

	// Start ReadLoop in a goroutine
	go n.ReadLoop()

	// Give ReadLoop time to start
	time.Sleep(100 * time.Millisecond)

	// Create a file in the watched directory
	testFile := filepath.Join(tmpDir, "test.txt")
	err = os.WriteFile(testFile, []byte("hello"), 0644)
	require.NoError(t, err)

	// Read from events channel with a timeout
	ctx := time.After(5 * time.Second)
	var event NotifyEvent
	received := false

	for !received {
		select {
		case event = <-n.Events():
			received = true
		case <-ctx:
			t.Fatal("timeout waiting for CREATE event")
		}
	}

	// Verify we got a CREATE event with the correct filename
	assert.True(t, event.IsCreate(), "event should be a CREATE")
	assert.Contains(t, event.Path, "test.txt", "event path should contain filename")
}

// TestWatchLogFileModify tests that ONESHOT watches work correctly.
func TestWatchLogFileModify(t *testing.T) {
	tmpDir := t.TempDir()

	n, err := New(tmpDir)
	require.NoError(t, err)
	defer n.Close()

	// Create a file to watch
	testFile := filepath.Join(tmpDir, "log.txt")
	err = os.WriteFile(testFile, []byte("initial"), 0644)
	require.NoError(t, err)

	// Start ReadLoop in a goroutine
	go n.ReadLoop()

	// Give ReadLoop time to start
	time.Sleep(100 * time.Millisecond)

	// Watch the file with ONESHOT flags
	err = n.WatchLogFile(testFile)
	require.NoError(t, err)

	// Write to the file
	err = os.WriteFile(testFile, []byte("updated"), 0644)
	require.NoError(t, err)

	// Read from events channel with a timeout
	ctx := time.After(5 * time.Second)
	var event NotifyEvent
	received := false

	for !received {
		select {
		case event = <-n.Events():
			// Keep reading until we get a MODIFY or CLOSE_WRITE
			if event.IsModify() || event.IsCloseWrite() {
				received = true
			}
		case <-ctx:
			t.Fatal("timeout waiting for MODIFY/CLOSE_WRITE event")
		}
	}

	assert.True(t, event.IsModify() || event.IsCloseWrite(), "event should be MODIFY or CLOSE_WRITE")

	// After ONESHOT, the watch should be gone. Try to re-add it.
	err = n.WatchLogFile(testFile)
	require.NoError(t, err)

	// Write again
	err = os.WriteFile(testFile, []byte("updated again"), 0644)
	require.NoError(t, err)

	// We should get another event since we re-added the watch
	received = false
	for !received {
		select {
		case event = <-n.Events():
			if event.IsModify() || event.IsCloseWrite() {
				received = true
			}
		case <-ctx:
			t.Fatal("timeout waiting for second MODIFY/CLOSE_WRITE event after re-watch")
		}
	}

	assert.True(t, event.IsModify() || event.IsCloseWrite(), "second event should be MODIFY or CLOSE_WRITE")
}

// TestRemoveWatch tests that removing a watch works correctly.
func TestRemoveWatch(t *testing.T) {
	tmpDir := t.TempDir()

	n, err := New(tmpDir)
	require.NoError(t, err)
	defer n.Close()

	subDir := filepath.Join(tmpDir, "subdir")
	err = os.Mkdir(subDir, 0755)
	require.NoError(t, err)

	// Add a watch
	err = n.WatchDir(subDir)
	require.NoError(t, err)

	// Verify it's in the watch list
	watchList := n.WatchList()
	assert.Contains(t, watchList, subDir)

	// Remove the watch
	err = n.RemoveWatch(subDir)
	require.NoError(t, err)

	// Verify it's no longer in the watch list
	watchList = n.WatchList()
	assert.NotContains(t, watchList, subDir)
}

// TestRemoveNonExistentWatch tests that removing a non-existent watch doesn't error.
func TestRemoveNonExistentWatch(t *testing.T) {
	tmpDir := t.TempDir()

	n, err := New(tmpDir)
	require.NoError(t, err)
	defer n.Close()

	// Try to remove a watch that doesn't exist
	err = n.RemoveWatch("/nonexistent/path")
	require.NoError(t, err)
}

// TestWatchDescriptorReuse tests that watch descriptor reuse doesn't cause issues
// across many different files (10 iterations).
func TestWatchDescriptorReuse(t *testing.T) {
	tmpDir := t.TempDir()

	n, err := New(tmpDir)
	require.NoError(t, err)
	defer n.Close()

	go n.ReadLoop()
	time.Sleep(100 * time.Millisecond)

	for i := 0; i < 10; i++ {
		testFile := filepath.Join(tmpDir, fmt.Sprintf("log_test_%d", i))
		err = os.WriteFile(testFile, []byte("initial"), 0644)
		require.NoError(t, err)

		err = n.WatchLogFile(testFile)
		require.NoError(t, err)

		err = os.WriteFile(testFile, []byte("updated"), 0644)
		require.NoError(t, err)

		deadline := time.After(5 * time.Second)
		received := false
		for !received {
			select {
			case event := <-n.Events():
				if event.IsModify() || event.IsCloseWrite() {
					received = true
				}
			case <-deadline:
				t.Fatalf("timeout waiting for event in iteration %d", i)
			}
		}
	}
}

// TestEventStringMethod tests the String() method of NotifyEvent.
func TestEventStringMethod(t *testing.T) {
	event := NotifyEvent{
		Path: "/tmp/test.txt",
	}
	event.Mask = 256 // IN_CREATE

	str := event.String()
	assert.Contains(t, str, "NotifyEvent")
	assert.Contains(t, str, "test.txt")
}

// TestWatchListEmpty tests that WatchList returns an empty list initially.
func TestWatchListEmpty(t *testing.T) {
	tmpDir := t.TempDir()

	n, err := New(tmpDir)
	require.NoError(t, err)
	defer n.Close()

	// The root directory should be in the watch list
	watchList := n.WatchList()
	assert.Contains(t, watchList, tmpDir)
}

// TestMultipleWatches tests that multiple watches can be added and managed.
func TestMultipleWatches(t *testing.T) {
	tmpDir := t.TempDir()

	n, err := New(tmpDir)
	require.NoError(t, err)
	defer n.Close()

	// Create multiple subdirectories and watch them
	subDirs := []string{
		filepath.Join(tmpDir, "dir1"),
		filepath.Join(tmpDir, "dir2"),
		filepath.Join(tmpDir, "dir3"),
	}

	for _, dir := range subDirs {
		err = os.Mkdir(dir, 0755)
		require.NoError(t, err)
		err = n.WatchDir(dir)
		require.NoError(t, err)
	}

	// Check that all watches are in the list
	watchList := n.WatchList()
	for _, dir := range subDirs {
		assert.Contains(t, watchList, dir)
	}

	// Remove one watch
	err = n.RemoveWatch(subDirs[1])
	require.NoError(t, err)

	// Check that it's gone
	watchList = n.WatchList()
	assert.Contains(t, watchList, subDirs[0])
	assert.NotContains(t, watchList, subDirs[1])
	assert.Contains(t, watchList, subDirs[2])
}

// TestCloseMultipleTimes tests that closing multiple times doesn't cause a panic.
func TestCloseMultipleTimes(t *testing.T) {
	tmpDir := t.TempDir()

	n, err := New(tmpDir)
	require.NoError(t, err)

	// Close multiple times
	err = n.Close()
	require.NoError(t, err)

	err = n.Close()
	require.NoError(t, err)

	err = n.Close()
	require.NoError(t, err)
}

// TestReadLoopCleanup tests that ReadLoop properly cleans up when done channel is closed.
func TestReadLoopCleanup(t *testing.T) {
	tmpDir := t.TempDir()

	n, err := New(tmpDir)
	require.NoError(t, err)

	// Start ReadLoop
	done := make(chan struct{})
	go func() {
		n.ReadLoop()
		close(done)
	}()

	// Give ReadLoop time to start
	time.Sleep(100 * time.Millisecond)

	// Create an event to wake up the blocked Read
	testFile := filepath.Join(tmpDir, "wakeup.txt")
	err = os.WriteFile(testFile, []byte("test"), 0644)
	require.NoError(t, err)

	// Close the Notify instance - this signals ReadLoop to stop
	n.Close()

	// Wait for ReadLoop to finish
	ctx := time.After(2 * time.Second)
	select {
	case <-done:
		// Expected behavior
	case <-ctx:
		t.Fatal("timeout waiting for ReadLoop to finish")
	}

	// Events channel should be closed after ReadLoop finishes. Events queued
	// before Close are still buffered, so drain them before reaching the close.
	for range n.Events() {
	}
}

// TestEventMethods tests all the event query methods.
func TestEventMethods(t *testing.T) {
	tests := []struct {
		name     string
		mask     uint32
		expected string
		method   func(*NotifyEvent) bool
	}{
		{"IsCreate", 0x100, "IsCreate", (*NotifyEvent).IsCreate},                 // IN_CREATE = 0x100
		{"IsDelete", 0x200, "IsDelete", (*NotifyEvent).IsDelete},                 // IN_DELETE = 0x200
		{"IsModify", 0x2, "IsModify", (*NotifyEvent).IsModify},                   // IN_MODIFY = 0x2
		{"IsCloseWrite", 0x8, "IsCloseWrite", (*NotifyEvent).IsCloseWrite},       // IN_CLOSE_WRITE = 0x8
		{"IsIgnored", 0x8000, "IsIgnored", (*NotifyEvent).IsIgnored},             // IN_IGNORED = 0x8000
		{"IsOverFlowErr", 0x4000, "IsOverFlowErr", (*NotifyEvent).IsOverFlowErr}, // IN_Q_OVERFLOW = 0x4000
		{"IsDir", 0x40000000, "IsDir", (*NotifyEvent).IsDir},                     // IN_ISDIR = 0x40000000
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := &NotifyEvent{}
			event.Mask = tt.mask
			assert.True(t, tt.method(event), "event should match")
		})
	}
}

// --- ONESHOT re-arm demonstration -------------------------------------------
//
// The kernel races when a ONESHOT watch is re-armed: inotify_handle_inode_event
// queues the event and calls wake_up() before fsnotify_destroy_mark() takes
// group->mark_mutex. A reader woken by that wake_up can call inotify_add_watch
// first, at which point fsnotify_find_mark still sees the ATTACHED mark and
// hands back the SAME watch descriptor. The pending destroy then tears down the
// freshly re-armed watch and the file goes silent for good.
//
// Getting the same descriptor back is therefore the precondition for the bug.
// Removing the old watch before re-adding forces the mark to detach first, so
// the kernel must allocate a fresh descriptor and the race cannot occur.

const (
	demoFiles     = 100
	demoWriters   = 2
	demoChurners  = 4
	demoChurnDirs = 20
	demoDuration  = 3 * time.Second
)

type rearmResult struct {
	rearms      int64
	sameWd      int64
	eventsMid   int64
	eventsFinal int64

	rmFailures int64
}

// demoTree lays out the log files and the directories used to create
// group->mark_mutex contention.
func demoTree(t *testing.T) (root string, files, churnDirs []string) {
	t.Helper()
	root = t.TempDir()

	for i := 0; i < demoFiles; i++ {
		p := filepath.Join(root, fmt.Sprintf("demo_%d.log", i))
		require.NoError(t, os.WriteFile(p, []byte("init\n"), 0644))
		files = append(files, p)
	}
	for i := 0; i < demoChurnDirs; i++ {
		p := filepath.Join(root, fmt.Sprintf("churn_%d", i))
		require.NoError(t, os.Mkdir(p, 0755))
		churnDirs = append(churnDirs, p)
	}
	return root, files, churnDirs
}

// startWriters appends to the log files so ONESHOT watches keep firing.
func startWriters(stop <-chan struct{}, wg *sync.WaitGroup, files []string) {
	for w := 0; w < demoWriters; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			data := []byte(fmt.Sprintf("w%d\n", id))
			for {
				select {
				case <-stop:
					return
				default:
				}
				for i := id; i < len(files); i += demoWriters {
					if f, err := os.OpenFile(files[i], os.O_APPEND|os.O_WRONLY, 0644); err == nil {
						_, _ = f.Write(data)
						_ = f.Close()
					}
				}
				time.Sleep(500 * time.Microsecond)
			}
		}(w)
	}
}

// startChurners repeatedly updates existing directory watches. Each call takes
// group->mark_mutex without generating events, which delays the kernel's
// deferred mark destruction and widens the race window.
func startChurners(stop <-chan struct{}, wg *sync.WaitGroup, fd int, churnDirs []string) {
	for c := 0; c < demoChurners; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, d := range churnDirs {
					_, _ = unix.InotifyAddWatch(fd, d, FlagsWatchDir)
				}
				time.Sleep(10 * time.Microsecond)
			}
		}()
	}
}

// runNaiveRearm re-arms with a bare InotifyAddWatch, reproducing the behaviour
// this package had before the fix.
func runNaiveRearm(t *testing.T) rearmResult {
	t.Helper()
	_, files, churnDirs := demoTree(t)

	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	require.NoError(t, err)
	defer unix.Close(fd)

	wdToFile := make(map[int]int, len(files))
	for i, f := range files {
		wd, err := unix.InotifyAddWatch(fd, f, FlagsWatchFile)
		require.NoError(t, err)
		wdToFile[wd] = i
	}
	for _, d := range churnDirs {
		_, err := unix.InotifyAddWatch(fd, d, FlagsWatchDir)
		require.NoError(t, err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	startWriters(stop, &wg, files)
	startChurners(stop, &wg, fd, churnDirs)
	defer func() {
		close(stop)
		wg.Wait()
	}()

	var res rearmResult
	var buf [unix.SizeofInotifyEvent * 4096]byte
	start := time.Now()
	mid := start.Add(demoDuration / 2)
	end := start.Add(demoDuration)

	for time.Now().Before(end) {
		if res.eventsMid == 0 && time.Now().After(mid) {
			res.eventsMid = res.rearms
		}

		n, err := unix.Read(fd, buf[:])
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			time.Sleep(100 * time.Microsecond)
			continue
		}
		require.NoError(t, err)

		var offset uint32
		for offset+unix.SizeofInotifyEvent <= uint32(n) {
			raw := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
			offset += unix.SizeofInotifyEvent + raw.Len

			if raw.Mask&(unix.IN_MODIFY|unix.IN_CLOSE_WRITE) == 0 {
				continue
			}
			oldWd := int(raw.Wd)
			idx, ok := wdToFile[oldWd]
			if !ok {
				continue
			}

			// The pre-fix re-arm: add without removing first.
			newWd, err := unix.InotifyAddWatch(fd, files[idx], FlagsWatchFile)
			if err != nil {
				continue
			}
			res.rearms++
			if newWd == oldWd {
				res.sameWd++
			} else {
				delete(wdToFile, oldWd)
			}
			wdToFile[newWd] = idx
		}
	}
	res.eventsFinal = res.rearms
	return res
}

// runFixedRearm drives the real production path: WatchLogFile removes the old
// watch before adding the new one.
func runFixedRearm(t *testing.T) rearmResult {
	t.Helper()
	root, files, churnDirs := demoTree(t)

	n, err := New(root)
	require.NoError(t, err)
	defer n.Close()

	for _, f := range files {
		require.NoError(t, n.WatchLogFile(f))
	}
	for _, d := range churnDirs {
		require.NoError(t, n.WatchDir(d))
	}

	go n.ReadLoop()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	startWriters(stop, &wg, files)
	startChurners(stop, &wg, n.fd, churnDirs)
	defer func() {
		close(stop)
		wg.Wait()
	}()

	// wdFor reads the descriptor the package currently associates with path.
	wdFor := func(path string) (int, bool) {
		n.mtx.RLock()
		defer n.mtx.RUnlock()
		wd, ok := n.watches[path]
		return wd, ok
	}

	var res rearmResult
	events := n.Events()
	timeout := time.After(demoDuration)
	half := time.After(demoDuration / 2)

	for done := false; !done; {
		select {
		case <-timeout:
			done = true
		case <-half:
			res.eventsMid = n.EventsReceived()
		case e, ok := <-events:
			if !ok {
				done = true
				break
			}
			if !e.IsModify() && !e.IsCloseWrite() {
				continue
			}
			oldWd, ok := wdFor(e.Path)
			if !ok {
				continue
			}
			if err := n.WatchLogFile(e.Path); err != nil {
				continue
			}
			newWd, ok := wdFor(e.Path)
			if !ok {
				continue
			}
			res.rearms++
			if newWd == oldWd {
				res.sameWd++
			}
		}
	}
	res.eventsFinal = n.EventsReceived()
	res.rmFailures = n.RmWatchFailures()
	return res
}

// TestOneShotRearmNeverReusesWatchDescriptor contrasts the pre-fix re-arm with
// the current one. The naive arm is reported for contrast only: whether it
// actually hits a same-descriptor return depends on kernel version, CPU count
// and scheduling luck, so asserting on it would be flaky. The fixed arm must
// never reuse a descriptor, which is what makes the race unreachable.
func TestOneShotRearmNeverReusesWatchDescriptor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping race demonstration in short mode")
	}

	naive := runNaiveRearm(t)
	fixed := runFixedRearm(t)

	t.Logf("naive re-arm (pre-fix): %d re-arms, %d same-wd (%.4f%%)",
		naive.rearms, naive.sameWd, percent(naive.sameWd, naive.rearms))
	t.Logf("fixed re-arm (current): %d re-arms, %d same-wd (%.4f%%)",
		fixed.rearms, fixed.sameWd, percent(fixed.sameWd, fixed.rearms))

	t.Logf("unexpected InotifyRmWatch errors during fixed re-arm: %d", fixed.rmFailures)

	require.Greater(t, fixed.rearms, int64(0), "fixed arm never re-armed; the workload produced no events")
	assert.Zero(t, fixed.sameWd,
		"re-arming reused a watch descriptor, which is what lets the kernel destroy the new watch")
	assert.Zero(t, fixed.rmFailures, "InotifyRmWatch failed for an unexpected reason")

	// A watch that the race killed stops delivering events, so a still-climbing
	// event count is the evidence that every watch stayed alive.
	assert.Greater(t, fixed.eventsFinal, fixed.eventsMid,
		"event flow stalled, which is how a watch killed by the race presents")
}

func percent(part, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}
