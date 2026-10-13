package inotify

import (
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

// NotifyEvent wraps a unix.InotifyEvent with the associated path.
type NotifyEvent struct {
	unix.InotifyEvent
	Path string
}

// IsCreate returns true if the event indicates a CREATE operation.
func (ne *NotifyEvent) IsCreate() bool {
	return ne.Mask&unix.IN_CREATE != 0
}

// IsDelete returns true if the event indicates a DELETE operation.
func (ne *NotifyEvent) IsDelete() bool {
	return ne.Mask&unix.IN_DELETE != 0
}

// IsModify returns true if the event indicates a MODIFY operation.
func (ne *NotifyEvent) IsModify() bool {
	return ne.Mask&unix.IN_MODIFY != 0
}

// IsCloseWrite returns true if the event indicates a CLOSE_WRITE operation.
func (ne *NotifyEvent) IsCloseWrite() bool {
	return ne.Mask&unix.IN_CLOSE_WRITE != 0
}

// IsIgnored returns true if the watch was removed or invalidated.
func (ne *NotifyEvent) IsIgnored() bool {
	return ne.Mask&unix.IN_IGNORED != 0
}

// IsOverFlowErr returns true if the event queue overflowed.
func (ne *NotifyEvent) IsOverFlowErr() bool {
	return ne.Mask&unix.IN_Q_OVERFLOW != 0
}

// IsDir returns true if the event is for a directory.
func (ne *NotifyEvent) IsDir() bool {
	return ne.Mask&unix.IN_ISDIR != 0
}

// String returns a human-readable representation of the event for logging.
func (ne *NotifyEvent) String() string {
	var flags []string

	if ne.IsCreate() {
		flags = append(flags, "CREATE")
	}
	if ne.IsDelete() {
		flags = append(flags, "DELETE")
	}
	if ne.IsModify() {
		flags = append(flags, "MODIFY")
	}
	if ne.IsCloseWrite() {
		flags = append(flags, "CLOSE_WRITE")
	}
	if ne.IsIgnored() {
		flags = append(flags, "IGNORED")
	}
	if ne.IsOverFlowErr() {
		flags = append(flags, "Q_OVERFLOW")
	}
	if ne.IsDir() {
		flags = append(flags, "ISDIR")
	}

	flagsStr := strings.Join(flags, "|")
	if flagsStr == "" {
		flagsStr = fmt.Sprintf("0x%x", ne.Mask)
	}

	return fmt.Sprintf("NotifyEvent{Wd:%d, Mask:%s, Path:%q}", ne.Wd, flagsStr, ne.Path)
}
