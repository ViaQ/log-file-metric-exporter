export GOROOT=$(shell go env GOROOT)
export GOFLAGS=-mod=vendor
export GO111MODULE=on

ARTIFACT_DIR?=./tmp
CURPATH=$(PWD)
GOFLAGS?=
CLO_RELEASE_VERSION?=5.2
BIN_NAME=log-file-metric-exporter
IMAGE_REPOSITORY_NAME=quay.io/openshift/origin-${BIN_NAME}
LOCAL_IMAGE_TAG=127.0.0.1:5000/openshift/origin-${BIN_NAME}:${CLO_RELEASE_VERSION}
MAIN_PKG=cmd/main.go
TARGET_DIR=$(CURPATH)/_output
TARGET=$(CURPATH)/bin/$(BIN_NAME)
BUILD_GOPATH=$(TARGET_DIR)

#inputs to 'run' which may need to change
TLS_CERTS_BASEDIR=_output
NAMESPACE ?= "openshift-logging"
ES_CERTS_DIR ?= ""
CACHE_EXPIRY ?= "5s"

PKGS=$(shell go list ./... | grep -v -E '/vendor/')
TEST_OPTIONS?=

build: 
	echo "Build....."
.PHONY: build

image:
	docker build -f Dockerfile -t $(LOCAL_IMAGE_TAG) .
.PHONY: image

test: 
	echo "Testing...."
.PHONY: test
