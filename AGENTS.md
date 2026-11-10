# AGENTS.md

This file provides guidance to automated agents (AI or otherwise) when working with code in this repository.

## Project Overview

The log-file-metric-exporter is a Prometheus exporter that monitors Kubernetes pod log files and exports the `log_logged_bytes_total` counter metric. This allows comparison of bytes actually logged versus what log collectors (e.g., Fluentd, Vector) are able to collect during runtime.

## Core Principles

1. **Security First**: Default to HTTPS with TLS; authentication should be the norm, not optional
2. **Kubernetes Native**: Assume pod log structure at `/var/log/pods/<namespace>_<podname>_<uuid>/<container>/`
3. **Simplicity**: Prefer sequential metric updates and straightforward logic over extreme performance
4. **Testability**: All components should have unit tests; benchmarks for performance-critical paths

## Architecture Summary

See [ARCHITECTURE.md](ARCHITECTURE.md) for detailed design decisions, tradeoffs, and implementation details.

### Core Components

1. **Main Entry Point** (`cmd/main.go`)
   - Parses command-line flags (directory, port, TLS, auth token)
   - Sets up HTTPS with TLS certificates and configurable cipher suites
   - Mounts Prometheus metrics endpoint with optional bearer token auth
   - Spawns LogWatcher in a background goroutine

2. **Log Watcher** (`pkg/logwatch/watcher.go`)
   - Watches directory (default: `/var/log/pods`) for file size changes
   - Parses Kubernetes log paths with regex to extract labels: namespace, pod name, pod UUID, container name
   - Maintains last-known file sizes; detects growth, truncation, and deletion
   - Updates `log_logged_bytes_total` counter on size changes
   - Runs 5 concurrent goroutines to process events

3. **Symlink Notifier** (`pkg/symnotify/symnotify.go`)
   - Wraps `fsnotify.Watcher` to handle Kubernetes symlinked log files
   - Auto-adds watches for symlink targets and directories
   - Recursively watches subdirectories; re-targets on symlink changes
   - Critical for correctness (without this, many events would be missed)

4. **Kubernetes Auth** (`pkg/auth/`)
   - `auth.go`: `KubeAuthenticator` validates bearer tokens via Kubernetes TokenReview API and checks authorization via SubjectAccessReview
   - `middleware.go`: `AuthMiddleware` wraps the metrics handler — extracts bearer token from `Authorization` header, authenticates, then authorizes `GET` on the request path
   - Enabled when `-secureMetrics` flag is set to `true`

## Problems This Project Solves

### 1. Tracking Actual Log Volume
**Problem**: In Kubernetes, there's no built-in way to see how many bytes each pod is actually writing to its logs. Log collectors (Fluentd, Vector) may fall behind, and without a reference metric you can't tell how much data is being lost.

**How**: The exporter watches log files at `/var/log/pods` and maintains a `log_logged_bytes_total` counter per namespace/pod/container. It tracks file size changes — when a file grows, the delta is added to the counter. This gives Prometheus a ground-truth metric to compare against collector ingestion rates.

### 2. Handling Kubernetes Symlinked Log Files
**Problem**: Kubernetes stores pod logs as symlinks that point to container runtime log locations. When logs rotate, the symlink target changes. Standard `fsnotify` doesn't track symlink re-targeting — it reports the event on the symlink path but won't automatically watch the new target.

**How**: `SymNotifier` (`pkg/symnotify`) wraps `fsnotify` and handles this transparently. On Chmod/Rename events, it detects symlink re-targeting by removing and re-adding the watch. On Create events, it recursively adds watches for new symlinks and subdirectories.

### 3. Handling Log Rotation
**Problem**: Container logs rotate — files get truncated and start fresh. A naive watcher that assumes files only grow would either miss data or double-count bytes after rotation.

**How**: `Watcher.Update()` in `pkg/logwatch` compares the current file size to the last known size. If the file grew (`size > lastSize`), it adds the delta. If the file was truncated (`size < lastSize`), it adds just the new size — correctly counting bytes written after rotation without double-counting.

