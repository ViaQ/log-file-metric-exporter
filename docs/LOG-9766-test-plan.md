# LOG-9766 Test Plan: Audience Scoping and Authentication Caching

## Overview
This test plan validates the audience scoping and caching improvements for TokenReview/SubjectAccessReview in the log-file-metric-exporter when deployed via cluster-logging-operator (CLO).

## Prerequisites
- OpenShift cluster with cluster-logging-operator installed
- Access to create/modify ClusterLogging resources
- `oc` CLI with cluster-admin privileges
- Prometheus access to verify metrics scraping

## Test Environment Setup

### 1. Deploy Updated Exporter via CLO
```bash
# Build and push updated image
make image
podman tag log-file-metric-exporter:latest <registry>/log-file-metric-exporter:log9766-test
podman push <registry>/log-file-metric-exporter:log9766-test

# Update CLO deployment to use test image
oc -n openshift-logging patch daemonset log-file-metric-exporter \
  -p '{"spec":{"template":{"spec":{"containers":[{"name":"log-file-metric-exporter","image":"<registry>/log-file-metric-exporter:log9766-test"}]}}}}'
```

### 2. Verify Operator Configuration
Check that the operator has configured the exporter with:
```bash
# Check pod args include authCacheTTL
oc -n openshift-logging get ds log-file-metric-exporter -o jsonpath='{.spec.template.spec.containers[0].args}'

# Expected: should include -authCacheTTL=10s (or custom value)
```

## Test Cases

### TC-0: Verify Backward Compatibility (Default Empty Audience)

**Objective:** Confirm that exporter works with default settings (no audience scoping) and accepts any valid bearer token.

**Steps:**
1. Verify exporter is running with default flags (empty audience):
   ```bash
   oc -n openshift-logging get ds log-file-metric-exporter -o jsonpath='{.spec.template.spec.containers[0].args}' | grep -q -- '-audience' && echo "audience flag set" || echo "audience flag not set (default empty)"
   ```

**Expected Result:** audience flag is not set (defaults to empty)

2. Verify Prometheus scraping works with default configuration:
   ```bash
   oc -n openshift-monitoring port-forward svc/prometheus-k8s 9090:9090 &
   curl -s http://localhost:9090/api/v1/targets | jq '.data.activeTargets[] | select(.labels.job=="log-file-metric-exporter")'
   ```

**Expected Result:** Target state is `up` (HTTP 200 from exporter, no audience scoping required)

### TC-1: Verify Audience Scoping When Enabled

**Objective:** Confirm that when audience is configured, only tokens with matching audience are accepted.

**Prerequisites:** Exporter must be deployed with `-audience=log-file-metric-exporter` flag.

**Steps:**
1. Verify exporter is configured with audience:
   ```bash
   oc -n openshift-logging get ds log-file-metric-exporter -o jsonpath='{.spec.template.spec.containers[0].args}' | grep -o -- '-audience=[^ ]*'
   ```

**Expected Result:** `-audience=log-file-metric-exporter` is present

2. Test with token matching the audience:
   ```bash
   POD=$(oc -n openshift-logging get pod -l component=log-file-metric-exporter -o name | head -1)
   TOKEN=$(oc -n openshift-monitoring create token prometheus-k8s --audience=log-file-metric-exporter)
   
   oc -n openshift-logging exec $POD -- curl -sk -H "Authorization: Bearer $TOKEN" https://localhost:2112/metrics | head -5
   ```

**Expected Result:** HTTP 200, metrics returned

3. Test with WRONG audience token:
   ```bash
   TOKEN=$(oc -n openshift-monitoring create token prometheus-k8s --audience=wrong-audience)
   
   oc -n openshift-logging exec $POD -- curl -sk -H "Authorization: Bearer $TOKEN" https://localhost:2112/metrics
   ```

**Expected Result:** HTTP 401 Unauthorized

**Validation:**
```bash
# Check exporter logs
oc -n openshift-logging logs -l component=log-file-metric-exporter --tail=50 | grep -i "authenticated"
```

### TC-2: Verify Caching Reduces API Server Load

