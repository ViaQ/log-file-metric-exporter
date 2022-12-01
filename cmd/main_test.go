package main

import (
	"testing"
)

func TestRegex(t *testing.T) {
	filename := "/var/log/containers/service-ca-operator-797d5bc576-jxgvk_openshift-service-ca-operator_service-ca-operator-2b255ed149909946b26570b65895c0d07d7e8e852bc25f3f18620120a44f1c74.log"
	matches := kubernetesregexpCompiled.FindStringSubmatch(filename)
	if len(matches) != 5 {
		t.Fatalf("log file name could not be parsed.")
	}
	if matches[1] != "service-ca-operator-797d5bc576-jxgvk" {
		t.Fatalf("log file name could not be parsed. incorrect pod name")
	}
	if matches[2] != "openshift-service-ca-operator" {
		t.Fatalf("log file name could not be parsed. incorrect namespace name")
	}
	if matches[3] != "service-ca-operator" {
		t.Fatalf("log file name could not be parsed. incorrect container name")
	}
	if matches[4] != "2b255ed149909946b26570b65895c0d07d7e8e852bc25f3f18620120a44f1c74" {
		t.Fatalf("log file name could not be parsed. incorrect dockerid")
	}
}
