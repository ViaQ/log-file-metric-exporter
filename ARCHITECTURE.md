# Architecture: log-file-metric-exporter

This document describes the internal design, key decisions, and implementation details.

## Overview

The exporter is a Prometheus metrics collector running as a sidecar or standalone pod in a Kubernetes cluster. It watches pod log files on the local node and exports metrics via HTTPS on port 2112 (configurable).

**Main metric**: `log_logged_bytes_total` – a counter per pod/container showing the cumulative bytes logged since the exporter started watching.

## System Architecture

There are two independent flows that share the Prometheus registry:

```text
  SCRAPE PATH (on-demand)             WATCH PATH (continuous background)

┌──────────────────────┐
│  Prometheus Scraper  │
│  (queries /metrics)  │
└──────────┬───────────┘
           │ HTTPS
           │ (+ bearer token if
           │  -secureMetrics=true)
           │
┌──────────▼───────────┐          ┌──────────────────────────┐
│  HTTPS Server        │          │  /var/log/pods           │
│  (cmd/main.go)       │          │  (k8s log directory)     │
│  • Port 2112         │          └──────────┬───────────────┘
│  • TLS configured    │                     │ fsnotify events
│  • /metrics endpoint │                     │
│  • Auth middleware   │          ┌──────────▼───────────────┐
│    (optional)        │          │  SymNotifier             │
└──────────┬───────────┘          │  (pkg/symnotify)         │
           │ reads                │  • Wraps fsnotify        │
           │                      │  • Handles symlinks      │
           │                      │  • Watches recursively   │
           │                      └──────────┬───────────────┘
           │                                 │ events
           │                                 │
           │                      ┌──────────▼───────────────┐
           │                      │  LogWatcher              │
           │                      │  (pkg/logwatch)          │
           │                      │  • Tracks file sizes     │
           │                      │  • Detects growth/       │
           │                      │    truncation/deletion   │
           │                      └──────────┬───────────────┘
           │                                 │ writes
           │    ┌────────────────────────┐   │
           └───►│  Prometheus Registry   │   │
                │ log_logged_bytes_total │◄──┘
                │  (shared counter)      │
                └────────────────────────┘
```

`cmd/main.go` initializes both paths: the HTTPS server runs on the main goroutine, the LogWatcher runs in a background goroutine.

## Component Details

### 1. Main Entry Point (`cmd/main.go`)

**Responsibilities**:
- Parse command-line flags (`-dir`, `-http`, `-crtFile`, `-keyFile`, `-tlsMinVersion`, `-cipherSuites`, `-groups`, `-secureMetrics`, `-verbosity`)
- Initialize TLS configuration with optional custom cipher suites and curve preferences
- Set up Prometheus HTTPS server
- Mount `/metrics` endpoint with optional Kubernetes-based bearer token auth
- Start log watcher goroutine

