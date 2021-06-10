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



all: fmt build image deploy-image
.PHONY: all

artifactdir:
	@mkdir -p $(ARTIFACT_DIR)


fmt:
	@gofmt -l -w cmd && \
	gofmt -l -w pkg
.PHONY: fmt

build: fmt
	go build $(LDFLAGS) -o $(TARGET) $(MAIN_PKG)
.PHONY: build

vendor:
	go mod vendor
.PHONY: vendor

image:
	podman build -f Dockerfile -t $(LOCAL_IMAGE_TAG) .
.PHONY: image

deploy-image: image
	IMAGE_TAG=$(LOCAL_IMAGE_TAG) hack/deploy-image.sh
.PHONY: deploy-image

clean:
	rm -rf $(TARGET_DIR)
.PHONY: clean

COVERAGE_DIR=$(ARTIFACT_DIR)/coverage
test: artifactdir
	@mkdir -p $(COVERAGE_DIR)
	@go test -race -coverprofile=$(COVERAGE_DIR)/test-unit.cov ./pkg/...
	@go tool cover -html=$(COVERAGE_DIR)/test-unit.cov -o $(COVERAGE_DIR)/test-unit-coverage.html
	@go tool cover -func=$(COVERAGE_DIR)/test-unit.cov | tail -n 1
.PHONY: test

lint:
	@hack/run-linter
.PHONY: lint
gen-dockerfiles:
	./hack/generate-dockerfile-from-midstream > Dockerfile
.PHONY: gen-dockerfiles
