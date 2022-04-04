// package symnotify provides a file system watcher that notifies events for symlink targets.
//
package symnotify

import (
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/ViaQ/logerr/log"
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
}

func NewWatcher() (*Watcher, error) {
	w, err := fsnotify.NewWatcher()
	return &Watcher{watcher: w}, err
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
		if info, err := os.Lstat(e.Name); err == nil {
			if isSymlink(info) || info.IsDir() {
				_ = w.Add(e.Name)
			}
		}
	case e.Op == Remove:
		_ = w.watcher.Remove(e.Name)
	case e.Op == Chmod || e.Op == Rename:
		if info, err := os.Lstat(e.Name); err == nil {
			if isSymlink(info) {
				// Symlink target may have changed.
				_ = w.watcher.Remove(e.Name)
				_ = w.watcher.Add(e.Name)
			}
		}
	}
	return e, nil
}

// Add a new directory, file or symlink to be watched.
func (w *Watcher) Add(name string) (err error) {
	log.V(3).Info("start watching", "path", name)
	if err := w.watcher.Add(name); err != nil {
		log.Error(err, "error watching", "path", name)
		return err
	}
	// If name is a directory, scan for existing symlinks and sub-directories to watch.
	if infos, err := ioutil.ReadDir(name); err == nil {
		for _, info := range infos {
			newName := filepath.Join(name, info.Name())
			switch {
			case info.IsDir():
				return w.Add(newName)
			case isSymlink(info):
				return w.watcher.Add(newName)
			}
		}
	}
	return nil
}

// Remove name from watcher
func (w *Watcher) Remove(name string) error {
	log.V(3).Info("stop watching", "path", name)
	return w.watcher.Remove(name)
}

// Close watcher
func (w *Watcher) Close() error { return w.watcher.Close() }

func isSymlink(info os.FileInfo) bool {
	return (info.Mode() & os.ModeSymlink) == os.ModeSymlink
}
