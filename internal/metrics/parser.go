package metrics

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var logFileRegex = regexp.MustCompile(
	`/(?P<namespace>[a-z0-9-]+)_(?P<pod>[a-z0-9.-]+)_(?P<uuid>[a-f0-9-]{36})/(?P<container>[a-z0-9-]+)/(?P<restart>[0-9]+)\.log(?:\.(?P<timestamp>\d{8}-\d{6}))?(?:\.gz)?$`,
)

type ParsedLogFile struct {
	Namespace    string
	Pod          string
	UUID         string
	Container    string
	RestartCount int
	Timestamp    string
	IsArchived   bool
}

func ParseLogFilePath(path string) (ParsedLogFile, error) {
	if path == "" {
		return ParsedLogFile{}, fmt.Errorf("path cannot be empty")
	}

	matches := logFileRegex.FindStringSubmatch(path)
	if matches == nil {
		return ParsedLogFile{}, fmt.Errorf("path does not match log file pattern: %s", path)
	}

	// Get subexpression names and values
	subexpNames := logFileRegex.SubexpNames()
	matchMap := make(map[string]string)
	for i, name := range subexpNames {
		if i != 0 && name != "" && i < len(matches) {
			matchMap[name] = matches[i]
		}
	}

	// Parse restart count
	restartStr := matchMap["restart"]
	restartCount, err := strconv.Atoi(restartStr)
	if err != nil {
		return ParsedLogFile{}, fmt.Errorf("invalid restart count: %s", restartStr)
	}

	// Check if archived (ends with .gz)
	isArchived := strings.HasSuffix(path, ".gz")

	return ParsedLogFile{
		Namespace:    matchMap["namespace"],
		Pod:          matchMap["pod"],
		UUID:         matchMap["uuid"],
		Container:    matchMap["container"],
		RestartCount: restartCount,
		Timestamp:    matchMap["timestamp"],
		IsArchived:   isArchived,
	}, nil
}
