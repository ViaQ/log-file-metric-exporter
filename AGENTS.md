# AGENTS.md

This file provides guidance to automated agents when working with code in this repository.

## Project Overview

The log-file-metric-exporter is a Prometheus exporter that monitors Kubernetes pod log files and exports the `log_logged_bytes_total` metric. 
This allows comparison of bytes actually logged versus what log collectors are able to collect during runtime.

## Architecture

### Core Components

1. **Main Entry Point** (`cmd/main.go`)
   - Initializes the log watcher on `/var/log/pods` (configurable via `-dir` flag)
   - Sets up HTTPS server on port 2112 (configurable via `-http` flag) for Prometheus metrics endpoint
   - Configures TLS with customizable minimum version and cipher suites
   - Supports OpenSSL cipher suite names that are mapped to IANA names

2. **Log Watcher** (`pkg/logwatch/watcher.go`)
   - Tracks log file size changes using a custom symlink-aware file watching mechanism
   - Parses Kubernetes log file paths with regex: `/namespace_podname_poduuid/containername/*.log`
   - Maintains a map of `LogLabels` to last known file sizes to detect growth or truncation
   - Uses mutex-protected concurrent access for metrics updates
   - Runs 5 goroutines concurrently to process file system events

3. **Symlink Notifier** (`pkg/symnotify/symnotify.go`)
   - Wraps `fsnotify.Watcher` to handle symlinks properly (critical for Kubernetes log files)
   - Automatically adds watches for symlink targets, directories, and their contents
   - Handles symlink re-targeting on Chmod/Rename events
   - Recursively watches directories for new symlinks and subdirectories

### Key Architectural Patterns

- **Metric Update Logic**: When a file grows, add the difference; when truncated, add the new size (handles log rotation)
- **Concurrent Event Processing**: Uses a pool of 5 goroutines with `sync.WaitGroup` to process events in parallel
- **Label Extraction**: Log file paths follow Kubernetes convention and are parsed to extract namespace, pod name, pod UUID, and container name
- **Prometheus Labels**: `namespace`, `podname`, `poduuid`, `containername`

## Development Commands

### Building and Testing

```bash
# Format code
make fmt

# Build binary (outputs to bin/log-file-metric-exporter)
make build

# Run tests with coverage (outputs to ./tmp/coverage/)
make test

# Run linter
make lint

# Clean artifacts
make clean
```

### Container Operations

```bash
# Build container image
make image

# Build source container image for testing
make image-src

# Test inside container locally
make test-container-local

# Push source image to registry
make push-image-src
```

### Running Tests

```bash
# Run all tests
go test ./pkg/... ./cmd

# Run specific package tests
go test ./pkg/logwatch
go test ./pkg/symnotify

# Run with verbose output
go test -v ./pkg/...

# Run benchmarks
go test -bench=. ./pkg/symnotify
```

## Important Implementation Details

### Log Path Regex
The regex pattern `/([a-z0-9-]+)_([a-z0-9-]+)_([a-f0-9-]+)/([a-z0-9-]+)/.*\.log` expects:
- Group 1: namespace (lowercase alphanumeric and hyphens)
- Group 2: pod name (lowercase alphanumeric and hyphens)
- Group 3: pod UUID (hex and hyphens)
- Group 4: container name (lowercase alphanumeric and hyphens)

### Metric Behavior
- File grows: `counter.Add(newSize - lastSize)`
- File truncated: `counter.Add(newSize)` (starts counting from truncation point)
- File removed: Delete metric labels and forget size tracking

### TLS Configuration
- Minimum TLS version: Use `-tlsMinVersion` flag (e.g., "VersionTLS12", "VersionTLS13")
- Cipher suites: Use `-cipherSuites` flag with comma-separated OpenSSL names
- The code maps OpenSSL cipher names to IANA names (see `openSSLToIANACiphersMap`)

### Concurrency Model
The `Watch()` method runs an infinite loop where each iteration:
1. Creates a `WaitGroup` with count 5
2. Spawns 5 goroutines that each call `watcher.Event()` (blocking)
3. Waits for all 5 to complete before starting the next batch
This ensures continuous event processing with controlled parallelism.

## Testing Notes

- Test files use the `_test.go` suffix
- Coverage reports are generated in HTML format at `./tmp/coverage/test-unit-coverage.html`
- The test suite includes benchmarks for `symnotify` performance
- Tests verify log label parsing, file watching behavior, and metric updates
