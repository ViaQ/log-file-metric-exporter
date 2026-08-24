package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLogFilePath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		want    ParsedLogFile
		wantErr bool
	}{
		{
			name: "standard log file",
			path: "/var/log/pods/test-ns_my-pod_19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e/my-container/0.log",
			want: ParsedLogFile{
				Namespace:    "test-ns",
				Pod:          "my-pod",
				UUID:         "19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e",
				Container:    "my-container",
				RestartCount: 0,
				Timestamp:    "",
				IsArchived:   false,
			},
		},
		{
			name: "rotated log with timestamp",
			path: "/var/log/pods/ns_pod_19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e/container/1.log.20230102-180708",
			want: ParsedLogFile{
				Namespace:    "ns",
				Pod:          "pod",
				UUID:         "19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e",
				Container:    "container",
				RestartCount: 1,
				Timestamp:    "20230102-180708",
				IsArchived:   false,
			},
		},
		{
			name: "archived rotated log",
			path: "/var/log/pods/ns_pod_19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e/container/2.log.20230105-030537.gz",
			want: ParsedLogFile{
				Namespace:    "ns",
				Pod:          "pod",
				UUID:         "19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e",
				Container:    "container",
				RestartCount: 2,
				Timestamp:    "20230105-030537",
				IsArchived:   true,
			},
		},
		{
			name: "pod name with dots",
			path: "/var/log/pods/monitoring_prometheus-0.abc_19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e/prometheus/0.log",
			want: ParsedLogFile{
				Namespace:    "monitoring",
				Pod:          "prometheus-0.abc",
				UUID:         "19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e",
				Container:    "prometheus",
				RestartCount: 0,
				Timestamp:    "",
				IsArchived:   false,
			},
		},
		{
			name:    "non-log file",
			path:    "/var/log/pods/ns_pod_uuid/container/something.txt",
			wantErr: true,
		},
		{
			name:    "empty path",
			path:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLogFilePath(tt.path)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
