package inotify

import (
	"path/filepath"
	"syscall"

	log "github.com/ViaQ/logerr/v2/log/static"
	"golang.org/x/sys/unix"
)

// WatchDir adds a directory to the watch list with directory-specific flags.
func (n *Notify) WatchDir(dir string) error {
	n.mtx.Lock()
	defer n.mtx.Unlock()
	return n.watchPath(dir, FlagsWatchDir)
}

// WatchLogFile adds a file to the watch list with file-specific flags (ONESHOT).
func (n *Notify) WatchLogFile(path string) error {
	n.mtx.Lock()
	defer n.mtx.Unlock()
	return n.watchPath(path, FlagsWatchFile)
}

// watchPath is the internal method to add a path to the watch list.
// The mutex must already be held by the caller.
func (n *Notify) watchPath(path string, flags uint32) error {
	// Clean the path
	path = filepath.Clean(path)

	// Re-arming a ONESHOT watch: remove the old watch first, even though
	// ONESHOT means the kernel has almost always removed it for us already.
	//
	// On the event path the kernel queues the event and wakes readers before it
	// runs fsnotify_destroy_mark(), which needs group->mark_mutex. A reader that
	// re-arms inside that window finds the old mark still ATTACHED, so
	// inotify_add_watch() hands back the SAME watch descriptor, and the pending
	// destroy then tears down the freshly re-armed watch. The file goes silent
	// permanently: no error, and the watch still appears in the watch list.
	// Contention on mark_mutex — which watching many files produces constantly —
	// widens that window well beyond the few nanoseconds it would otherwise be.
	//
	// inotify_rm_watch() runs the destroy itself while holding the mutex, so the
	// add that follows cannot find an ATTACHED mark and the kernel must allocate
	// a fresh descriptor. EINVAL is the common case rather than an error: it
	// means the kernel had already detached the mark during event delivery.
	//
	// The race is present in every kernel from at least v4.14 to current master.
	if oldWd, exists := n.watches[path]; exists && flags&unix.IN_ONESHOT != 0 {
		if _, err := unix.InotifyRmWatch(n.fd, uint32(oldWd)); err != nil && err != syscall.EINVAL {
			n.rmWatchFailures.Add(1)
			log.V(2).Info("error removing old watch before re-arm", "path", path, "wd", oldWd, "err", err)
		}
		delete(n.paths, oldWd)
		delete(n.watches, path)
	}

	// Add the watch using inotify
	wd, err := unix.InotifyAddWatch(n.fd, path, flags)
	if err != nil {
		log.Error(err, "error adding watch", "path", path, "flags", flags)
		return err
	}

	// Check if this watch descriptor was previously assigned to a different path.
	// This can happen because the kernel reuses watch descriptors after ONESHOT watches
	// are automatically removed. Clean up the stale entry if needed.
	if oldPath, exists := n.paths[wd]; exists && oldPath != path {
		log.V(2).Info("watch descriptor reused", "oldPath", oldPath, "newPath", path, "wd", wd)
		delete(n.watches, oldPath)
	}

	// Store the mapping
	n.watches[path] = wd
	n.paths[wd] = path

	log.V(2).Info("watch added", "path", path, "wd", wd, "flags", flags)
	return nil
}

// RemoveWatch removes a path from the watch list.
func (n *Notify) RemoveWatch(path string) error {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	path = filepath.Clean(path)

	wd, exists := n.watches[path]
	if !exists {
		log.V(2).Info("watch not found", "path", path)
		return nil
	}

	// Try to remove the watch. Ignore EINVAL since ONESHOT watches may already be removed by the kernel.
	if _, err := unix.InotifyRmWatch(n.fd, uint32(wd)); err != nil {
		if err != syscall.EINVAL {
			log.Error(err, "error removing watch", "path", path, "wd", wd)
			return err
		}
		log.V(2).Info("watch already removed by kernel", "path", path, "wd", wd)
	}

	delete(n.watches, path)
	delete(n.paths, wd)

	log.V(2).Info("watch removed", "path", path, "wd", wd)
	return nil
}

// WatchList returns a copy of all currently watched paths.
func (n *Notify) WatchList() []string {
	n.mtx.RLock()
	defer n.mtx.RUnlock()

	paths := make([]string, 0, len(n.watches))
	for path := range n.watches {
		paths = append(paths, path)
	}
	return paths
}
