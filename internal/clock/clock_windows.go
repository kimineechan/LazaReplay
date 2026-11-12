//go:build windows

// Package clock gives the recorder one time base and a precise frame timer.
// Every timestamp in the program is a reading of the Windows performance
// counter in units of 100 nanoseconds, the unit Windows media APIs use.
package clock

import (
	"syscall"
	"unsafe"
)

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	winmm           = syscall.NewLazyDLL("winmm.dll")
	queryCounter    = kernel32.NewProc("QueryPerformanceCounter")
	queryFrequency  = kernel32.NewProc("QueryPerformanceFrequency")
	createTimer     = kernel32.NewProc("CreateWaitableTimerExW")
	setTimer        = kernel32.NewProc("SetWaitableTimer")
	waitForObject   = kernel32.NewProc("WaitForSingleObject")
	closeHandle     = kernel32.NewProc("CloseHandle")
	timeBeginPeriod = winmm.NewProc("timeBeginPeriod")

	frequency = func() int64 {
		var f int64
		queryFrequency.Call(uintptr(unsafe.Pointer(&f)))
		if f <= 0 {
			f = Second
		}
		return f
	}()
)

// Second is one second in 100-nanosecond units.
const Second = 10_000_000

// FromCounter converts a raw performance counter reading to 100 ns units.
func FromCounter(c int64) int64 {
	return c/frequency*Second + c%frequency*Second/frequency
}

// Now returns the current time.
func Now() int64 {
	var c int64
	queryCounter.Call(uintptr(unsafe.Pointer(&c)))
	return FromCounter(c)
}

// Ticker wakes its caller on an even grid of times. It uses the
// high-resolution timer of Windows 10, so a 60 per second grid is kept to
// well under a millisecond without raising the system timer rate.
type Ticker struct {
	handle   uintptr
	next     int64
	interval int64
}

// NewTicker starts a grid with the given spacing, beginning now.
func NewTicker(interval int64) *Ticker {
	const highResolution, access = 0x2, 0x1F0003
	h, _, _ := createTimer.Call(0, 0, highResolution, access)
	if h == 0 {
		// Before Windows 10 1803: an ordinary timer at 1 ms granularity.
		timeBeginPeriod.Call(1)
		h, _, _ = createTimer.Call(0, 0, 0, access)
	}
	return &Ticker{handle: h, next: Now(), interval: interval}
}

// Wait sleeps until the next point of the grid and returns its time. When
// the caller has fallen behind, the points it missed are skipped and their
// number returned.
func (t *Ticker) Wait() (tick int64, skipped int) {
	t.next += t.interval
	now := Now()
	if now > t.next {
		behind := (now - t.next) / t.interval
		t.next += behind * t.interval
		return t.next, int(behind)
	}
	due := now - t.next // negative: relative to now
	if t.handle == 0 {
		return t.next, 0
	}
	setTimer.Call(t.handle, uintptr(unsafe.Pointer(&due)), 0, 0, 0, 0)
	waitForObject.Call(t.handle, 0xFFFFFFFF)
	return t.next, 0
}

// Close releases the timer.
func (t *Ticker) Close() {
	if t.handle != 0 {
		closeHandle.Call(t.handle)
		t.handle = 0
	}
}
