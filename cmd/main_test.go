package main

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/log-file-metric-exporter/pkg/auth"
	"github.com/log-file-metric-exporter/test/scraper"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const url = "https://localhost:2112/metrics"

// fakeAuth returns an authenticator backed by a fake Kubernetes clientset:
// TokenReview reports `authenticated`, SubjectAccessReview reports `allowed`.
// Mirrors the reactor setup in pkg/auth/auth_test.go.
func fakeAuth(authenticated, allowed bool) *auth.KubeAuthenticator {
	fc := fake.NewClientset()
	fc.PrependReactor("create", "tokenreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		createAction := a.(k8stesting.CreateAction)
		review := createAction.GetObject().(*authenticationv1.TokenReview)
		// Verify audience is set correctly
		if len(review.Spec.Audiences) > 0 && review.Spec.Audiences[0] != "log-file-metric-exporter" {
			return true, &authenticationv1.TokenReview{
				Status: authenticationv1.TokenReviewStatus{Authenticated: false},
			}, nil
		}
		return true, &authenticationv1.TokenReview{
			Status: authenticationv1.TokenReviewStatus{
				Authenticated: authenticated,
				User:          authenticationv1.UserInfo{Username: "system:serviceaccount:openshift-monitoring:prometheus-k8s"},
			},
		}, nil
	})
	fc.PrependReactor("create", "subjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &authorizationv1.SubjectAccessReview{
			Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed, Reason: "test"},
		}, nil
	})
	return auth.NewKubeAuthenticatorWithClient(fc)
}

// testMetricsHandler builds a handler using a fake authenticator for testing.
func testMetricsHandler(secureMetrics bool, cacheTTL time.Duration, authenticator *auth.KubeAuthenticator) (http.Handler, error) {
	handler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if secureMetrics {
		if authenticator == nil {
			return nil, fmt.Errorf("no authenticator")
		}
		var a auth.Authenticator = authenticator
		if cacheTTL > 0 {
			a = auth.NewCachedAuthenticator(authenticator, cacheTTL)
		}
		handler = auth.AuthMiddleware(a, handler)
	}
	return handler, nil
}

func serve(t *testing.T, h http.Handler, authHeader string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestMetricsHandler_SecureRejectsTokenless(t *testing.T) {
	h, err := testMetricsHandler(true, 0, fakeAuth(true, true))
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, serve(t, h, ""))
}

func TestMetricsHandler_SecureAllowsValidToken(t *testing.T) {
	h, err := testMetricsHandler(true, 0, fakeAuth(true, true))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, serve(t, h, "Bearer valid-token"))
}

func TestMetricsHandler_SecureForbidsUnauthorized(t *testing.T) {
	h, err := testMetricsHandler(true, 0, fakeAuth(true, false))
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, serve(t, h, "Bearer valid-token"))
}

func TestMetricsHandler_InsecureServesWithoutToken(t *testing.T) {
	h, err := testMetricsHandler(false, 0, nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, serve(t, h, ""))
}

// runMain runs the metric exporter watching dir.
func runMain(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("go", "run", "main.go", "-dir="+dir, "-crtFile=testdata/server.crt", "-keyFile=testdata/server.key", "-secureMetrics=false")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // create session so we can kill go run and sub-processes
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		require.NoError(t, syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM))
		_ = cmd.Wait()
	})
}

// Test that scraped metrics have the correct labels.
func TestScrapeMetrics(t *testing.T) {
	// create directories for test logs
	tmpDir, err := os.MkdirTemp("", t.Name())
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)
	runMain(t, tmpDir)

	// Create a log file
	path := filepath.Join(tmpDir, "test-qegihyox_functional_19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e/something/0.log")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	s := scraper.New()
	findMetric := func() *dto.Metric {
		mfs, err := s.Scrape(url)
		require.NoError(t, err)
		if mf := mfs["log_logged_bytes_total"]; mf != nil {
			return scraper.FindMetric(mf, "poduuid", "19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e")
		}
		return nil
	}

	// Write to log and scrape metric till eventually the exporter has updated the metric.
	data := []byte("hello\n")
	require.Eventually(t, func() bool {
		require.NoError(t, os.WriteFile(path, data, 0600))
		if m := findMetric(); m != nil {
			assert.Equal(t, float64(len(data)), *m.Counter.Value)
			assert.Equal(t, scraper.Labels(m), map[string]string{
				"containername": "something",
				"namespace":     "test-qegihyox",
				"podname":       "functional",
				"poduuid":       "19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e",
			})
			return true
		}
		return false
	}, 10*time.Second, time.Second/10)

	// Write more data, should be detected
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	assert.Eventually(t, func() bool {
		_, err = f.WriteString("more data\n")
		require.NoError(t, err)
		m := findMetric()
		return m != nil && *m.Counter.Value > float64(len(data))
	}, 10*time.Second, time.Second/10)

	// Remove the log, make sure the metric is eventually removed.
	os.Remove(path)
	require.Eventually(t, func() bool { return findMetric() == nil }, 10*time.Second, time.Second/10)
}

func TestParseTLSGroups(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []tls.CurveID
	}{
		{
			name:     "all supported groups",
			input:    []string{"X25519", "secp256r1", "secp384r1", "secp521r1", "X25519MLKEM768"},
			expected: []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384, tls.CurveP521, tls.X25519MLKEM768},
		},
		{
			name:     "single group",
			input:    []string{"X25519"},
			expected: []tls.CurveID{tls.X25519},
		},
		{
			name:     "trims whitespace",
			input:    []string{" secp256r1 ", " secp384r1"},
			expected: []tls.CurveID{tls.CurveP256, tls.CurveP384},
		},
		{
			name:     "skips unsupported groups",
			input:    []string{"X25519", "unsupported", "secp256r1"},
			expected: []tls.CurveID{tls.X25519, tls.CurveP256},
		},
		{
			name:     "empty input",
			input:    []string{},
			expected: []tls.CurveID{},
		},
		{
			name:     "post-quantum hybrid group",
			input:    []string{"X25519MLKEM768"},
			expected: []tls.CurveID{tls.X25519MLKEM768},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := parseTLSGroups(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestOpenSSLToIANACipherSuites(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "maps known ciphers",
			input:    []string{"ECDHE-ECDSA-AES128-GCM-SHA256", "ECDHE-RSA-AES256-GCM-SHA384"},
			expected: []string{"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256", "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384"},
		},
		{
			name:     "skips unknown ciphers",
			input:    []string{"ECDHE-ECDSA-AES128-GCM-SHA256", "UNKNOWN-CIPHER"},
			expected: []string{"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"},
		},
		{
			name:     "empty input",
			input:    []string{},
			expected: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := openSSLToIANACipherSuites(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestSecureMetricsDefaultsTrue(t *testing.T) {
	assert.True(t, defaultSecureMetrics,
		"secureMetrics must default to true so the exporter is fail-closed (LOG-9761)")
}
