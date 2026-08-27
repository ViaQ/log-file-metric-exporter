// package symnotify provides a file system watcher that notifies events for symlink targets.
package symnotify

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	log "github.com/ViaQ/logerr/v2/log/static"
	"github.com/fsnotify/fsnotify"
)

type Event = fsnotify.Event
type Op = fsnotify.Op

const (
	Create Op = fsnotify.Create
	Write     = fsnotify.Write
	Remove    = fsnotify.Remove
	Rename    = fsnotify.Rename
	Chmod     = fsnotify.Chmod
)

// Watcher is like fsnotify.Watcher but also notifies on changes to symlink targets
type Watcher struct {
	watcher *fsnotify.Watcher
	// root is the resolved confinement boundary; symlink targets outside it are refused.
	root string
}

func NewWatcher(root string) (*Watcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	// Resolve the root once so target comparisons are symlink-stable.
	resolved, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return nil, fmt.Errorf("error resolving watch root %q: %w", root, err)
	}
	return &Watcher{watcher: w, root: resolved}, nil
}

// Event returns the next event or an error.
func (w *Watcher) Event() (e Event, err error) {
	var ok bool
	select {
	case e, ok = <-w.watcher.Events:
	case err, ok = <-w.watcher.Errors:
	}
	if !ok {
		err = io.EOF
	}
	if err != nil {
		return Event{}, err
	}
	log.V(3).Info("event", "path", e.Name, "operation", e.Op.String())
	switch {
	case e.Op == Create:
		var info os.FileInfo
		if info, err = os.Lstat(e.Name); err == nil {
			if isSymlink(info) || info.IsDir() {
				err = w.Add(e.Name)
			}
		}
	case e.Op == Remove:
		err = w.watcher.Remove(e.Name)
	case e.Op == Chmod || e.Op == Rename:
		var info os.FileInfo
		if info, err = os.Lstat(e.Name); err == nil {
			if isSymlink(info) {
				// Symlink target may have changed; re-add only if it still resolves in-root.
				_ = w.watcher.Remove(e.Name)
				_, err = w.add(e.Name)
			}
		}
	}
	if err != nil {
		if !errors.Is(err, fsnotify.ErrNonExistentWatch) {
			log.Error(err, "Error retrieving event", "path", e.Name, "operation", e.Op.String())
		}
	}
	return e, nil
}

// Remove name from watcher
func (w *Watcher) Remove(name string) error {
	log.V(3).Info("stop watching", "path", name)
	return w.watcher.Remove(name)
}

// Add a new directory, file or symlink to be watched.
func (w *Watcher) Add(name string) (err error) {
	log.V(3).Info("start watching", "path", name)
	watched, err := w.add(name)
	if err != nil {
		return err
	}
	if !watched {
		return nil
	}
	// If name is a directory, scan for existing symlinks and sub-directories to watch.
	var infos []fs.FileInfo
	if infos, err = ioutil.ReadDir(name); err == nil {
		for _, info := range infos {
			log.V(3).Info("Checking path for more files to watch", "name", name, "subpath", info.Name())
			newName := filepath.Join(name, info.Name())
			switch {
			case info.IsDir():
				if e := w.Add(newName); e != nil {
					log.Error(e, "Error path to watch", "path", newName)
				}
			case isSymlink(info):
				if _, e := w.add(newName); e != nil {
					log.Error(e, "Error for symnotify#Add", "path", newName)
				}
			}
		}
	}
	return err
}

// Close watcher
func (w *Watcher) Close() error { return w.watcher.Close() }

func isSymlink(info os.FileInfo) bool {
	return (info.Mode() & os.ModeSymlink) == os.ModeSymlink
}

// Within reports whether path may be watched or stat-ed under the confinement root.
func (w *Watcher) Within(path string) bool { return w.withinRoot(path) }

// withinRoot resolves path and reports whether it stays under the root. Non-symlinks
// are trusted because we never watch out-of-root directories, so their parents are
// already in-root; only symlink leaves need full resolution. Fail-closed on error.
func (w *Watcher) withinRoot(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if !isSymlink(info) {
		return true
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	return real == w.root || strings.HasPrefix(real, w.root+string(os.PathSeparator))
}

// add installs a watch on name only if it stays within the confinement root.
// Returns watched=false (with nil error) when the path is refused.
func (w *Watcher) add(name string) (watched bool, err error) {
	if !w.withinRoot(name) {
		log.V(2).Info("refusing to watch path with target outside root", "path", name, "root", w.root)
		return false, nil
	}
	if err = w.watcher.Add(name); err != nil {
		log.Error(err, "error watching", "path", name)
		return false, err
	}
	return true, nil
}