### 4. Controlled Concurrency Under Load
**Problem**: A busy cluster can generate thousands of file system events per second. Processing events one at a time is too slow, but spawning a goroutine per event leads to unbounded resource usage.

**How**: `Watcher.Watch()` in `pkg/logwatch` uses a fixed pool of 5 goroutines per iteration. Each goroutine blocks on the next event, processes it, then the pool resets. This drains the event queue faster than sequential processing while keeping resource usage predictable.

### 5. Flexible Log Path Discovery
**Problem**: The watch directory and log path structure may vary across environments. Hard-coding paths would limit portability.

**How**: The watch directory is configurable via the `-dir` flag (default: `/var/log/pods`). Log paths are parsed with a regex that extracts namespace, pod name, pod UUID, and container name from the Kubernetes path convention. This tolerates minor path variations without code changes.

## Development Workflow

### Building
```bash
make build     # Binary to bin/log-file-metric-exporter
make fmt       # Format Go code
make lint      # Run linter
make clean     # Remove artifacts
```

### Testing
```bash
make test              # Run all tests + coverage report
go test -v ./pkg/...   # Verbose test output
go test -bench=. ./pkg/symnotify  # Benchmarks
```

### Containers
```bash
make image             # Build container image
make image-src         # Build test/debug container
make test-container-local  # Run tests in container
```

## Code Style and Conventions

### Go Conventions
- Follow standard Go style (enforced by `gofmt`)
- Run `make fmt` before committing
- No multi-line comments; one-liner comments only
- Comment the *why*, not the *what* (code should be clear)

### Naming
- Metrics: lowercase with underscores (e.g., `log_logged_bytes_total`)
- Functions/types: PascalCase (Go standard)
- Constants: Go convention (exported `PascalCase`, unexported `camelCase`)
- Package names: short, lowercase, no underscores

### Metrics and Labels
- Metric: `log_logged_bytes_total` (counter, in bytes)
- Labels: `namespace`, `podname`, `poduuid`, `containername` (all lowercase)
- No high-cardinality labels (avoid unique pod IDs); use pod UUID instead

### Import Organization
```go
import (
    "fmt"
    "os"
    // stdlib
    
    "github.com/prometheus/client_golang/prometheus"
    // vendor
    
    "github.com/log-file-metric-exporter/pkg/logwatch"
    // local
)
```

## Key Code Paths

### When Starting Work
1. Read this file first
2. Skim [ARCHITECTURE.md](ARCHITECTURE.md) for the relevant component
3. Look at the component's `_test.go` file to understand behavior

### Path Parsing
See `pkg/logwatch/watcher.go`: `LogLabels.Parse(path)`
- Regex (package-level `logFile` var): `/([a-z0-9-]+)_([a-z0-9-]+)_([a-f0-9-]+)/([a-z0-9-]+)/.*\.log`
- Groups: Namespace, Name (pod name), UUID, Container
- Case: all labels must be lowercase

### Metric Updates
See `pkg/logwatch/watcher.go`: `Watcher.Update(path)`
- **File grows**: `counter.Add(size - lastSize)`
- **File truncates**: `counter.Add(size)` (size < lastSize means rotation)
- **File deleted / disappears**: `Watcher.Forget(path)` — deletes metric labels and size tracking

### Event Processing
See `pkg/logwatch/watcher.go`: `Watcher.Watch()` and `Watcher.processNextEvent()`
- Loop: spawn 5 goroutines, each calls `symnotify.Watcher.Event()` (blocking), wait for all, repeat
- Each `Event()` call returns one fsnotify event (Name, Op)
- On `Remove`: call `Forget()`; on all others: call `Update()`
- Don't change this concurrency pattern without benchmarking

### Symlink Handling
See `pkg/symnotify/symnotify.go`: `Watcher.Add(name)` and `Watcher.Event()`
- `Add()`: watches the path; if it's a directory, scans for subdirectories and symlinks and watches them recursively
- `Event()`: on Create events, adds watches for new symlinks/directories; on Chmod/Rename, removes and re-adds watch (symlink target may have changed); on Remove, removes the watch
- Directory watches are recursive; new subdirectories auto-watched

