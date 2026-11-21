//go:build windows

package instance

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	lockfileExclusiveLock   = 0x00000002
	lockfileFailImmediately = 0x00000001
)

type overlapped struct {
	Internal     uintptr
	InternalHigh uintptr
	Offset       uint32
	OffsetHigh   uint32
	HEvent       syscall.Handle
}

var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx = kernel32.NewProc("LockFileEx")
	procUnlockFile = kernel32.NewProc("UnlockFileEx")
)

func lockFile(f *os.File) error {
	var ov overlapped
	r1, _, e := procLockFileEx.Call(f.Fd(), lockfileExclusiveLock|lockfileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&ov)))
	if r1 == 0 {
		return e
	}
	return nil
}

func unlockFile(f *os.File) error {
	var ov overlapped
	r1, _, e := procUnlockFile.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ov)))
	if r1 == 0 {
		return e
	}
	return nil
}