**Key Design Decisions**:
- **TLS by default**: Server always runs HTTPS via `ListenAndServeTLS` — there is no plain HTTP mode
- **Configurable ciphers**: OpenSSL cipher names are mapped to IANA names via `openSSLToIANACiphersMap`; TLS 1.3 ciphers and DHE ciphers are skipped with log messages (Go manages TLS 1.3 ciphers automatically; DHE is not implemented in Go's crypto/tls)
- **Configurable TLS curves**: `-groups` flag accepts curve names (e.g., `X25519`, `secp256r1`) mapped via `supportedTLSGroups`
- **Optional authentication**: When `-secureMetrics=true`, metrics endpoint is wrapped with `auth.AuthMiddleware` which validates tokens via Kubernetes TokenReview API and checks authorization via SubjectAccessReview
- **Single entry point**: One command handles all configuration and startup
- **No graceful shutdown**: The server exits on `ListenAndServeTLS` error or `os.Exit(1)` on watcher error — no signal handling

**Concurrency**:
- Main goroutine runs HTTPS server (`ListenAndServeTLS`, blocking)
- Background goroutine runs `LogWatcher.Watch()` (infinite loop, blocking)

### 2. Log Watcher (`pkg/logwatch/watcher.go`)

**Responsibilities**:
- Watch a directory (default: `/var/log/pods`) for log file changes
- Parse Kubernetes log file paths to extract namespace, pod name, pod UUID, and container name
- Track file sizes and detect growth or truncation
- Update Prometheus counter metric with size deltas
- Handle file deletion and cleanup

**Key Design Decisions**:
- **Path parsing via regex**: Extract labels from Kubernetes log path format
  - Pattern: `/namespace_podname_uuid/containername/*.log`
  - Robust to symlinks and log rotation
- **Metric on delta**: Only add to counter when file size changes, not on every event
- **Truncation handling**: When file is truncated (log rotation), add only the new size as the delta
  - This correctly represents cumulative bytes written, not file size
- **Goroutine pool**: Process events concurrently with 5 goroutines
  - Each iteration: spawn 5 workers, wait for all to finish, repeat
  - Prevents event queue from growing unbounded
- **Mutex-protected state**: `LogLabels → lastSize` map protected by sync.Mutex

**Label Extraction**:
```text
/var/log/pods/default_my-pod_a1b2c3d4-e5f6/container-name/0.log
├─ namespace: "default"
├─ podname: "my-pod"
├─ poduuid: "a1b2c3d4-e5f6"
└─ containername: "container-name"
```

**Metric Update Logic**:
| Event | Behavior |
|-------|----------|
| File grows | `counter.Add(newSize - lastSize)` |
| File truncated | `counter.Add(newSize)` (new size = new delta) |
| File removed | `Forget()`: delete metric labels, forget size tracking |
| File first seen | `Update()`: lastSize is 0, so `counter.Add(size)` — initial size is counted |

### 3. Symlink Notifier (`pkg/symnotify/symnotify.go`)

**Responsibilities**:
- Wrap `fsnotify.Watcher` to handle symlinks properly
- Automatically add watches for symlink targets and directories
- Recursively watch nested directories for new logs
- Handle symlink re-targeting on events

**Key Design Decisions**:
- **Symlink transparency**: fsnotify reports events on symlinks, not targets
  - SymNotifier detects when a watched path is a symlink and watches the target too
  - When symlinks re-target (Rename/Chmod events), update watches accordingly
- **Recursive watching**: Initial watch adds watches for the directory and all subdirectories
  - New subdirectories trigger recursive watch addition
- **Event passthrough**: SymNotifier forwards all events from fsnotify with minimal filtering
- **Error handling**: fsnotify errors are propagated; directory watch failures don't block the watcher

**Why This Matters**:
Kubernetes stores pod logs as symlinks for two reasons:
1. Isolation: symlinks point to a container runtime location
2. Rotation: logs may get re-targeted without path changes

Without proper symlink handling, the watcher would miss these events.

## Concurrency Model

### Watch Loop (LogWatcher)
```go
func (w *Watcher) Watch() error {
    for {
        max := 5
        wg := sync.WaitGroup{}
        wg.Add(max)
        for i := 1; i <= max; i++ {
            go w.processNextEvent(&wg)   // Each goroutine calls wg.Done()
        }
        wg.Wait()                        // Wait for all 5 before next batch
    }
}
```

Each `processNextEvent` calls `w.watcher.Event()` (blocking on fsnotify channels), then either `Forget()` on Remove or `Update()` for all other events.

**Why This Design**:
- Events pile up in fsnotify's channel during processing
- 5 goroutines drain the queue faster than sequential processing
- WaitGroup ensures controlled parallelism (exactly 5 workers per iteration)
- Doesn't create unbounded goroutines

### Metric Updates
- Multiple goroutines may call `Update()` concurrently
- `LogLabels → lastSize` map is protected by `sync.RWMutex` (declared as RWMutex but only write-locks are used currently via `Lock()`/`Unlock()` in both `Update()` and `Forget()`)
- Prometheus counter operations (`Add`, `GetMetricWithLabelValues`, `DeleteLabelValues`) are thread-safe
- No fine-grained locks; simplicity over throughput (log watches aren't latency-critical)

## TLS Configuration

The exporter supports both standard and custom TLS configurations:

### Standard TLS
```bash
./log-file-metric-exporter \
  -crtFile=/etc/tls/cert.pem \
  -keyFile=/etc/tls/key.pem \
  -tlsMinVersion=VersionTLS13
```

### Custom Cipher Suites
OpenSSL names are mapped to IANA names. For example:
```bash
./log-file-metric-exporter \
  -cipherSuites="ECDHE-RSA-AES128-GCM-SHA256,ECDHE-RSA-AES256-GCM-SHA384"
```

The `openSSLToIANACiphersMap` in `cmd/main.go` handles the translation.

## Metrics Schema

### Counter: `log_logged_bytes_total`

**Labels**:
- `namespace` – Kubernetes namespace (e.g., "default")
- `podname` – Pod name (e.g., "my-app-pod")
- `poduuid` – Pod UUID for uniqueness (e.g., "a1b2c3d4-e5f6")
- `containername` – Container name (e.g., "main")

**Type**: Counter (monotonically increasing)

**Semantics**: Total bytes written to the container's log file since the exporter started watching.

**Example**:
```text
log_logged_bytes_total{namespace="default",podname="nginx-abcd1234",poduuid="a1b2c3d4",containername="nginx"} 102400
```

## Testing Strategy

### Unit Tests
- `cmd/main_test.go`: `TestScrapeMetrics`, `TestParseTLSGroups`, `TestOpenSSLToIANACipherSuites`
- `pkg/logwatch/log_labels_test.go`: `TestParseLogLabels` — label extraction from paths
- `pkg/logwatch/watcher_test.go`: `TestWatcherSeesFileChange`, `TestWatcherSeesAndWatchesExistingFiles`
- `pkg/symnotify/symnotify_test.go`: Create/write/remove events, real files, symlinks, symlink re-targeting, subdirectory watching
- `pkg/auth/auth_test.go`: Middleware tests — success, missing header, invalid scheme, empty token, unauthenticated, unauthorized

### Benchmarks
- `pkg/symnotify/symnotify_benchmark_test.go`: `BenchmarkStress` — stress-tests file system event throughput

### Integration Tests
- Container build test: Verify image builds correctly
- Local container test: `make test-container-local`

## Known Tradeoffs

### 1. Pooled Goroutines vs. Unbounded
**Decision**: Fixed pool of 5 goroutines.
- **Pro**: Predictable memory, no goroutine explosion under high log volume
- **Con**: May be slower than fully unbounded under extreme load
- **Rationale**: Log watches are typically in the hundreds or thousands, not millions; predictability matters in production

### 2. Regex Parsing vs. Direct Path Construction
**Decision**: Regex-based label extraction from Kubernetes log paths.
- **Pro**: Works with any log location; resilient to path changes
- **Con**: Slightly slower than direct parsing; brittle if path format changes
- **Rationale**: Kubernetes log path format is stable; regex is more maintainable than manual parsing

### 3. Metric on Delta vs. Absolute Size
**Decision**: Counter adds the *difference* in size, not absolute size.
- **Pro**: Handles log rotation gracefully; semantically correct counter
- **Con**: More complex logic than just reporting file size
- **Rationale**: Log rotation is common in containers; delta-based counting is correct for a "total bytes logged" metric

### 4. Single-Threaded Event Processing vs. Parallel
**Decision**: Sequential event processing with pooled goroutines.
- **Pro**: No lock contention on the metrics map; simpler code
- **Con**: May be slower under extreme event volume
- **Rationale**: Metric writes are short operations; batching them is simpler than fine-grained parallelism

## Future Considerations

- **Event batching**: Batch events across the 5-goroutine pool to reduce wake-ups
- **Metrics types**: Add gauges for current file sizes or goroutine counts
- **Filtering**: Allow regex filters to watch only certain pods/containers
- **Performance**: Profile under high log volume; consider lock-free data structures if needed

## Debugging

### Enable Verbose Logging
The exporter uses `logerr` (ViaQ/logerr/v2). Increase verbosity with the `-verbosity` flag (default 0). Internal log calls use `log.V(3)` and `log.V(4)`, so `-verbosity=3` or higher will show detailed event and metric update logs.

### Test Locally
```bash
# Build and run in foreground
make build
./bin/log-file-metric-exporter -dir /var/log/pods -http :2112

# In another terminal, query metrics (no auth)
curl -k https://localhost:2112/metrics

# If -secureMetrics is enabled, include a bearer token
curl -k --header "Authorization: Bearer YOUR_TOKEN" https://localhost:2112/metrics
```

### Run Tests with Verbose Output
```bash
go test -v ./pkg/...
```

### Check Coverage
After running tests, open the HTML report:
```bash
open tmp/coverage/test-unit-coverage.html
```
