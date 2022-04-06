// Package logwatch watches Pod log files and updates metrics.
package logwatch

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/ViaQ/logerr/log"
	"github.com/fsnotify/fsnotify"
	"github.com/log-file-metric-exporter/pkg/symnotify"
	"github.com/prometheus/client_golang/prometheus"
)

var logFile = regexp.MustCompile(`/([a-z0-9-]+)_([a-z0-9-]+)_([a-f0-9-]+)/([a-z0-9-]+)/.*\.log`)

// LogLabels are the labels for a Pod log file.
//
// NOTE: The log Path is not a label because it includes a variable "n.log" part that changes
// over the life of the same container.
type LogLabels struct {
	Namespace, Name, UUID, Container string
}

func (l *LogLabels) Parse(path string) (ok bool) {
	match := logFile.FindStringSubmatch(path)
	if match != nil {
		l.Namespace, l.Name, l.UUID, l.Container = match[1], match[2], match[3], match[4]
		return true
	}
	return false
}

type Watcher struct {
	watcher *symnotify.Watcher
	metrics *prometheus.CounterVec
	sizes   map[LogLabels]float64
}

func New(dir string) (*Watcher, error) {
	//Get new watcher
	watcher, err := symnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("error creating watcher: %w", err)
	}
	w := &Watcher{
		watcher: watcher,
		metrics: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "log_logged_bytes_total",
			Help: "Total number of bytes written to a single log file path, accounting for rotations",
		}, []string{"namespace", "podname", "poduuid", "containername"}),
		sizes: make(map[LogLabels]float64),
	}
	if err := prometheus.Register(w.metrics); err != nil {
		return nil, fmt.Errorf("error registering metrics: %w", err)
	}
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error { return w.Update(path) })
	err = w.watcher.Add(dir)
	if err != nil {
		return nil, fmt.Errorf("error watching directory %v: %w", dir, err)
	}
	return w, nil
}

func (w *Watcher) Close() {
	w.watcher.Close()
	prometheus.Unregister(w.metrics)
}

func (w *Watcher) Update(path string) (err error) {
	defer func() {
		if os.IsNotExist(err) {
			w.Forget(path)
			err = nil // Not an error if a file disappears
		}
		if err != nil {
			log.Error(err, "error updating metric", "path", path)
		}
	}()

	var l LogLabels
	if !l.Parse(path) {
		return nil
	}
	counter, err := w.metrics.GetMetricWithLabelValues(l.Namespace, l.Name, l.UUID, l.Container)
	if err != nil {
		return err
	}
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}
	if stat.IsDir() {
		return nil // Ignore directories
	}
	lastSize, size := w.sizes[l], float64(stat.Size())
	w.sizes[l] = size
	var add float64
	if size > lastSize {
		// File has grown, add the difference to the counter.
		add = size - lastSize
	} else if size < lastSize {
		// File truncated, starting over. Add the size.
		add = size
	}
	log.V(3).Info("updated metric", "path", path, "lastsize", lastSize, "currentsize", size, "addedbytes", add)
	counter.Add(add)
	return nil
}

func (w *Watcher) Forget(path string) {
	var l LogLabels
	if l.Parse(path) {
		delete(w.sizes, l) // Clean up sizes entry
		_ = w.metrics.DeleteLabelValues(l.Namespace, l.Name, l.UUID, l.Container)
	}
}

func (w *Watcher) Watch() error {
	for {
		e, err := w.watcher.Event()
		switch {
		case err == io.EOF:
			return nil
		case err != nil:
			return err
		case e.Op == fsnotify.Remove:
			w.Forget(e.Name)
		default:
			w.Update(e.Name)
		}
	}
}