**Objective:** Confirm that TokenReview/SAR calls are cached and not repeated within TTL.

**Steps:**
1. Enable audit logging for TokenReview/SAR on the cluster (if available) OR check API server metrics:
   ```bash
   # Get API server metrics (if accessible)
   oc get --raw /metrics | grep 'apiserver_request_total{verb="create",resource="tokenreviews"}'
   oc get --raw /metrics | grep 'apiserver_request_total{verb="create",resource="subjectaccessreviews"}'
   ```

2. Trigger multiple scrapes within the cache TTL (default 10s):
   ```bash
   # Get valid token
   TOKEN=$(oc -n openshift-monitoring create token prometheus-k8s --audience=log-file-metric-exporter)
   POD=$(oc -n openshift-logging get pod -l component=log-file-metric-exporter -o name | head -1)
   
   # Scrape 10 times rapidly (within 10s window)
   for i in {1..10}; do
     oc -n openshift-logging exec $POD -- curl -sk -H "Authorization: Bearer $TOKEN" https://localhost:2112/metrics > /dev/null
     echo "Scrape $i completed"
   done
   ```

3. Check API server request counts:
   ```bash
   # Compare before/after counts for tokenreviews
   oc get --raw /metrics | grep 'apiserver_request_total{verb="create",resource="tokenreviews"}'
   ```

**Expected Result:** 
- First scrape: 1 TokenReview + 1 SubjectAccessReview
- Subsequent scrapes (within 10s): 0 additional API calls (cache hit)
- Total: Only 1 TokenReview + 1 SAR for all 10 scrapes

**Validation:**
Check exporter logs don't show repeated "authenticated request" messages:
```bash
oc -n openshift-logging logs -l component=log-file-metric-exporter --since=30s | grep "authenticated request" | wc -l
# Should be 1, not 10
```

### TC-3: Verify Negative Caching (Failed Auth)

**Objective:** Confirm that failed authentication attempts are also cached.

**Steps:**
1. Attempt to scrape with invalid token multiple times:
   ```bash
   POD=$(oc -n openshift-logging get pod -l component=log-file-metric-exporter -o name | head -1)
   
   # 5 rapid attempts with bad token
   for i in {1..5}; do
     oc -n openshift-logging exec $POD -- curl -sk -H "Authorization: Bearer bad-token-xyz" https://localhost:2112/metrics
   done
   ```

2. Check API server metrics:
   ```bash
   oc get --raw /metrics | grep 'apiserver_request_total{verb="create",resource="tokenreviews"}'
   ```

**Expected Result:** Only 1 TokenReview API call (subsequent attempts served from cache)

### TC-4: Verify Cache Expiration

**Objective:** Confirm cache expires after TTL and re-validates.

**Steps:**
1. Scrape with valid token:
   ```bash
   TOKEN=$(oc -n openshift-monitoring create token prometheus-k8s --audience=log-file-metric-exporter)
   POD=$(oc -n openshift-logging get pod -l component=log-file-metric-exporter -o name | head -1)
   
   oc -n openshift-logging exec $POD -- curl -sk -H "Authorization: Bearer $TOKEN" https://localhost:2112/metrics > /dev/null
   ```

2. Wait for cache TTL to expire:
   ```bash
   sleep 12  # Wait longer than default 10s TTL
   ```

3. Scrape again with same token:
   ```bash
   oc -n openshift-logging exec $POD -- curl -sk -H "Authorization: Bearer $TOKEN" https://localhost:2112/metrics > /dev/null
   ```

**Expected Result:** 
- Second scrape triggers new TokenReview (cache expired)
- Check logs show new "authenticated request" message

### TC-5: Verify Prometheus Scraping Still Works

**Objective:** Ensure legitimate Prometheus scraping continues to work.

**Steps:**
1. Check Prometheus targets:
   ```bash
   # Access Prometheus UI or use API
   oc -n openshift-monitoring port-forward svc/prometheus-k8s 9090:9090 &
   
   # Check target health
   curl -s http://localhost:9090/api/v1/targets | jq '.data.activeTargets[] | select(.labels.job=="log-file-metric-exporter")'
   ```

