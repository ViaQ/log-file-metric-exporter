package inotify

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	log "github.com/ViaQ/logerr/v2/log/static"
	"golang.org/x/sys/unix"
)

const (
	// FlagsWatchDir are the inotify flags for watching a directory.
	FlagsWatchDir = unix.IN_CREATE | unix.IN_DELETE

	// FlagsWatchFile are the inotify flags for watching a file.
	FlagsWatchFile = unix.IN_MODIFY | unix.IN_CLOSE_WRITE | unix.IN_ONESHOT

	// EventChanSize is the size of the event channel buffer.
	EventChanSize = 4096
)

// Notify is a wrapper around the Linux inotify API.
type Notify struct {
	rootDir         string
	fd              int
	inotifyFile     *os.File
	watches         map[string]int // path -> watch descriptor
	paths           map[int]string // watch descriptor -> path
	mtx             sync.RWMutex
	events          chan NotifyEvent
	done            chan struct{}
	rmWatchFailures atomic.Int64 // non-EINVAL errors from InotifyRmWatch
	eventsReceived  atomic.Int64 // events received from the kernel
}

// New creates a new Notify instance for the given root directory.
// It initializes the inotify file descriptor and watches the root directory.
func New(root string) (*Notify, error) {
	// Validate that root is a directory
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("root must be a directory")
	}

	// Create inotify instance with CLOEXEC and NONBLOCK flags
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}

	// Create os.File from the fd for easier reading
	inotifyFile := os.NewFile(uintptr(fd), "inotify")

	n := &Notify{
		rootDir:     root,
		fd:          fd,
		inotifyFile: inotifyFile,
		watches:     make(map[string]int),
		paths:       make(map[int]string),
		events:      make(chan NotifyEvent, EventChanSize),
		done:        make(chan struct{}),
	}

	// Watch the root directory
	if err := n.WatchDir(root); err != nil {
		n.inotifyFile.Close()
		return nil, err
	}

	return n, nil
}

// Events returns a read-only channel for receiving inotify events.
func (n *Notify) Events() <-chan NotifyEvent {
	return n.events
}

// RmWatchFailures returns how many times removing a watch before re-arming
// failed for a reason other than EINVAL. EINVAL is not counted: it is the
// normal outcome, meaning the kernel had already detached the mark. Anything
// else is unexpected and worth investigating.
func (n *Notify) RmWatchFailures() int64 {
	return n.rmWatchFailures.Load()
}

// EventsReceived returns the total number of watch events received from the kernel.
// This metric demonstrates watch health: steadily increasing means all watches are alive,
// freezing means watches have died (would occur without the ONESHOT race fix).
func (n *Notify) EventsReceived() int64 {
	return n.eventsReceived.Load()
}

// ReadLoop reads raw bytes from the inotify file descriptor and sends parsed events
// to the events channel. It runs until the done channel is closed.
func (n *Notify) ReadLoop() {
	var buf [unix.SizeofInotifyEvent * EventChanSize]byte
	defer func() {
		close(n.events)
		n.cleanup()
	}()

	for {
		select {
		case <-n.done:
			return
		default:
		}

		nn, err := n.inotifyFile.Read(buf[:])
		if err != nil {
			select {
			case <-n.done:
				return
			default:
			}
			if errors.Is(err, io.EOF) {
				return
			}
			if errors.Is(err, syscall.EAGAIN) {
				select {
				case <-n.done:
					return
				case <-time.After(50 * time.Millisecond):
				}
				continue
			}
			log.Error(err, "error reading inotify fd")
			return
		}

		// Parse events from the buffer
		var offset uint32 = 0
		for offset <= uint32(nn-unix.SizeofInotifyEvent) {
			raw := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
			nameBytes := buf[offset+unix.SizeofInotifyEvent : offset+unix.SizeofInotifyEvent+raw.Len]
			path := trimNull(string(nameBytes))

			// Get the watched path from the watch descriptor
			n.mtx.RLock()
			watchedPath := n.paths[int(raw.Wd)]
			n.mtx.RUnlock()

			// Combine watch descriptor path with event name. Join ignores an
			// empty name, which is what a file watch reports.
			if watchedPath != "" {
				path = filepath.Join(watchedPath, path)
			}

			offset += unix.SizeofInotifyEvent + raw.Len

			e := NotifyEvent{
				InotifyEvent: *raw,
				Path:         path,
			}

			n.eventsReceived.Add(1)

			select {
			case n.events <- e:
			case <-n.done:
				return
			}
		}
	}
}

// Close signals ReadLoop to stop and unblocks any pending Read.
func (n *Notify) Close() error {
	select {
	case <-n.done:
		return nil
	default:
		close(n.done)
	}
	// Close the file to unblock any pending Read call.
	// ReadLoop's defer will handle the rest of cleanup.
	n.inotifyFile.Close()
	return nil
}

// cleanup removes all watches and closes the inotify file.
// It must be called when ReadLoop exits.
func (n *Notify) cleanup() {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	for path := range n.watches {
		delete(n.watches, path)
	}
	n.paths = make(map[int]string)
}

// trimNull removes trailing null bytes from a string.
func trimNull(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return s[:i]
		}
	}
	return s
}
