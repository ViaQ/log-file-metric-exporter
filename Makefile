
IMAGE=viaq/logwatcher:v0.0.1
TARGET=bin/logwatcher
MAIN_PKG=main.go

.PHONY: build
build:
	go build $(LDFLAGS) -o $(TARGET) $(MAIN_PKG)

.PHONY: image
image:
	podman build -f Dockerfile . -t $(IMAGE)