2. Verify metrics are being scraped:
   ```bash
   # Query for log_logged_bytes_total
   curl -s 'http://localhost:9090/api/v1/query?query=log_logged_bytes_total' | jq '.data.result | length'
   ```

**Expected Result:** 
- Target state: `up`
- Metrics present with recent timestamps

### TC-6: Verify Cache TTL Configuration

**Objective:** Test custom cache TTL values.

**Steps:**
1. Update exporter with different cache TTL:
   ```bash
   # Via operator CR if it supports authCacheTTL in spec
   oc -n openshift-logging patch logfilemetricexporter default --type=json -p='[
     {"op":"replace","path":"/spec/authCacheTTL","value":"30s"}
   ]'
   ```

2. Verify the setting took effect:
   ```bash
   oc -n openshift-logging logs -l component=log-file-metric-exporter --tail=10 | grep "authCacheTTL"
   ```

3. Test cache behavior with new TTL (repeat TC-4 with 30s wait)

**Expected Result:** Cache expires after 30s, not 10s

### TC-6b: Verify Audience Configuration

**Objective:** Test custom audience values.

**Steps:**
1. Update exporter with different audience:
   ```bash
   # Via operator CR if it supports tokenAudience in spec
   oc -n openshift-logging patch logfilemetricexporter default --type=json -p='[
     {"op":"replace","path":"/spec/tokenAudience","value":"custom-audience"}
   ]'
   ```

2. Verify the setting took effect:
   ```bash
   oc -n openshift-logging logs -l component=log-file-metric-exporter --tail=10 | grep "audience"
   ```

3. Update Prometheus ServiceMonitor to use the new audience:
   ```bash
   oc -n openshift-monitoring patch servicemonitor log-file-metric-exporter --type=json -p='[
     {"op":"replace","path":"/spec/endpoints/0/bearerTokenSecret/name","value":"<new-bound-token-with-custom-audience>"}
   ]'
   ```

4. Verify Prometheus can scrape with new audience:
   ```bash
   TOKEN=$(oc -n openshift-monitoring create token prometheus-k8s --audience=custom-audience)
   POD=$(oc -n openshift-logging get pod -l component=log-file-metric-exporter -o name | head -1)
   oc -n openshift-logging exec $POD -- curl -sk -H "Authorization: Bearer $TOKEN" https://localhost:2112/metrics | head -5
   ```

**Expected Result:** HTTP 200 with new audience; HTTP 401 with old audience

### TC-7: Verify Caching Disabled (cacheTTL=0)

**Objective:** Confirm cache can be disabled.

**Steps:**
1. Set cacheTTL to 0:
   ```bash
   oc -n openshift-logging patch ds log-file-metric-exporter --type=json -p='[
     {"op":"replace","path":"/spec/template/spec/containers/0/args","value":["-authCacheTTL=0s"]}
   ]'
   ```

2. Perform multiple scrapes:
   ```bash
   TOKEN=$(oc -n openshift-monitoring create token prometheus-k8s --audience=log-file-metric-exporter)
   POD=$(oc -n openshift-logging get pod -l component=log-file-metric-exporter -o name | head -1)
   
   for i in {1..3}; do
     oc -n openshift-logging exec $POD -- curl -sk -H "Authorization: Bearer $TOKEN" https://localhost:2112/metrics > /dev/null
   done
   ```

3. Check logs:
   ```bash
   oc -n openshift-logging logs -l component=log-file-metric-exporter --since=30s | grep "authenticated request" | wc -l
   ```

**Expected Result:** 
- Logs show 3 "authenticated request" messages (no caching)
- Logs show "caching disabled" message on startup

## Operator Requirements Validation

### Verify Operator Grants Correct Permissions
```bash
# Check ServiceAccount has TokenReview/SAR permissions
SA=$(oc -n openshift-logging get ds log-file-metric-exporter -o jsonpath='{.spec.template.spec.serviceAccountName}')

oc -n openshift-logging describe sa $SA
oc get clusterrolebinding -o json | jq ".items[] | select(.subjects[]?.name==\"$SA\" and .subjects[]?.namespace==\"openshift-logging\")"
```

