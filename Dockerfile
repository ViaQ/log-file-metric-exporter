FROM registry.access.redhat.com/ubi9/go-toolset AS builder

ENV BUILD_VERSION=1.1.0
ENV OS_GIT_MAJOR=1
ENV OS_GIT_MINOR=1
ENV OS_GIT_PATCH=0
ENV REMOTE_SOURCE=${REMOTE_SOURCE:-.}


USER 0
WORKDIR  /go/src/github.com/log-file-metric-exporter
COPY ${REMOTE_SOURCE} .

RUN make build

FROM registry.access.redhat.com/ubi9/ubi-minimal
COPY --from=builder /go/src/github.com/log-file-metric-exporter/bin/log-file-metric-exporter  /usr/local/bin/.
COPY --from=builder /go/src/github.com/log-file-metric-exporter/hack/log-file-metric-exporter.sh  /usr/local/bin/.

RUN chmod +x /usr/local/bin/log-file-metric-exporter
RUN chmod +x /usr/local/bin/log-file-metric-exporter.sh

LABEL \
        io.k8s.display-name="OpenShift LogFileMetric Exporter" \
        io.k8s.description="OpenShift LogFileMetric Exporter component of OpenShift Cluster Logging" \
        License="Apache-2.0" \
        name="openshift-logging/log-file-metric-exporter" \
        com.redhat.component="log-file-metric-exporter-container" \
        io.openshift.maintainer.product="OpenShift Container Platform" \
        io.openshift.maintainer.component="Logging" \
        version=v1.1.0

CMD ["sh", "-c", "/usr/local/bin/log-file-metric-exporter.sh"]

