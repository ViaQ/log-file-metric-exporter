package symnotify_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"os"

	"math/rand"

	"github.com/log-file-metric-exporter/pkg/symnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// To check for memory growth run this benchmark for a longer time, e.g.
// go test -memprofile=mem.prof -run X -bench BenchmarkStress -benchtime=5m
func BenchmarkStress(b *testing.B) {
	dir := b.TempDir()
	w, err := symnotify.NewWatcher()
	require.NoError(b, err)
	require.NoError(b, w.Add(dir))
	files := make([]*os.File, 512)
	for i := 0; i < b.N; i++ {
		n := rand.Intn(len(files))
		f := files[n]
		if f == nil { // Create if file not present
			f, err = os.Create(filepath.Join(dir, fmt.Sprintf("log%d", n)))
			files[n] = f
			require.NoError(b, err)
		} else {
			p := rand.Intn(100)
			if p < 10 { // Remove 10% of the time
				_ = f.Close()
				require.NoError(b, os.Remove(f.Name()))
				files[n] = nil
			} else { // Write 90% of the time
				f.Write([]byte("hello\n"))
			}
		}
		// Consume the event
		e, err := w.Event()
		require.NoError(b, err)
		assert.Equal(b, f.Name(), e.Name)
	}
}
