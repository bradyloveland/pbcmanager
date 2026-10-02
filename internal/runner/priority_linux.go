package runner

import "syscall"

// lowerThreadPriority puts the calling OS thread at the lowest CPU priority
// and in the idle I/O class, so measuring doesn't slow the backup down.
func lowerThreadPriority() {
	tid := syscall.Gettid()
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, tid, 19)
	const whoProcess, classIdle = 1, 3
	_, _, _ = syscall.Syscall(syscall.SYS_IOPRIO_SET, whoProcess, uintptr(tid), classIdle<<13)
}
