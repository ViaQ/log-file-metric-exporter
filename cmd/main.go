package main

import (
	"flag"
	"net/http"
	"os"
	"regexp"

	"github.com/ViaQ/logerr/log"
	"github.com/fsnotify/fsnotify"
	"github.com/log-file-metric-exporter/pkg/symnotify"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	verbosity int = 0
	//Reference regexp https://github.com/fabric8io/fluent-plugin-kubernetes_metadata_filter/blob/master/lib/fluent/plugin/filter_kubernetes_metadata.rb#L56, https://github.com/kubernetes/kubernetes/blob/release-1.6/pkg/kubelet/dockertools/docker.go
	//compile k8 logfilepathname pattern
	kubernetesregexpCompiled = regexp.MustCompile(`.var.log.containers.(?P<pod>[^_]+)_(?P<ns>[a-z0-9][-a-z0-9]*[a-z0-9])_(?P<container>.+)-(?P<dockerid>[a-z0-9]{64})\.log`)
)

const (
	PodNameIndex = iota + 1
	NamespaceIndex
	ContainerNameIndex
	DockerIndex
	MatchLen
)

type FileWatcher struct {
	watcher *symnotify.Watcher
	metrics *prometheus.CounterVec
	sizes   map[string]float64
	added   map[string]bool
}

func (w *FileWatcher) Update(path string, namespace string, podname string, containername string) error {
	var add float64
	var lastSize float64
	var size float64

	counter, err := w.metrics.GetMetricWithLabelValues(namespace, podname, containername)
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
	lastSize, size = w.sizes[path], float64(stat.Size())
	w.sizes[path] = size
	if size > lastSize {
		// File has grown, add the difference to the counter.
		add = size - lastSize
	} else if size < lastSize {
		// File truncated, starting over. Add the size.
		add = size
	}
	log.V(3).Info("For logfile in...", "lastsize", lastSize, "currentsize", size, "addedbytes", add)
	counter.Add(add)
	return nil
}

func (w *FileWatcher) Watch() {

	for {
		//All logfiles with containername are added to the watcher
		//write event for these logfiles are being watched
		//create event gets issued for all new logfiles appear under logfilepathname /var/log/containers/
		//For the cases new log files added, old files moved, old files deleted, you need to add/remove them from watcher as whole dir added to the watcher
		//For new log files added write event is not getting issued

		e, err := w.watcher.Event()
		if err != nil {
			log.Error(err, "Watcher.Event returning err")
			os.Exit(1)
		}

		log.V(3).Info("Events notified for...", "e.Name", e.Name, "Event", e.Op)
		if e.Op == fsnotify.Remove {
			delete(w.sizes, e.Name)
		} else {

			//Get namespace, podname, containername from e.Name - log file path
			matches := kubernetesregexpCompiled.FindStringSubmatch(e.Name)

			if matches == nil {
				log.V(2).Info("filename doesn't conform with k8 logfile path name ...", "filename", e.Name)
			} else {
				podname := matches[PodNameIndex]
				namespace := matches[NamespaceIndex]
				containername := matches[ContainerNameIndex]
				dockerid := matches[DockerIndex]
				log.V(3).Info("Namespace podname containername...", "namespace", namespace, "podname", podname, "containername", containername, "dockerid", dockerid)

				err := w.Update(e.Name, namespace, podname, containername)
				if err != nil {
					log.V(2).Info("file e.Name Stat can't be checked", "filename", e.Name, "error", err)
				}
			}
		}
	}
}

func main() {
	var dir string
	var addr string
	var crtFile string
	var keyFile string

	//directory to be watched out where symlinks to all logs files are present e.g. /var/log/containers/
	//debug option true or false
	//listening port where this go-app push prometheus registered metrics for further collected or reading by end prometheus server
	flag.StringVar(&dir, "dir", "/var/log/containers/", "Directory containing log files")
	flag.IntVar(&verbosity, "verbosity", 0, "set verbosity level")
	flag.StringVar(&addr, "http", ":2112", "HTTP service address where metrics are exposed")
	flag.StringVar(&crtFile, "crtFile", "/etc/fluent/metrics/tls.crt", "cert file for log-file-metric-exporter service")
	flag.StringVar(&keyFile, "keyFile", "/etc/fluent/metrics/tls.key", "key file for log-file-metric-exporter service")
	flag.Parse()

	log.SetLogLevel(verbosity)

	log.V(2).Info("Watching out logfiles dir ...", "dir", dir, "http", addr)
	log.V(2).Info("Crt and Key taken from...", crtFile, keyFile)

	//Get new watcher
	symwatcher, err := symnotify.NewWatcher()
	if err != nil {
		log.Error(err, "NewFileWatcher error")
	}
	w := &FileWatcher{
		watcher: symwatcher,
		metrics: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "log_logged_bytes_total",
			Help: "Total number of bytes written to a single log file path, accounting for rotations",
		}, []string{"namespace", "podname", "containername"}),
		sizes: make(map[string]float64),
		added: make(map[string]bool),
	}

	errp := prometheus.Register(w.metrics)
	if err != nil {
		log.Error(errp, "Error in Prometheus.Register registering metrics")
	}
	defer prometheus.Unregister(w.metrics)

	defer w.watcher.Close()
	//Add dir to watcher
	erra := w.watcher.Add(dir)
	if erra != nil {
		log.Error(erra, "Error in Watcher.Add call in adding dir")
	}

	go w.Watch()
	http.Handle("/metrics", promhttp.Handler())
	errh := http.ListenAndServeTLS(addr, crtFile, keyFile, nil)
	if errh != nil {
		log.Error(errh, "Error in http.ListenAndServei call")
	}

}
