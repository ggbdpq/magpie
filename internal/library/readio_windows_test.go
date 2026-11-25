//go:build windows

package library

import (
	"syscall"
	"testing"
	uns "unsafe"
)

// ioCounters is what Windows keeps of a process's reads and writes
// (GetProcessIoCounters), as the task manager shows it.
type ioCounters struct {
	ReadOperationCount, WriteOperationCount, OtherOperationCount uint64
	ReadTransferCount, WriteTransferCount, OtherTransferCount    uint64
}

var procGetProcessIoCounters = syscall.NewLazyDLL("kernel32.dll").NewProc("GetProcessIoCounters")

// processReadBytes is how many bytes this process has read so far, as
// Windows counts it.
func processReadBytes(t *testing.T) uint64 {
	t.Helper()
	var io ioCounters
	r, _, err := procGetProcessIoCounters.Call(^uintptr(0), uintptr(uns.Pointer(&io)))
	if r == 0 {
		t.Fatalf("GetProcessIoCounters: %v", err)
	}
	return io.ReadTransferCount
}