## Testing Guidelines

### Unit Test Structure
```go
func TestMyFunction(t *testing.T) {
    // Setup
    input := setupTestData()
    
    // Execute
    result := MyFunction(input)
    
    // Assert
    if result != expected {
        t.Errorf("got %v, want %v", result, expected)
    }
}
```

### Required Test Coverage
- **Happy path**: Normal operation (file grows, log rotation, deletion)
- **Edge cases**: Empty files, rapid size changes, symlink re-targeting
- **Error cases**: Permission errors, missing files, invalid paths

### Benchmarks
`pkg/symnotify/symnotify_benchmark_test.go` has `BenchmarkStress` for file system event throughput.
Run with: `go test -bench=. ./pkg/symnotify`

## Common Commands

```bash
# Build and test
make build test lint

# Run a single test
go test -run TestParseLogLabels ./pkg/logwatch

# Debug with verbose output
go test -v -run TestParseLogLabels ./pkg/logwatch

# Check coverage for a package
go test -cover ./pkg/logwatch

# Run container tests
make test-container-local
```

## Security: CWE-327 Hardening (LOG-9764)

As of this version, the exporter enforces strict TLS security to comply with CWE-327 (Use of Broken or Risky Cryptographic Algorithm).

### Supported TLS Versions
- **TLS 1.2 and TLS 1.3 only**. TLS 1.0 and 1.1 are explicitly rejected.
- The `supportedTlsVersions` map (cmd/main.go:27-30) contains only `VersionTLS12` and `VersionTLS13`
- Unknown versions trigger startup failure (`os.Exit(1)`)
- Explicit `MinVersion: tls.VersionTLS12` on tls.Config (cmd/main.go:188) prevents fallback to weaker versions

### Supported Cipher Suites
- **AEAD ciphers only**: AES-GCM and ChaCha20-Poly1305 with ECDHE key exchange
- Go's secure ciphers (`tls.CipherSuites()`) are listed in `supportedCipherSuites` map (cmd/main.go:42-50)
- OpenSSL-named ciphers from Kubernetes profiles are translated via `openSSLToIANACiphersMap` (cmd/main.go:55-68)
- All weak ciphers (CBC mode, 3DES, SHA-1 MACs, non-ECDHE) are permanently excluded
- Unknown ciphers are logged as errors and excluded from configuration

### Impact on Kubernetes TLS Profiles
When deployed via the Cluster Logging Operator, the exporter interacts with OpenShift TLS profiles:
- **Intermediate** and **Modern** profiles: Fully supported; no breaking changes
- **Old** profile: TLS 1.0/1.1 requests will be upgraded to TLS 1.2; insecure ciphers (CBC, 3DES, SHA-1 MAC) will be silently filtered. Monitoring clients must support TLS 1.2+ with AEAD ciphers
- **Custom** profiles: Only ciphers matching the secure OpenSSL-to-IANA mapping are accepted

### Deployment Guidance
If scraper clients (Prometheus, Grafana, etc.) are unable to connect after upgrade:
- **Error**: "protocol version not supported" or "no shared cipher suites"
- **Resolution**: Upgrade scraper clients to support TLS 1.2+ (standard since ~2015)
- Consult [CONTRIBUTING.md](CONTRIBUTING.md) for TLS configuration details

## When to Update This File

- Add guidance for common mistakes you discover
- Document new conventions or patterns
- Remove guidance that becomes obsolete
- Link to new architecture documentation

## References

- **Architecture**: [ARCHITECTURE.md](ARCHITECTURE.md)
- **Contributing**: [CONTRIBUTING.md](CONTRIBUTING.md)
- **README**: [README.md](README.md)
- **Prometheus Docs**: https://prometheus.io/docs/instrumenting/exposition_formats/
- **fsnotify**: https://github.com/fsnotify/fsnotify
