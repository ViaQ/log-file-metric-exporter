package inotify

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime/debug"

	"github.com/golang/glog"
	"golang.org/x/sys/unix"
)

func (n *Notify) WatchList() []string {
	n.mtx.RLock()
	defer n.mtx.RUnlock()

	entries := make([]string, 0, len(n.watches))
	for pathname, fd := range n.watches {
		entries = append(entries, fmt.Sprintf("%6x, %q", fd, pathname))
	}

	return entries
}

func (n *Notify) WatchDir(dir string) error {
	n.mtx.Lock()
	defer n.mtx.Unlock()
	return n.watchPathWith(dir, FlagsWatchDir)
}

func (n *Notify) WatchLogFile(path string) error {
	n.mtx.Lock()
	defer n.mtx.Unlock()
	return n.watchPathWith(path, FlagsWatchFile)
}

func (n *Notify) watchPathWith(path string, flags uint32) error {
	// n.mtx is already held
	oldfd := n.watches[path]
	path = filepath.Clean(path)
	wfd, err := unix.InotifyAddWatch(n.fd, path, flags)
	if wfd == -1 {
		glog.Errorf("error in watch for path: %q, err: %v\n", path, err)
		debug.PrintStack()
		return err
	}

	count := 0
	for wfd == oldfd {
		//glog.Errorf("count: %3d inotify returned the same fd as before. So adding watch again. oldfd: %x, newfd: %x", count, oldfd, wfd)
		wfd, err = unix.InotifyAddWatch(n.fd, path, flags)
		if wfd == -1 {
			glog.Errorf("error in watch for path: %q, err: %v\n", path, err)
			debug.PrintStack()
			return err
		}
		count += 1
		if count == 1000 {
			return errors.New("----------- inotify has gone mad -------------------")
		}
	}
	if count != 0 && glog.V(3) {
		glog.Infof("After %3d reties: added watch for path: %q, wd: %x", count, path, wfd)
	}
	n.watches[path] = wfd
	n.paths[wfd] = path
	return nil
}

func (n *Notify) RemoveWatch(wfd int, path string) error {
	n.mtx.Lock()
	defer n.mtx.Unlock()
	return n.removeWatch(wfd, path)
}

func (n *Notify) RemoveWatchForPath(path string) error {
	n.mtx.Lock()
	defer n.mtx.Unlock()
	wd, ok := n.watches[path]
	if !ok {
		glog.Errorf("Could not find watch descriptor for path %q", path)
		return nil
	}
	return n.removeWatch(wd, path)
}

func (n *Notify) removeWatch(wfd int, path string) error {
	/*
		ret, err := unix.InotifyRmWatch(n.fd, uint32(wfd))
		if ret == -1 {
			glog.V(0).Infof("error in watch for path: %q, err: %v\n", path, err)
			debug.PrintStack()
			return err
		}
	*/
	if glog.V(5) {
		glog.Infof("removed watch for path: %q\n", path)
	}
	delete(n.watches, path)
	delete(n.paths, wfd)
	/*
		entries := make([]string, 0, len(n.watches))
		for pathname := range n.watches {
			entries = append(entries, pathname)
		}
		l := strings.Join(entries, ",\n")
		glog.V(3).Infof("Watching \n%s\n", l)
	*/
	return nil
}
