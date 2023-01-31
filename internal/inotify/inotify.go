package inotify

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/golang/glog"
	"golang.org/x/sys/unix"
)

const (
	FlagsWatchDir = unix.IN_CREATE | // File/directory created in watched directory
		unix.IN_DELETE // File/directory deleted from watched directory.
		//unix.IN_DELETE_SELF //  Watched file/directory was itself deleted.

	FlagsWatchFile = unix.IN_MODIFY | // File was modified (e.g., write(2), truncate(2)).
		unix.IN_CLOSE_WRITE | // File opened for writing was closed.
		unix.IN_ONESHOT // Monitor the filesystem object corresponding to pathname for one event, then remove from watch list.

	EventChanSize = 4096

	RootDir = "/var/log/pods"
	SelfDir = "logwatcher"
)

var (
	sizemtx              = sync.Mutex{}
	Sizes                = map[string]int64{}
	HandledEvents uint64 = 0
	SentEvents    uint64 = 0
)

type Notify struct {
	rootDir     string
	namespaces  map[string]struct{}
	fd          int
	inotifyFile *os.File // used for read()ing events
	watches     map[string]int
	paths       map[int]string
	mtx         sync.RWMutex
	events      chan NotifyEvent
}

func New(root string) (*Notify, error) {
	fi, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, errors.New("input must be a directory")
	}
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	n := &Notify{
		rootDir:     root,
		fd:          fd,
		inotifyFile: os.NewFile(uintptr(fd), ""),
		mtx:         sync.RWMutex{},
		events:      make(chan NotifyEvent, EventChanSize),
		paths:       map[int]string{},
		watches:     map[string]int{},
		namespaces:  map[string]struct{}{},
	}
	return n, n.WatchDir(root)
}

func (n *Notify) Start() {
	n.WatchExistingLogs()
	glog.V(0).Infoln("started....")
	glog.Flush()
	// Suggest to keep num of loops to 1, else events may be handled out of order
	MaxEventLoops := 1
	wg := sync.WaitGroup{}
	wg.Add(1)
	go n.ReadLoop(&wg)
	// TODO: remove the looping, need only one EventLoop goroutine
	for i := 0; i < MaxEventLoops; i++ {
		wg.Add(1)
		go n.EventLoop(&wg, i)
	}
	wg.Wait()
}

// ReadLoop reads the inotify fd and generates events
func (n *Notify) ReadLoop(wg *sync.WaitGroup) {
	var (
		buf [unix.SizeofInotifyEvent * EventChanSize]byte // Buffer for a maximum of 4096 raw events
	)
	defer func() {
		close(n.events)
		n.mtx.Lock()
		for fd := range n.paths {
			unix.InotifyRmWatch(n.fd, uint32(fd))
		}
		n.mtx.Unlock()
		n.inotifyFile.Close()
	}()

	for {
		readbytes, err := n.inotifyFile.Read(buf[:])
		if err != nil {
			if errors.Unwrap(err) == io.EOF {
				glog.Errorf("Received EOF on inotify file descriptor")
			}
			glog.Errorf("Error in ReadLoop. breaking the loop. err: %v", err)
			break
		}
		if readbytes <= 0 {
			glog.Errorf("readbytes <= 0. breaking the loop. readbytes: %d", readbytes)
			break
		}
		events := 0
		consumed := 0
		var offset uint32 = 0
		for offset <= uint32(readbytes-unix.SizeofInotifyEvent) {
			raw := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
			consumed += unix.SizeofInotifyEvent
			path := string(buf[offset+unix.SizeofInotifyEvent : offset+unix.SizeofInotifyEvent+raw.Len])
			consumed += int(raw.Len)
			offset += unix.SizeofInotifyEvent + raw.Len
			/*
				if raw.Mask&unix.IN_IGNORED == unix.IN_IGNORED {
					continue
				}
			*/
			e := NotifyEvent{
				InotifyEvent: *raw,
				path:         strings.TrimRight(path, "\x00"),
			}
			n.events <- e
			events += 1
			atomic.AddUint64(&SentEvents, 1)
		}
		if readbytes-consumed != 0 {
			glog.V(0).Infof("Read %d bytes, %d events, consumed %d bytes, remaining %d bytes", readbytes, events, consumed, (readbytes - consumed))
		}
	}
	wg.Done()
	glog.V(0).Infoln("exiting ReadLoop")
}

