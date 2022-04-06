package logwatch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseLogLabels(t *testing.T) {
	path := "/var/log/pods/test-qegihyox_functional_19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e/something/0.log"
	var l LogLabels
	assert.True(t, l.Parse(path))
	want := LogLabels{
		Namespace: "test-qegihyox",
		Name:      "functional",
		UUID:      "19b40c1b-df6d-4e63-b5aa-d6c5ed20ac4e",
		Container: "something",
	}
	assert.Equal(t, want, l)
}
