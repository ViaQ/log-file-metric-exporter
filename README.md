# log-file-metric-exporter

A Prometheus exporter that monitors Kubernetes pod log files and exports metrics about the volume of data being logged.

## What It Does

This exporter collects metrics about container logs being produced in a Kubernetes environment. It publishes the `log_logged_bytes_total` counter metric in Prometheus, allowing you to:
- See total data bytes actually logged in your clusters
- Compare against what log collectors (e.g., Fluentd) are able to collect during runtime
- Monitor log volume per namespace, pod, and container

The exporter uses Go's `fsnotify` package to watch for size changes in pod log files at the configured path (default: `/var/log/pods`).

## Building and Running Locally

### Prerequisites
- Go (see `go.mod` for minimum version)
- Make
- GNU tools (for scripts)

### Build the Binary
```bash
make build
```
The binary outputs to `bin/log-file-metric-exporter`.

### Run Tests
```bash
make test
```
This generates a coverage report at `tmp/coverage/test-unit-coverage.html`.

### Run the Exporter
```bash
./bin/log-file-metric-exporter -dir /var/log/pods -http :2112
```

### Available Flags
- `-dir` - Directory path to watch for log files (default: `/var/log/pods`)
- `-http` - HTTP server address for metrics endpoint (default: `:2112`)
- `-crtFile` - Path to TLS certificate file (default: `/etc/fluent/metrics/tls.crt`)
- `-keyFile` - Path to TLS private key file (default: `/etc/fluent/metrics/tls.key`)
- `-tlsMinVersion` - Minimum TLS version (e.g., `VersionTLS12`, `VersionTLS13`)
- `-cipherSuites` - Comma-separated list of OpenSSL cipher suite names
- `-groups` - TLS groups/curves for key exchange (e.g., `X25519,secp256r1,secp384r1`)
- `-secureMetrics` - Require valid bearer token for metrics scraping (default: `true`)
- `-authCacheTTL` - TTL for caching authentication/authorization results (default: `10s`; 0 to disable)
- `-reconcileInterval` - Interval for full disk reconcile to prune stale metrics (default: `5m`; 0 to disable)
- `-verbosity` - Log verbosity level (default: `0`)

## Container Operations
```bash
make image              # Build container image
make image-src         # Build source container for testing
make test-container-local  # Run tests inside container
```

## Metrics Endpoint
The exporter serves metrics at `https://<host>:<port>/metrics` by default (port 2112).

Example metrics output:
```text
log_logged_bytes_total{namespace="default",podname="my-pod",poduuid="uuid-1234",containername="app"} 1024000
log_logged_bytes_total{namespace="kube-system",podname="etcd",poduuid="uuid-5678",containername="etcd"} 512000
```

## More Information
- **Architecture and Design**: See [ARCHITECTURE.md](ARCHITECTURE.md) for internal design details
- **Contributing**: See [CONTRIBUTING.md](CONTRIBUTING.md) for how to submit changes
- **Agent Instructions**: See [AGENTS.md](AGENTS.md) for guidance when working with this code
