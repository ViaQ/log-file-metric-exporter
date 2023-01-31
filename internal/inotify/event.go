package inotify

import "golang.org/x/sys/unix"

type NotifyEvent struct {
	unix.InotifyEvent
	path string
}

func (ne NotifyEvent) IsCreate() bool {
	return ne.Mask&unix.IN_CREATE == unix.IN_CREATE
}

func (ne NotifyEvent) IsDelete() bool {
	return ne.Mask&unix.IN_DELETE == unix.IN_DELETE
}

func (ne NotifyEvent) IsModify() bool {
	return ne.Mask&unix.IN_MODIFY == unix.IN_MODIFY
}

func (ne NotifyEvent) IsCloseWrite() bool {
	return ne.Mask&unix.IN_CLOSE_WRITE == unix.IN_CLOSE_WRITE
}

func (ne NotifyEvent) IsIgnored() bool {
	return ne.Mask&unix.IN_IGNORED == unix.IN_IGNORED
}

func (ne NotifyEvent) IsOverFlowErr() bool {
	return ne.Mask&unix.IN_Q_OVERFLOW == unix.IN_Q_OVERFLOW
}

func (ne NotifyEvent) IsDir() bool {
	return ne.Mask&unix.IN_ISDIR == unix.IN_ISDIR // Subject of this event is a directory.
}
