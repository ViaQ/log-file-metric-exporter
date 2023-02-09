package metrics

import (
	"errors"
	"regexp"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	totalLoggedBytes *prometheus.CounterVec
	newMetric        *prometheus.GaugeVec
}

var logFileRegex = regexp.MustCompile(`/var/log/pods/(?P<namespace>[a-z0-9-]+)_(?P<pod>[a-z0-9-]+)_(?P<uuid>[a-z0-9-]{32})/(?P<container>[a-z0-9-]+)/(?P<restart>[0-9]*).log(\.{0,1}(?P<ts>[\d]{8}-[\d]{6})){0,1}(\.gz){0,1}`)

func New() *Metrics {
	return &Metrics{
		// ===================
		// Proposed New Metric
		// ===================
		// This is a Gauge because we don't get the incremental value in log size, we get the actual value.
		// To make it a Counter we will need to save current size in memory, and update the counter using the diff.
		// We could avoid this state in memory by having the actual size of the file as a metric labelled as below.
		// And use a separate query to get the total number of bytes written by the container.
		//
		// Example:
		// A container got restarted twice, and in third iteration the log file got rotated thrice. The metrics will be as follows:
		//
		// {"namespace": "N", "podname": "P", "poduuid":"U", "containername":"C", "restartCount":0,"timestamp":""} 2770
		// {"namespace": "N", "podname": "P", "poduuid":"U", "containername":"C", "restartCount":1,"timestamp":""} 8768
		// {"namespace": "N", "podname": "P", "poduuid":"U", "containername":"C", "restartCount":2,"timestamp":"20230105-114647"} 98098098
		// {"namespace": "N", "podname": "P", "poduuid":"U", "containername":"C", "restartCount":2,"timestamp":"20230105-114647"} 6876876
		// {"namespace": "N", "podname": "P", "poduuid":"U", "containername":"C", "restartCount":2,"timestamp":"20230105-114647"} 9879987
		// {"namespace": "N", "podname": "P", "poduuid":"U", "containername":"C", "restartCount":2,"timestamp":""}         98769
		//
		// The total size of logged bytes in current generation(restartCount) would be sum of last 4 metrics
		//
		// When log gets written, we know above values by parsing the filename. The metrics which will be updated for timestamp="". When
		// logfile gets rotated, we receive a notification from inotify (not tested or coded yet). on this notification, create a new metric
		// with timestamp from filename, and value as the current value of metric. Restart the same metric from 0.
		newMetric: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "log_logged_bytes_total",
			Help: "Total number of bytes written to a single log file path, accounting for rotations for a ",
		}, []string{"namespace", "podname", "poduuid", "containername", "restartCount", "timestamp"}),

		// Old (existing) metric
		totalLoggedBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "log_logged_bytes_total",
			Help: "Total number of bytes written to a single log file path, accounting for rotations",
		}, []string{"namespace", "podname", "poduuid", "containername"}),
	}
}

type ParsedLogFile struct {
	Namespace    string
	Pod          string
	UUID         string
	Container    string
	RestartCount int
	Timespamp    string
	IsArchived   bool
}

func (m *Metrics) parse(logFilePath string) (ParsedLogFile, error) {
	matches := logFileRegex.FindStringSubmatch(logFilePath)
	if len(matches) != 9 {
		return ParsedLogFile{}, errors.New("failed to parse log file path")
	}
	restartCount, err := strconv.Atoi(matches[5])
	if err != nil {
		return ParsedLogFile{}, errors.New("failed to parse log file path")
	}
	return ParsedLogFile{
		Namespace:    matches[1],
		Pod:          matches[2],
		UUID:         matches[3],
		Container:    matches[4],
		RestartCount: restartCount,
		Timespamp:    matches[7], // matches[6] corresponds to .<timestamp>
		IsArchived:   (matches[8] == ".gz"),
	}, nil
}

func (m *Metrics) UpdateMetric(filepath string) error {
	var (
		p   ParsedLogFile
		err error
	)
	if p, err = m.parse(filepath); err != nil {
		return err
	}
	prometheus.MustNewConstMetric(nil, prometheus.GaugeValue, float64(12), "")
	_ = p
	return nil
}
