// Package logwatch watches Pod log files and updates metrics.
package logwatch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	log "github.com/ViaQ/logerr/v2/log/static"
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
	watcher           *symnotify.Watcher
	metrics           *prometheus.CounterVec
	sizes             map[LogLabels]float64
	mutex             sync.RWMutex
	dir               string
	reconcileInterval time.Duration
	reconcileNow      chan struct{}
	done              chan struct{}
	closeOnce         sync.Once
}

func New(dir string, reconcileInterval time.Duration) (*Watcher, error) {
	log.V(3).Info("Initializing a new watcher...")
	//Get new watcher
	watcher, err := symnotify.NewWatcher(dir)
	if err != nil {
		return nil, fmt.Errorf("error creating watcher: %w", err)
	}
	w := &Watcher{
		watcher: watcher,
		metrics: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "log_logged_bytes_total",
			Help: "Total number of bytes written to a single log file path, accounting for rotations",
		}, []string{"namespace", "podname", "poduuid", "containername"}),
		sizes:             make(map[LogLabels]float64),
		mutex:             sync.RWMutex{},
		dir:               dir,
		reconcileInterval: reconcileInterval,
		reconcileNow:      make(chan struct{}, 1),
		done:              make(chan struct{}),
	}

	log.V(3).Info("Registering counter", "metrics", w.metrics)
	if err := prometheus.Register(w.metrics); err != nil {
		return nil, fmt.Errorf("error registering metrics: %w", err)
	}
	log.V(3).Info("Walking watch dir", "dir", dir)
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error { return w.Update(path) })
	if err != nil {
		return nil, err
	}
	err = w.watcher.Add(dir)
	if err != nil {
		return nil, fmt.Errorf("error watching directory %v: %w", dir, err)
	}
	go w.reconcileLoop()
	return w, nil
}

func (w *Watcher) Close() {
	w.closeOnce.Do(func() {
		close(w.done)
		w.watcher.Close()
		prometheus.Unregister(w.metrics)
	})
}

func (w *Watcher) Forget(path string) {
	log.V(3).Info("Watcher#Forget", "path", path)
	var l LogLabels
	if l.Parse(path) {
		defer w.mutex.Unlock()
		w.mutex.Lock()
		delete(w.sizes, l) // Clean up sizes entry
		_ = w.metrics.DeleteLabelValues(l.Namespace, l.Name, l.UUID, l.Container)
	}
}

// triggerReconcile requests an immediate reconcile without blocking. Requests
// coalesce: a full buffer already means a reconcile is pending.
func (w *Watcher) triggerReconcile() {
	select {
	case w.reconcileNow <- struct{}{}:
	default:
	}
}

// reconcile performs a full resync against disk, then prunes stale in-memory tuples.
func (w *Watcher) reconcile() {
	log.V(3).Info("Watcher#reconcile: starting disk reconcile", "dir", w.dir)
	live := w.resync()
	w.prune(live)
}

// reconcileLoop runs reconcile on the configured interval and on demand until
// the watcher is closed. A non-positive interval disables the timer; the
// on-demand trigger still works.
func (w *Watcher) reconcileLoop() {
	var tick <-chan time.Time
	if w.reconcileInterval > 0 {
		ticker := time.NewTicker(w.reconcileInterval)
		defer ticker.Stop()
		tick = ticker.C
	}
	for {
		select {
		case <-w.done:
			return
		case <-tick:
			w.reconcile()
		case <-w.reconcileNow:
			w.reconcile()
		}
	}
}

// resync re-walks the watched directory, refreshing metrics via Update and
// re-establishing watches, and returns the set of tuples present on disk.
func (w *Watcher) resync() map[LogLabels]struct{} {
	live := map[LogLabels]struct{}{}
	err := filepath.Walk(w.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // tolerate transient walk errors; prune re-checks existence
		}
		if info.IsDir() {
			return nil
		}
		var l LogLabels
		if l.Parse(path) {
			live[l] = struct{}{}
		}
		_ = w.Update(path)
		return nil
	})
	if err != nil {
		log.Error(err, "Watcher#resync: walk error", "dir", w.dir)
	}
	// Re-establish any watches missed during an overflow (idempotent).
	if err := w.watcher.Add(w.dir); err != nil {
		log.Error(err, "Watcher#resync: error re-adding watch", "dir", w.dir)
	}
	return live
}

// prune deletes in-memory tuples absent from live. Each candidate is
// re-checked against disk under the lock to avoid pruning a pod that appeared
// after the walk started.
func (w *Watcher) prune(live map[LogLabels]struct{}) {
	w.mutex.Lock()
	var stale []LogLabels
	for l := range w.sizes {
		if _, ok := live[l]; !ok {
			stale = append(stale, l)
		}
	}
	w.mutex.Unlock()

	for _, l := range stale {
		if w.podDirExists(l) {
			continue // reappeared / still on disk: keep it
		}
		w.mutex.Lock()
		delete(w.sizes, l)
		_ = w.metrics.DeleteLabelValues(l.Namespace, l.Name, l.UUID, l.Container)
		w.mutex.Unlock()
		log.V(3).Info("Watcher#prune: removed stale tuple", "labels", l)
	}
}

// podDirExists reports whether the container log directory for l still exists.
func (w *Watcher) podDirExists(l LogLabels) bool {
	containerDir := filepath.Join(w.dir,
		fmt.Sprintf("%s_%s_%s", l.Namespace, l.Name, l.UUID), l.Container)
	_, err := os.Stat(containerDir)
	return err == nil
}

func (w *Watcher) Watch() error {
	for {
		select {
		case <-w.done:
			return nil
		default:
		}
		max := 5
		wg := sync.WaitGroup{}
		wg.Add(max)
		for i := 1; i <= max; i++ {
			go w.processNextEvent(&wg)
		}
		wg.Wait()
	}
}
func (w *Watcher) processNextEvent(wg *sync.WaitGroup) {
	defer wg.Done()
	e, err := w.watcher.Event()
	log.V(3).Info("logwatch.Watcher#Watch", "path", e.Name, "event", e.Op.String())
	switch {
	case err == io.EOF:
		return
	case err != nil:
		log.Error(err, "Error retrieving watch event")
		if errors.Is(err, fsnotify.ErrEventOverflow) {
			log.Info("inotify event overflow; triggering full reconcile")
			w.triggerReconcile()
		}
	case e.Op == fsnotify.Remove:
		w.Forget(e.Name)
	default:
		if err = w.Update(e.Name); err != nil {
			log.V(4).Error(err, "Error during Watcher#Update", "path", e.Name, "event", e.Op.String())
		}
	}
}

func (w *Watcher) Update(path string) (err error) {
	log.V(3).Info("Watcher#Update", "path", path)
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
		log.V(3).Info("Unable to parse path for LogLabels. returning early from update", "path", path)
		return nil
	}
	if !w.watcher.Within(path) {
		log.V(2).Info("refusing to stat symlink target outside root", "path", path)
		return nil
	}
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}
	if stat.IsDir() {
		log.V(3).Info("Ignoring path given it is a directory", "path", path)
		return nil // Ignore directories
	}
	counter, err := w.metrics.GetMetricWithLabelValues(l.Namespace, l.Name, l.UUID, l.Container)
	if err != nil {
		return err
	}
	defer w.mutex.Unlock()
	w.mutex.Lock()
	lastSize, size := w.sizes[l], float64(stat.Size())
	log.V(3).Info("Stats", "path", path, "lastSize", lastSize, "size", size)
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
