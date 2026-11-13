# CLO Changes Required for LOG-9766

## Overview
The cluster-logging-operator needs to be updated to support the log-file-metric-exporter's new audience scoping and caching features (LOG-9766).

## Files to Modify

### 1. `internal/metrics/logfilemetricexporter/factory.go`

#### ONLY Change: Add authCacheTTL flag and optionally audience to container args

**Location:** `newLogMetricsExporterContainer()` function, Args array (around line 117)

**Current code:**
```go
exporterContainer.Args = []string{"-c",
    "/usr/local/bin/log-file-metric-exporter -verbosity=2 -dir=/var/log/pods -http=:2112 -keyFile=/etc/logfilemetricexporter/metrics/tls.key -crtFile=/etc/logfilemetricexporter/metrics/tls.crt -secureMetrics -tlsMinVersion=" +
        tls.MinTLSVersion(tlsProfileSpec) + " -cipherSuites=" + strings.Join(tls.TLSCiphers(tlsProfileSpec), ",") +
        " -groups=" + strings.Join(tls.TLSGroups(tlsProfileSpec), ",")}
```

**Minimum change (caching only, no audience scoping):**
```go
exporterContainer.Args = []string{"-c",
    "/usr/local/bin/log-file-metric-exporter -verbosity=2 -dir=/var/log/pods -http=:2112 -keyFile=/etc/logfilemetricexporter/metrics/tls.key -crtFile=/etc/logfilemetricexporter/metrics/tls.crt -secureMetrics -authCacheTTL=10s -tlsMinVersion=" +
        tls.MinTLSVersion(tlsProfileSpec) + " -cipherSuites=" + strings.Join(tls.TLSCiphers(tlsProfileSpec), ",") +
        " -groups=" + strings.Join(tls.TLSGroups(tlsProfileSpec), ",")}
```

**With audience scoping (requires coordinated Prometheus update):**
```go
exporterContainer.Args = []string{"-c",
    "/usr/local/bin/log-file-metric-exporter -verbosity=2 -dir=/var/log/pods -http=:2112 -keyFile=/etc/logfilemetricexporter/metrics/tls.key -crtFile=/etc/logfilemetricexporter/metrics/tls.crt -secureMetrics -authCacheTTL=10s -audience=log-file-metric-exporter -tlsMinVersion=" +
        tls.MinTLSVersion(tlsProfileSpec) + " -cipherSuites=" + strings.Join(tls.TLSCiphers(tlsProfileSpec), ",") +
        " -groups=" + strings.Join(tls.TLSGroups(tlsProfileSpec), ",")}
```

**Notes:**
- The `-authCacheTTL` flag is required to enable caching and reduce API server load.
- The `-audience` flag is optional and defaults to empty (disabled). Only set if Prometheus is configured to request tokens with a specific audience.
- When `-audience` is set, Prometheus MUST be configured to request and present a token with the same audience value, or scraping will fail with HTTP 401.

## Optional: Update ServiceMonitor for Token Audience

**Only required if exporter is deployed with `-audience` flag.**

**Location:** ServiceMonitor configuration that CLO generates for Prometheus scraping

If the exporter is configured with `-audience=log-file-metric-exporter`, the ServiceMonitor (or equivalent scraping config) must request a token with the same audience so that Prometheus presents the correct token to the exporter's `/metrics` endpoint.

This typically involves updating the Prometheus scrape configuration to use:
```yaml
bearerTokenFile: /var/run/secrets/bound-token/token
```

Where the bound token is projected with `audience: log-file-metric-exporter`.

