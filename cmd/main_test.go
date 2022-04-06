package main_test

import (
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/log-file-metric-exporter/test/scraper"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const url = "https://localhost:2112/metrics"

// runMain runs the metric exporter watching dir.
func runMain(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("go", "run", "main.go", "-dir="+dir, "-crtFile=testdata/server.crt", "-keyFile=testdata/server.key")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // create session so we can kill go run and sub-processes
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		require.NoError(t, syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL))
		_ = cmd.Wait()
	})
}

// Test that scraped metrics have the correct labels.
func TestScrapeMetrics(t *testing.T) {
	// create directories for test logs
	tmpDir, err := ioutil.TempDir("", t.Name())
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
		require.NoError(t, ioutil.WriteFile(path, data, 0600))
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
