### This is a generated file from Dockerfile.in ###
#@follow_tag(registry-proxy.engineering.redhat.com/rh-osbs/openshift-golang-builder:rhel_8_golang_1.17)
FROM registry.access.redhat.com/ubi8/go-toolset AS builder

ENV BUILD_VERSION=1.1.0
ENV OS_GIT_MAJOR=1
ENV OS_GIT_MINOR=1
ENV OS_GIT_PATCH=0
ENV SOURCE_GIT_COMMIT=${CI_LOG_FILE_METRIC_EXPORTER_UPSTREAM_COMMIT}
ENV SOURCE_GIT_URL=${CI_LOG_FILE_METRIC_EXPORTER_UPSTREAM_URL}
ENV REMOTE_SOURCE=${REMOTE_SOURCE:-.}


USER 0
WORKDIR  /go/src/github.com/log-file-metric-exporter
COPY ${REMOTE_SOURCE} .

RUN make build

#@follow_tag(registry.redhat.io/ubi8:latest)
FROM registry.access.redhat.com/ubi8
COPY --from=builder /go/src/github.com/log-file-metric-exporter/bin/log-file-metric-exporter  /usr/local/bin/.
COPY --from=builder /go/src/github.com/log-file-metric-exporter/hack/log-file-metric-exporter.sh  /usr/local/bin/.

RUN chmod +x /usr/local/bin/log-file-metric-exporter
RUN chmod +x /usr/local/bin/log-file-metric-exporter.sh

LABEL \
        io.k8s.display-name="OpenShift LogFileMetric Exporter" \
        io.k8s.description="OpenShift LogFileMetric Exporter component of OpenShift Cluster Logging" \
        License="Apache-2.0" \
        name="openshift-logging/log-file-metric-exporter-rhel8" \
        com.redhat.component="log-file-metric-exporter-container" \
        io.openshift.maintainer.product="OpenShift Container Platform" \
        io.openshift.maintainer.component="Logging" \
        io.openshift.build.commit.id=${CI_LOG_FILE_METRIC_EXPORTER_UPSTREAM_COMMIT} \
        io.openshift.build.source-location=${CI_LOG_FILE_METRIC_EXPORTER_UPSTREAM_URL} \
        io.openshift.build.commit.url=${CI_LOG_FILE_METRIC_EXPORTER_UPSTREAM_URL}/commit/${CI_LOG_FILE_METRIC_EXPORTER_UPSTREAM_COMMIT} \
        version=v1.1.0

CMD ["sh", "-c", "/usr/local/bin/log-file-metric-exporter.sh"]
