export GOROOT=$(shell go env GOROOT)
export GOFLAGS=
export GO111MODULE=on

ARTIFACT_DIR?=./tmp
CURPATH=$(PWD)
GOFLAGS?=
BUILD_VERSION?=1.2.0
BIN_NAME=log-file-metric-exporter
CONTAINER_BUILD_ARGS ?=
IMAGE_REPOSITORY_NAME=quay.io/openshift-logging/origin-${BIN_NAME}:v${BUILD_VERSION}
LOCAL_IMAGE_TAG=127.0.0.1:5000/openshift/origin-${BIN_NAME}:v${BUILD_VERSION}
#just for testing purpose pushing it to docker.io
MAIN_PKG=cmd/main.go
TARGET_DIR=$(CURPATH)/_output
TARGET=$(CURPATH)/bin/$(BIN_NAME)
BUILD_GOPATH=$(TARGET_DIR)

#inputs to 'run' which may need to change
TLS_CERTS_BASEDIR=_output
NAMESPACE ?= "openshift-logging"
ES_CERTS_DIR ?= ""
CACHE_EXPIRY ?= "5s"

PKGS=$(shell go list ./...)
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

image:
	podman build -f Dockerfile -t $(LOCAL_IMAGE_TAG) $(CONTAINER_BUILD_ARGS) --build-arg BUILD_VERSION=$(BUILD_VERSION) .
	podman tag ${LOCAL_IMAGE_TAG} ${IMAGE_REPOSITORY_NAME}
.PHONY: image

image-src:
	podman build -f Dockerfile.src -t $(LOCAL_IMAGE_TAG)-src .
	podman tag ${LOCAL_IMAGE_TAG}-src ${IMAGE_REPOSITORY_NAME}-src
.PHONY: image-src

deploy-image: image
	IMAGE_TAG=$(LOCAL_IMAGE_TAG) hack/deploy-image.sh
	IMAGE_TAG=$(IMAGE_REPOSITORY_NAME) hack/deploy-image.sh
.PHONY: deploy-image

clean:
	rm -rf $(TARGET_DIR)
.PHONY: clean

COVERAGE_DIR=$(ARTIFACT_DIR)/coverage
test: artifactdir
	@mkdir -p $(COVERAGE_DIR)
	@go test -race -coverprofile=$(COVERAGE_DIR)/test-unit.cov ./pkg/...
	@go test -v ./cmd
	@go tool cover -html=$(COVERAGE_DIR)/test-unit.cov -o $(COVERAGE_DIR)/test-unit-coverage.html
	@go tool cover -func=$(COVERAGE_DIR)/test-unit.cov | tail -n 1
.PHONY: test

test-container-local: image-src
	podman run -it $(LOCAL_IMAGE_TAG)-src make test
.PHONY: test-container-local

test-container-on-cluster: push-image-src
	oc create ns test-deleteme
	oc -n test-deleteme run test-unit --image-pull-policy='Always' --image=${IMAGE_REPOSITORY_NAME}-src --restart='Never' --command -- make test
.PHONY: test-container-on-cluster

push-image-src: image-src
	podman push $(IMAGE_REPOSITORY_NAME)-src
.PHONY: push-image-src

lint:
	@hack/run-linter
.PHONY: lint
