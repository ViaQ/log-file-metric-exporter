### This is a generated file from Dockerfile.in ###
#@follow_tag(openshift-golang-builder:1.14)
FROM registry.ci.openshift.org/ocp/builder:rhel-8-golang-1.15-openshift-4.7 AS builder

ENV REMOTE_SOURCE=${REMOTE_SOURCE:-.}
ARG dir=/var/log/containers/
ARG verbosity=2
ARG http=:2112
WORKDIR  /go/src/github.com/log-file-metric-exporter
COPY ${REMOTE_SOURCE} .

RUN make build

#@follow_tag(openshift-ose-base:ubi8)
FROM registry.ci.openshift.org/ocp/4.7:base
COPY --from=builder /go/src/github.com/log-file-metric-exporter/bin/log-file-metric-exporter /usr/bin/
CMD /usr/bin/log-file-metric-exporter -dir=${dir} -verbosity=${verbosity} -http=${http}

LABEL \
        io.k8s.display-name="OpenShift log-file-metric-exporter" \
        io.k8s.description="OpenShift K8 log files logged_bytes_total metric exporter in  OpenShift Cluster Logging" \
        name="openshift/ose-log-file-metric-exporter" \
        com.redhat.component="ose-log-file-metric-exporter-container" \
        io.openshift.maintainer.product="OpenShift Container Platform" \
        io.openshift.maintainer.component="Logging" \
        io.openshift.build.commit.id=${CI_LOG_FILE_METRIC_EXPORTER_UPSTREAM_COMMIT} \
        io.openshift.build.source-location=${CI_LOG_FILE_METRIC_EXPORTER_UPSTREAM_URL} \
        io.openshift.build.commit.url=${CI_LOG_FILE_METRIC_EXPORTER_UPSTREAM_URL}/commit/${CI_LOG_FILE_METRIC_EXPORTER_UPSTREAM_COMMIT} \
        version=${CI_CONTAINER_VERSION}

