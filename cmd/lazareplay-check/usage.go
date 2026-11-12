//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// usage returns the processor time this process has used, in seconds, and
// the most memory it has held at once, in bytes.
func usage() (seconds float64, peak uint64) {
	kernel := syscall.NewLazyDLL("kernel32.dll")
	process, _, _ := kernel.NewProc("GetCurrentProcess").Call()
	var created, exited, inKernel, inUser uint64
	kernel.NewProc("GetProcessTimes").Call(process, uintptr(unsafe.Pointer(&created)), uintptr(unsafe.Pointer(&exited)),
		uintptr(unsafe.Pointer(&inKernel)), uintptr(unsafe.Pointer(&inUser)))
	// PROCESS_MEMORY_COUNTERS: a size, a fault count, then the peak and
	// current working set and six more sizes.
	var counters [10]uint64
	*(*uint32)(unsafe.Pointer(&counters[0])) = uint32(unsafe.Sizeof(counters))
	if proc := kernel.NewProc("K32GetProcessMemoryInfo"); proc.Find() == nil {
		proc.Call(process, uintptr(unsafe.Pointer(&counters[0])), unsafe.Sizeof(counters))
	}
	return float64(inKernel+inUser) / 1e7, counters[1]
}
