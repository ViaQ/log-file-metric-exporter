FROM golang:1.23 AS builder

USER 0
WORKDIR  /go/src/github.com/log-file-metric-exporter

COPY ./go.mod ./go.sum ./
RUN go mod download
COPY Makefile ./
COPY ./cmd ./cmd
COPY ./pkg ./pkg

RUN make build

FROM registry.access.redhat.com/ubi9/ubi-minimal

ARG BUILD_VERSION=1.2.0

COPY --from=builder /go/src/github.com/log-file-metric-exporter/bin/log-file-metric-exporter  /usr/local/bin/.
RUN chmod +x /usr/local/bin/log-file-metric-exporter

LABEL \
        io.k8s.display-name="OpenShift LogFileMetric Exporter" \
        io.k8s.description="OpenShift LogFileMetric Exporter component of OpenShift Cluster Logging" \
        License="Apache-2.0" \
        name="openshift-logging/log-file-metric-exporter" \
        com.redhat.component="log-file-metric-exporter-container" \
        io.openshift.maintainer.product="OpenShift Container Platform" \
        io.openshift.maintainer.component="Logging" \
        version="v${BUILD_VERSION}"

CMD ["/usr/local/bin/log-file-metric-exporter", "-verbosity=2", "-dir=/var/log/containers", "-http=:2112"]

