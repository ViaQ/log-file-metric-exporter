package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// labels identifies a container's log stream. The rotation suffix is
// deliberately excluded so bytes keep accumulating onto one series as the log
// rotates from 0.log to 1.log and so on.
type labels struct {
	namespace, pod, uuid, container string
}

type Metrics struct {
	logBytes *prometheus.CounterVec

	mtx   sync.Mutex
	sizes map[labels]float64
}

func New() *Metrics {
	return &Metrics{
		logBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "log_logged_bytes_total",
			Help: "Total number of bytes written to a single log file path, accounting for rotations",
		}, []string{"namespace", "podname", "poduuid", "containername"}),
		sizes: make(map[labels]float64),
	}
}

func (m *Metrics) Register(reg prometheus.Registerer) error {
	return reg.Register(m.logBytes)
}

// Update records the current on-disk size of a log file, adding the growth
// since the last observation to the counter.
//
// Sizes for a given path must arrive in the order they were observed. A size
// smaller than the last one is taken as the file having restarted and is added
// in full, so a stale reading arriving late would be counted a second time.
// The watcher handles events on a single goroutine, which is what keeps them
// ordered.
func (m *Metrics) Update(path string, size int64) error {
	parsed, err := ParseLogFilePath(path)
	if err != nil {
		return err
	}
	l := labels{parsed.Namespace, parsed.Pod, parsed.UUID, parsed.Container}

	counter, err := m.logBytes.GetMetricWithLabelValues(l.namespace, l.pod, l.uuid, l.container)
	if err != nil {
		return err
	}

	m.mtx.Lock()
	defer m.mtx.Unlock()

	lastSize, current := m.sizes[l], float64(size)
	m.sizes[l] = current

	add := current - lastSize
	if current < lastSize {
		// Truncated or rotated: the file started over, so all of it is new.
		add = current
	}
	counter.Add(add)
	return nil
}

func (m *Metrics) Forget(path string) {
	parsed, err := ParseLogFilePath(path)
	if err != nil {
		return
	}
	l := labels{parsed.Namespace, parsed.Pod, parsed.UUID, parsed.Container}

	m.mtx.Lock()
	defer m.mtx.Unlock()

	delete(m.sizes, l)
	m.logBytes.DeleteLabelValues(l.namespace, l.pod, l.uuid, l.container)
}
