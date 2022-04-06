package main

import (
	"flag"
	"net/http"
	"os"

	"github.com/ViaQ/logerr/log"
	"github.com/log-file-metric-exporter/pkg/logwatch"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	logDir = "/var/log/pods"
)

func main() {
	var (
		dir       string
		addr      string
		crtFile   string
		keyFile   string
		verbosity int
	)
	flag.StringVar(&dir, "dir", logDir, "Directory containing log files")
	flag.IntVar(&verbosity, "verbose", 0, "set verbosity level")
	flag.StringVar(&addr, "http", ":2112", "HTTP service address where metrics are exposed")
	flag.StringVar(&crtFile, "crtFile", "/etc/fluent/metrics/tls.crt", "cert file for log-file-metric-exporter service")
	flag.StringVar(&keyFile, "keyFile", "/etc/fluent/metrics/tls.key", "key file for log-file-metric-exporter service")
	flag.Parse()

	log.SetLogLevel(verbosity)
	log.Info("start log metric exporter", "path", dir)

	w, err := logwatch.New(dir)
	if err != nil {
		log.Error(err, "watch error", "path", dir)
		os.Exit(1)
	}

	go func() {
		if err := w.Watch(); err != nil {
			log.Error(err, "error in watch", "path", dir)
			os.Exit(1)
		}
	}()

	http.Handle("/metrics", promhttp.Handler())
	if err := http.ListenAndServeTLS(addr, crtFile, keyFile, nil); err != nil {
		log.Error(err, "error in HTTP listen", "addr", addr)
		os.Exit(1)
	}
}
