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
	notify  *inotify.Notify
	metrics *metrics.Metrics
	rootDir string
}

func New(rootDir string, m *metrics.Metrics) (*Watcher, error) {
	n, err := inotify.New(rootDir)
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		notify:  n,
		metrics: m,
		rootDir: rootDir,
	}

	w.watchExistingLogs()
	return w, nil
}

func (w *Watcher) watchExistingLogs() {
	filepath.WalkDir(w.rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
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
		}
		return nil
	})
}

func (w *Watcher) Start(ctx context.Context) error {
	go w.notify.ReadLoop()

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

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
				log.Error(nil, "inotify queue overflow, exiting to allow restart")
				os.Exit(1)
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

		case <-ticker.C:
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
