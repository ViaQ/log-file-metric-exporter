# log-file-metric-exporter

Exporter to collect metrics about container logs being produced in a kubernetes environment
It publishes log_logged_bytes_total metric in prometheus. This metric allows one to see total data bytes actually logged vs. what collector (fluentd) is able to collect during runtime.
This implementation is based on Golang and it uses fsnotify package to watch out for new data written to log files residing in the Watcher path.