**Expected:** ServiceAccount bound to ClusterRole with:
- `tokenreviews.authentication.k8s.io` create
- `subjectaccessreviews.authorization.k8s.io` create

## Performance Testing

### Measure Cache Effectiveness
```bash
# Run load test: 100 scrapes over 60 seconds
POD=$(oc -n openshift-logging get pod -l component=log-file-metric-exporter -o name | head -1)
TOKEN=$(oc -n openshift-monitoring create token prometheus-k8s --audience=log-file-metric-exporter --duration=3600s)

# Before: get API server request baseline
BEFORE=$(oc get --raw /metrics | grep 'apiserver_request_total{.*resource="tokenreviews".*}' | awk '{print $2}')

# Execute 100 scrapes with 0.6s interval (simulates frequent Prometheus scrapes)
for i in {1..100}; do
  oc -n openshift-logging exec $POD -- curl -sk -H "Authorization: Bearer $TOKEN" https://localhost:2112/metrics > /dev/null &
  sleep 0.6
done
wait

# After: check increase
AFTER=$(oc get --raw /metrics | grep 'apiserver_request_total{.*resource="tokenreviews".*}' | awk '{print $2}')

echo "TokenReview calls: $((AFTER - BEFORE))"
```

**Expected:** 
- Without cache: ~100 TokenReview calls
- With cache (10s TTL): ~10-20 calls (cache hits reduce load)

## Rollback Testing

### Verify Graceful Degradation
If operator doesn't provide bound token with correct audience:

**Steps:**
1. Remove audience from projected token (simulate operator not updated)
2. Attempt scrape

**Expected Result:** 
- Exporter rejects authentication (fail-closed)
- Logs show "token is not authenticated" or similar
- No metrics exposed to unauthorized clients

## Sign-off Criteria

- [ ] TC-0: Backward compatibility maintained (default empty audience works)
- [ ] TC-1: Audience scoping enforced when enabled (wrong audience rejected)
- [ ] TC-2: Caching reduces API calls (verified via metrics)
- [ ] TC-3: Negative caching works (failed auth cached)
- [ ] TC-4: Cache expires after TTL
- [ ] TC-5: Prometheus scraping works end-to-end
- [ ] TC-6: Custom cache TTL configurable via -authCacheTTL flag
- [ ] TC-6b: Custom audience configurable via -audience flag (optional)
- [ ] TC-7: Cache can be disabled (cacheTTL=0)
- [ ] Existing deployments work without changes (audience defaults to empty/disabled)
- [ ] Prometheus can be updated independently to use audience scoping when ready
- [ ] Performance improvement: >80% reduction in TokenReview calls under load
- [ ] No regressions: all existing functionality works

## Known Issues / Operator Gaps

If tests fail, document here what operator changes are needed:

1. **Missing authCacheTTL flag**: Operator should pass `-authCacheTTL=10s` in container args
   - Verify with: `oc -n openshift-logging get ds log-file-metric-exporter -o jsonpath='{.spec.template.spec.containers[0].args}'`
   - Fix: Add `-authCacheTTL=10s` to exporter container args in factory.go

2. **Missing audience flag**: Operator should pass `-audience=log-file-metric-exporter` in container args
   - Verify with: `oc -n openshift-logging get ds log-file-metric-exporter -o jsonpath='{.spec.template.spec.containers[0].args}'`
   - Fix: Add `-audience=log-file-metric-exporter` to exporter container args in factory.go

3. **Prometheus ServiceMonitor not configured for bound token**: CLO must configure Prometheus to request a token with matching audience
   - Verify with: `oc -n openshift-monitoring get servicemonitor log-file-metric-exporter -o yaml`
   - Fix: Add projected ServiceAccount token volume to Prometheus pod with matching audience

4. **Mismatch between exporter and Prometheus audiences**: If exporter uses `-audience=X` but Prometheus requests audience Y, scraping will fail with 401
   - Fix: Ensure both exporter and Prometheus are configured with the same audience value

5. **Other gaps**: (document as discovered)

## References
- Jira: https://redhat.atlassian.net/browse/LOG-9766
- PR: (add link after creation)