func (n *Notify) EventLoop(wg *sync.WaitGroup, idx int) {
	var tickerCh <-chan time.Time
	// start ticker in first goroutine only
	if idx == 0 {
		tickerCh = time.NewTicker(time.Minute * 1).C
	}
	for {
		if glog.V(9) {
			glog.Info("--------- going to select ---------")
		}
		select {
		case e := <-n.events:
			//handled := false
			atomic.AddUint64(&HandledEvents, 1)
			if e.IsOverFlowErr() {
				glog.Exit("Overflow occured. Exiting program.")
			}
			n.mtx.RLock()
			watchedPath, ok := n.paths[int(e.Wd)]
			n.mtx.RUnlock()
			if !ok {
				glog.Errorf("A watch event received for an unknown watched path. Fd: %d, Event Mask: %x", e.Wd, e.Mask)
				continue
			}

			if glog.V(5) && e.Mask != 0x8000 {
				glog.Infof("event for wd: %x, watchedPath: %q, for path: %q, mask is: %x", e.Wd, watchedPath, e.path, e.Mask)
			}

			switch {
			case e.IsIgnored():
				if glog.V(9) {
					glog.Infof("Received an IN_IGNORED event. wd: %x, watchedPath: %q, path: %q, Mask: %x", e.Wd, watchedPath, e.path, e.Mask)
				}
			case e.IsCreate():
				if e.path != "" {
					if e.IsDir() {
						// a directory got created in a directory
						newdir := filepath.Join(watchedPath, e.path)
						if watchedPath == n.rootDir {
							// a new namespace_pod directory got created, add a watch for this directory
							glog.V(3).Infof("A new namespace_pod got created. namespace_pod: %q", newdir)
						} else {
							// a new container directory got created, add a watch for this directory
							glog.V(3).Infof("A new container got created. container: %q", newdir)
						}
						must(n.WatchDir(newdir))
					} else {
						// a logfile got created
						logfile := filepath.Join(watchedPath, e.path)
						// ignore files which are not log files
						if strings.HasSuffix(logfile, ".log") {
							glog.V(3).Infof("a new log file got created: %q\n", logfile)
							must(n.WatchLogFile(logfile))
							err := UpdateFileSize(logfile)
							if err != nil {
								glog.Errorf("could not stat file: %q", logfile)
							}
						} else {
							if glog.V(7) {
								glog.Infof("A file was created which is not a log file. path: %q", logfile)
							}
						}
					}
				} else {
					// there should'nt be anything here
					glog.Errorf("unrecognized IN_CREATE event. wd: %x, watchedPath: %q, path: %q, Mask: %x", e.Wd, watchedPath, e.path, e.Mask)
				}
			case e.IsDelete():
				if e.path != "" {
					if e.IsDir() {
						// a directory got created in a directory
						removeddir := filepath.Join(watchedPath, e.path)
						if watchedPath == n.rootDir {
							// a namespace_pod directory got deleted, remove watch for this directory
							glog.V(3).Infof("A namespace_pod got deleted. namespace_pod: %q", removeddir)
						} else {
							// a container directory got deleted, remove watch for this directory
							glog.V(3).Infof("A container got deleted. container: %q", removeddir)
						}
						must(n.RemoveWatchForPath(removeddir))
					} else {
						// a file got deleted in a directory
						logfile := filepath.Join(watchedPath, e.path)
						glog.V(3).Infof("A log file got deleted %q", logfile)
						// don't need to remove watch for the file because files are watched using IN_ONESHOT, and watch would have got removed when file was closed for writing.
						must(n.RemoveWatchForPath(logfile))
					}
				} else {
					// there should'nt be anything here because delete notification should come on parent directory
					glog.Errorf("unrecognized IN_DELETE event. wd: %x, watchedPath: %q, path: %q, Mask: %x", e.Wd, watchedPath, e.path, e.Mask)
				}
			case e.IsModify():
				if e.path != "" {
					// this should not occur as no IN_MODIFY watch is placed for any directory
					glog.Errorf("unrecognized IN_MODIFY event. wd: %x, watchedPath: %q, path: %q, Mask: %x", e.Wd, watchedPath, e.path, e.Mask)
				} else {
					// a log file got written
					if glog.V(9) {
						glog.Infof("logfile %q got written", watchedPath)
					}
					if err := UpdateFileSize(watchedPath); err != nil {
						glog.Errorf("Error in doing stat for file: %q, err: %v", watchedPath, err)
					}
					// add a new watch for the file
					must(n.WatchLogFile(watchedPath))
				}
			case e.IsCloseWrite():
				if e.path != "" {
					// this should not occur as no IN_CLOSE_WRITE watch is placed for any directory
					glog.Errorf("unrecognized IN_CLOSE_WRITE event. wd: %x, watchedPath: %q, path: %q, Mask: %x", e.Wd, watchedPath, e.path, e.Mask)
				} else {
					// a log file opened for writing got closed
					glog.V(3).Infof("logfile %q got closed for writing", watchedPath)
					// update file size last time
					if err := UpdateFileSize(watchedPath); err != nil {
						glog.Errorf("Error in doing stat for file: %q, err: %v", watchedPath, err)
					}
					// add a new watch for the file because we want an event when it is written to again.
					// If we avoid adding a watch here, we would need to add a IN_OPEN for its parent directory which leads to a large number of unwanted events
					must(n.WatchLogFile(watchedPath))
				}
			default:
				glog.Errorf("unhandled event. wd: %x, watchedPath: %q, path: %q, Mask: %x", e.Wd, watchedPath, e.path, e.Mask)
			}

		case <-tickerCh:
			/**/
			sizemtx.Lock()
			glog.V(0).Infof("sizes(%d): %s\nEventsSent: %d, EventsHandled: %d\n", len(Sizes), func(m map[string]int64) string {
				p, err := json.MarshalIndent(m, "", "  ")
				if err != nil {
					return fmt.Sprintf("%v", err)
				}
				return string(p)
			}(Sizes), SentEvents, HandledEvents)
			sizemtx.Unlock()
			/**/
			/**/
			n.mtx.RLock()
			wl := n.WatchList()
			n.mtx.RUnlock()

			l := strings.Join(wl, ",\n")
			glog.V(0).Infof("Watching paths: \n%s\nTotal watches: %d\n", l, len(wl))
			/**/
		}
	}
	wg.Done()
	glog.V(3).Info("exiting Handleloop")
}

func (n *Notify) WatchExistingLogs() {
	filepath.WalkDir(n.rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == n.rootDir {
			return nil
		}
		// do not watch self dir else results in a positive feedback loop
		if d.Name() == SelfDir && d.IsDir() {
			glog.V(0).Infof("skipping reading self dir: %s", SelfDir)
			return filepath.SkipDir
		}
		glog.V(3).Infof("checking dir: %s", path)

		if d.IsDir() {
			glog.V(0).Infof("watching directory %q", path)
			return n.WatchDir(path)
		} else {
			err2 := n.WatchLogFile(path)
			if err2 != nil {
				return err2
			}
			err2 = UpdateFileSize(path)
			//	if err2 != nil {
			//		n.RemoveWatch(0, path)
			//	}
			return err2
		}
		return nil
	})
}

func UpdateFileSize(path string) error {
	s, err := os.Stat(path)
	if err != nil {
		glog.Error("could not stat file: ", path, err)
		return err
	}
	sizemtx.Lock()
	Sizes[path] = s.Size()
	sizemtx.Unlock()
	return nil
}
func must(err error) {
	if err != nil {
		glog.Exit("Exiting with error: ", err)
	}
}
