package metrics

import (
	"strconv"
	"testing"
)

func TestRegex(t *testing.T) {

	type test struct {
		ParsedLogFile
		path string
	}

	tests := []test{
		{
			path: "/var/log/pods/openshift-kube-controller-manager_kube-controller-manager-crc-pbwlw-master-0_738a9f84e9aed99070694fd38123a679/cluster-version-operator/2.log",
			ParsedLogFile: ParsedLogFile{
				Namespace:    "openshift-kube-controller-manager",
				Pod:          "kube-controller-manager-crc-pbwlw-master-0",
				UUID:         "738a9f84e9aed99070694fd38123a679",
				Container:    "cluster-version-operator",
				RestartCount: 2,
			},
		},
		{
			path: "/var/log/pods/openshift-kube-controller-manager_kube-controller-manager-crc-pbwlw-master-0_738a9f84e9aed99070694fd38123a679/cluster-version-operator/0.log.20230102-180708",
			ParsedLogFile: ParsedLogFile{
				Namespace:    "openshift-kube-controller-manager",
				Pod:          "kube-controller-manager-crc-pbwlw-master-0",
				UUID:         "738a9f84e9aed99070694fd38123a679",
				Container:    "cluster-version-operator",
				RestartCount: 0,
				Timespamp:    "20230102-180708",
			},
		},
		{
			path: "/var/log/pods/openshift-kube-controller-manager_kube-controller-manager-crc-pbwlw-master-0_738a9f84e9aed99070694fd38123a679/cluster-version-operator/2.log.20230105-030511.gz",
			ParsedLogFile: ParsedLogFile{
				Namespace:    "openshift-kube-controller-manager",
				Pod:          "kube-controller-manager-crc-pbwlw-master-0",
				UUID:         "738a9f84e9aed99070694fd38123a679",
				Container:    "cluster-version-operator",
				RestartCount: 2,
				Timespamp:    "20230105-030511",
				IsArchived:   true,
			},
		},
	}
	for _, tc := range tests {
		matches := logFileRegex.FindStringSubmatch(tc.path)
		if len(matches) != 9 {
			t.Errorf("regex matches mismatched. want: 9, have: %d", len(matches))
		}
		if tc.Namespace != matches[1] {
			t.Errorf("namespace want: %s, have: %s", tc.Namespace, matches[1])
		}
		if tc.Pod != matches[2] {
			t.Errorf("pod want: %s, have: %s", tc.Pod, matches[2])
		}
		if tc.UUID != matches[3] {
			t.Errorf("uuid want: %s, have: %s", tc.UUID, matches[3])
		}
		if tc.Container != matches[4] {
			t.Errorf("container want: %s, have: %s", tc.Container, matches[4])
		}
		restartCount, _ := strconv.Atoi(matches[5])
		if tc.RestartCount != restartCount {
			t.Errorf("restartCount want: %d, have: %d", tc.RestartCount, restartCount)
		}
		if tc.Timespamp != matches[7] {
			t.Errorf("timespamp want: %s, have: %s", tc.Timespamp, matches[7])
		}
		if tc.IsArchived != (matches[8] == ".gz") {
			t.Errorf("zip file want: %v, have: %v", tc.IsArchived, (matches[8] == ".gz"))
		}
	}
}
