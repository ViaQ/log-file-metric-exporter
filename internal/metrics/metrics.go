package metrics

import (
	"errors"
	"regexp"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	totalLoggedBytes *prometheus.CounterVec
	loggedBytes      *prometheus.GaugeVec
}

var logFileRegex = regexp.MustCompile(`/var/log/pods/(?P<namespace>[a-z0-9-]+)_(?P<pod>[a-z0-9-]+)_(?P<uuid>[a-z0-9-]{32})/(?P<container>[a-z0-9-]+)/(?P<restart>[0-9]*).log(\.{0,1}(?P<ts>[\d]{8}-[\d]{6})){0,1}(\.gz){0,1}`)

func New() *Metrics {
	return &Metrics{
		loggedBytes: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "log_logged_bytes_total",
			Help: "Total number of bytes written to a single log file path, accounting for rotations",
		}, []string{"namespace", "podname", "poduuid", "containername"}),
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
