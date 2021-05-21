### This is a generated file from Dockerfile.in ###
#@follow_tag(openshift-golang-builder:1.14)
FROM registry.ci.openshift.org/ocp/builder:rhel-8-golang-1.15-openshift-4.7 AS builder

ENV REMOTE_SOURCE=${REMOTE_SOURCE:-.}
ENV dir=/var/log/containers/
ENV verbosity=2
ENV http=:2112
WORKDIR  /go/src/github.com/log-file-metric-exporter
COPY ${REMOTE_SOURCE} .

RUN make build

