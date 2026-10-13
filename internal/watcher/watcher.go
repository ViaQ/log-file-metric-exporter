package watcher

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/ViaQ/logerr/v2/log/static"
	"github.com/log-file-metric-exporter/internal/inotify"
	"github.com/log-file-metric-exporter/internal/metrics"
)

type Watcher struct {
	notify            *inotify.Notify
	metrics           *metrics.Metrics
	rootDir           string
	reconcileInterval time.Duration
}

func New(rootDir string, m *metrics.Metrics, reconcileInterval time.Duration) (*Watcher, error) {
	n, err := inotify.New(rootDir)
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		notify:            n,
		metrics:           m,
		rootDir:           rootDir,
		reconcileInterval: reconcileInterval,
	}

	w.reconcile()
	return w, nil
}

// reconcile walks the whole log tree and brings the watcher back into agreement
// with what is on disk: it re-arms a watch on every directory and log file
// (idempotent, so already-watched paths are simply re-armed), restats each file
// so any growth missed while unwatched is added to the counter, and forgets
// series whose files have gone away. It is the initial scan, the recovery path
// after an inotify overflow, and the periodic safety-net sweep. Because it calls
// metrics.Update, it must run on the single goroutine that drives Start.
func (w *Watcher) reconcile() {
	active := make([]string, 0)
	filepath.WalkDir(w.rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil 
			}
			return err
		}
		if path == w.rootDir {
			return nil
		}
		if d.IsDir() {
			log.V(3).Info("watching directory", "path", path)
			return w.notify.WatchDir(path)
		}
		if strings.HasSuffix(path, ".log") {
			if err := w.notify.WatchLogFile(path); err != nil {
				return err
			}
			w.updateFileSize(path)
			active = append(active, path)
		}
		return nil
	})
	w.metrics.Retain(active)
}

func (w *Watcher) Start(ctx context.Context) error {
	go w.notify.ReadLoop()

	diag := time.NewTicker(60 * time.Second)
	defer diag.Stop()

	// A reconcile interval of 0 disables the periodic safety-net sweep; overflow
	// recovery still reconciles on demand. A stopped ticker never fires, so the
	// select arm below is simply never selected.
	reconcile := time.NewTicker(time.Hour)
	reconcile.Stop()
	if w.reconcileInterval > 0 {
		reconcile.Reset(w.reconcileInterval)
	}
	defer reconcile.Stop()

	events := w.notify.Events()
	for {
		select {
		case <-ctx.Done():
			w.notify.Close()
			return ctx.Err()

		case e, ok := <-events:
			if !ok {
				return nil
			}

			if e.IsOverFlowErr() {
				// The kernel dropped an unknown set of events, so re-walk the
				// tree to re-arm watches, add any growth missed while a file was
				// unwatched, and forget series for files that have since gone.
				log.Error(nil, "inotify queue overflow, reconciling to recover")
				w.reconcile()
				continue
			}

			if e.IsIgnored() {
				continue
			}

			switch {
			case e.IsCreate():
				w.handleCreate(e)
			case e.IsDelete():
				w.handleDelete(e)
			case e.IsModify():
				w.handleModify(e)
			case e.IsCloseWrite():
				w.handleCloseWrite(e)
			}

		case <-reconcile.C:
			log.V(2).Info("periodic reconcile")
			w.reconcile()

		case <-diag.C:
			watchList := w.notify.WatchList()
			log.V(1).Info("diagnostic", "watchCount", len(watchList))
		}
	}
}

func (w *Watcher) handleCreate(e inotify.NotifyEvent) {
	if e.IsDir() {
		log.V(3).Info("new directory", "path", e.Path)
		if err := w.notify.WatchDir(e.Path); err != nil {
			log.Error(err, "failed to watch directory", "path", e.Path)
		}
	} else if strings.HasSuffix(e.Path, ".log") {
		log.V(3).Info("new log file", "path", e.Path)
		if err := w.notify.WatchLogFile(e.Path); err != nil {
			log.Error(err, "failed to watch log file", "path", e.Path)
		}
		w.updateFileSize(e.Path)
	}
}

func (w *Watcher) handleDelete(e inotify.NotifyEvent) {
	log.V(3).Info("deleted", "path", e.Path)
	if err := w.notify.RemoveWatch(e.Path); err != nil {
		log.Error(err, "failed to remove watch", "path", e.Path)
	}
	w.metrics.Forget(e.Path)
}

func (w *Watcher) handleModify(e inotify.NotifyEvent) {
	w.updateFileSize(e.Path)
	if err := w.notify.WatchLogFile(e.Path); err != nil {
		log.Error(err, "failed to re-add watch after modify", "path", e.Path)
	}
}

func (w *Watcher) handleCloseWrite(e inotify.NotifyEvent) {
	w.updateFileSize(e.Path)
	if err := w.notify.WatchLogFile(e.Path); err != nil {
		log.Error(err, "failed to re-add watch after close_write", "path", e.Path)
	}
}

func (w *Watcher) updateFileSize(path string) {
	info, err := os.Stat(path)
	if err != nil {
		log.V(3).Info("could not stat file", "path", path, "error", err)
		return
	}
	if err := w.metrics.Update(path, info.Size()); err != nil {
		log.V(5).Info("could not update metric", "path", path, "error", err)
	}
}

func (w *Watcher) Close() {
	w.notify.Close()
}