**If exporter uses default `-audience=""** (empty, disabled):
- No audience scoping is performed; any valid bearer token is accepted
- Prometheus can use default ServiceAccount tokens without audience projection
- This maintains backward compatibility with existing deployments

**Note:** This is Prometheus-side configuration, not the exporter's own pod spec.

## Optional Enhancement: Make Cache TTL and Audience Configurable

### Add to `api/logging/v1alpha1/log_file_metrics_exporter_types.go`:

```go
type LogFileMetricExporterSpec struct {
    // ... existing fields ...
    
    // AuthCacheTTL specifies the TTL for caching TokenReview and SubjectAccessReview results.
    // A value of 0 disables caching.
    // +optional
    // +kubebuilder:default="10s"
    AuthCacheTTL *metav1.Duration `json:"authCacheTTL,omitempty"`
    
    // TokenAudience specifies the audience for TokenReview requests.
    // Prometheus must request tokens with the same audience.
    // An empty string disables audience scoping.
    // +optional
    // +kubebuilder:default="log-file-metric-exporter"
    TokenAudience *string `json:"tokenAudience,omitempty"`
}
```

Then in `factory.go`, use:
```go
cacheTTL := "10s"  // default
if exporter.Spec.AuthCacheTTL != nil {
    cacheTTL = exporter.Spec.AuthCacheTTL.Duration.String()
}

audience := "log-file-metric-exporter"  // default
if exporter.Spec.TokenAudience != nil {
    audience = *exporter.Spec.TokenAudience
}

exporterContainer.Args = []string{"-c",
    "/usr/local/bin/log-file-metric-exporter ... -authCacheTTL=" + cacheTTL + " -audience=" + audience + " ..."}
```

## Testing

After making these changes in CLO:

1. Build and deploy updated CLO
2. Create/update LogFileMetricExporter CR
3. Verify the exporter pod has:
   - Container arg includes `-authCacheTTL=10s`
   - **No** bound-sa-token volume (exporter doesn't need it)

4. Verify Prometheus ServiceMonitor/scraping config requests bound token with correct audience

5. Verify metrics scraping works:
   ```bash
   oc -n openshift-logging get pods -l component=log-file-metric-exporter
   # Check pod logs for "metrics endpoint secured with bearer token authentication and caching"
   
   # Verify Prometheus can scrape
   oc -n openshift-monitoring get servicemonitor log-file-metric-exporter -o yaml
   ```

6. Validate audience scoping (Prometheus-side):
   ```bash
   # Prometheus should present a token with audience: log-file-metric-exporter
   # Scraping should succeed (HTTP 200)
   
   # If manually testing with wrong audience token, it should fail (401)
   TOKEN=$(oc -n openshift-monitoring create token prometheus-k8s --audience=wrong-audience)
   oc -n openshift-logging exec <exporter-pod> -- curl -k -H "Authorization: Bearer $TOKEN" https://localhost:2112/metrics
   # Expected: HTTP 401
   ```

## Backward Compatibility Note

**IMPORTANT:** These changes add audience scoping, which means tokens without the specific audience will be rejected. This is a **breaking change** that requires coordination:

1. Prometheus/Prometheus Operator must be configured FIRST to request bound tokens with `log-file-metric-exporter` audience
2. Then log-file-metric-exporter can be updated with audience validation
3. Rolling update should work if Prometheus is already presenting the correct token

## Dependencies

- Kubernetes 1.20+ (ServiceAccountToken volume projection)
- log-file-metric-exporter image with LOG-9766 changes

## Related Issues

- LOG-9766: TokenReview without audience scoping; no authn/authz result caching
- LOG-9765: Container runs as root (related security improvement)

## PR Checklist

- [ ] Add `-authCacheTTL=10s` flag to exporter container args in `factory.go`
- [ ] Optionally add `-audience=log-file-metric-exporter` flag (requires coordinated Prometheus update)
- [ ] If adding audience flag: update ServiceMonitor/scraping config to request bound token with matching audience
- [ ] Update unit tests in `daemonset_test.go`
- [ ] Update e2e tests in `test/e2e/logfilesmetricexporter/`
- [ ] Update documentation in `docs/administration/logfilemetricexporter.adoc`
- [ ] Verify exporter pod can authenticate scrapers with/without audience scoping
- [ ] Verify Prometheus can access `/metrics` with correctly-configured token
- [ ] Verify RBAC permissions (should already exist from LOG-9761)
