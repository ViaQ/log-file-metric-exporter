#! /bin/bash

/usr/local/bin/log-file-metric-exporter -verbosity=2 -dir=/var/log/containers -http=:2112
