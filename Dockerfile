### This is a generated file from Dockerfile.in ###
#@follow_tag(openshift-golang-builder:1.14)
FROM registry.ci.openshift.org/ocp/builder:rhel-8-golang-1.15-openshift-4.7 AS builder

ENV BUILD_VERSION=${CI_CONTAINER_VERSION}
ENV OS_GIT_MAJOR=${CI_X_VERSION}
ENV OS_GIT_MINOR=${CI_Y_VERSION}
ENV OS_GIT_PATCH=${CI_Z_VERSION}
ENV SOURCE_GIT_COMMIT=${CI_LOG_FILE_METRIC_EXPORTER_UPSTREAM_COMMIT}
ENV SOURCE_GIT_URL=${CI_LOG_FILE_METRIC_EXPORTER_UPSTREAM_URL}
ENV REMOTE_SOURCE=${REMOTE_SOURCE:-.}


WORKDIR  /go/src/github.com/log-file-metric-exporter
COPY ${REMOTE_SOURCE} .
ADD ${REMOTE_SOURCE}/Makefile .

RUN make build

#@follow_tag(openshift-ose-base:ubi8)
FROM registry.ci.openshift.org/ocp/4.7:base
COPY --from=builder /go/src/github.com/log-file-metric-exporter/bin/log-file-metric-exporter  /usr/local/bin/.
COPY --from=builder /go/src/github.com/log-file-metric-exporter/hack/log-file-metric-exporter.sh  /usr/local/bin/.

RUN chmod +x /usr/local/bin/log-file-metric-exporter
RUN chmod +x /usr/local/bin/log-file-metric-exporter.sh

LABEL \
        io.k8s.display-name="OpenShift LogFileMetric Exporter" \
        io.k8s.description="OpenShift LogFileMetric Exporter component of OpenShift Cluster Logging" \
        name="openshift/log-file-metric-exporter" \
        com.redhat.component="log-file-metric-exporter-container" \
        io.openshift.maintainer.product="OpenShift Container Platform" \
        io.openshift.maintainer.component="Logging" \
        io.openshift.build.commit.id=${CI_LOG_FILE_METRIC_EXPORTER_UPSTREAM_COMMIT} \
        io.openshift.build.source-location=${CI_LOG_FILE_METRIC_EXPORTER_UPSTREAM_URL} \
        io.openshift.build.commit.url=${CI_LOG_FILE_METRIC_EXPORTER_UPSTREAM_URL}/commit/${CI_LOG_FILE_METRIC_EXPORTER_UPSTREAM_COMMIT} \
        version=${CI_CONTAINER_VERSION}

CMD ["sh", "-c", "/usr/local/bin/log-file-metric-exporter.sh"]

