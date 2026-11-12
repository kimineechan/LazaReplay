//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"

	"lazareplay/internal/clock"
	"lazareplay/internal/testcard"
)

// The pattern window shows the test card on the real screen, at the place
// the generated source puts it, so both can be checked the same way.
const (
	patternX, patternY = testcard.X, testcard.Y
	patternW, patternH = testcard.Width, testcard.Height
)

var (
	user32            = syscall.NewLazyDLL("user32.dll")
	gdi32             = syscall.NewLazyDLL("gdi32.dll")
	dwmapi            = syscall.NewLazyDLL("dwmapi.dll")
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	setDpiContext     = user32.NewProc("SetProcessDpiAwarenessContext")
	registerClass     = user32.NewProc("RegisterClassExW")
	createWindow      = user32.NewProc("CreateWindowExW")
	defWindowProc     = user32.NewProc("DefWindowProcW")
	showWindow        = user32.NewProc("ShowWindow")
	peekMessage       = user32.NewProc("PeekMessageW")
	translateMessage  = user32.NewProc("TranslateMessage")
	dispatchMessage   = user32.NewProc("DispatchMessageW")
	destroyWindow     = user32.NewProc("DestroyWindow")
	getDC             = user32.NewProc("GetDC")
	releaseDC         = user32.NewProc("ReleaseDC")
	createCompatible  = gdi32.NewProc("CreateCompatibleDC")
	createDIBSection  = gdi32.NewProc("CreateDIBSection")
	selectObject      = gdi32.NewProc("SelectObject")
	bitBlt            = gdi32.NewProc("BitBlt")
	dwmFlush          = dwmapi.NewProc("DwmFlush")
	getModuleHandle   = kernel32.NewProc("GetModuleHandleW")
	patternWindowProc = syscall.NewCallback(func(window, message, wparam, lparam uintptr) uintptr {
		r, _, _ := defWindowProc.Call(window, message, wparam, lparam)
		return r
	})
)

type windowClass struct {
	Size       uint32
	Style      uint32
	Proc       uintptr
	ClassExtra int32
	WindowExt  int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	Menu       *uint16
	Name       *uint16
	SmallIcon  uintptr
}

type bitmapHeader struct {
	Size          uint32
	Width, Height int32
	Planes, Bits  uint16
	Compression   uint32
	SizeImage     uint32
	XPels, YPels  int32
	Used, Needed  uint32
}

// pattern shows the test pattern for the given number of seconds, redrawing
// once per refresh of the screen.
func pattern() {
	seconds := option("seconds", 10)
	setDpiContext.Call(^uintptr(3)) // per-monitor aware: one pixel is one pixel
	instance, _, _ := getModuleHandle.Call(0)
	name, _ := syscall.UTF16PtrFromString("LazaReplayTestPattern")
	class := windowClass{Proc: patternWindowProc, Instance: instance, Name: name}
	class.Size = uint32(unsafe.Sizeof(class))
	registerClass.Call(uintptr(unsafe.Pointer(&class)))
	const popup, topmost, toolWindow, noActivate = 0x80000000, 0x8, 0x80, 0x08000000
	window, _, _ := createWindow.Call(topmost|toolWindow|noActivate, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)),
		popup, patternX, patternY, patternW, patternH, 0, 0, instance, 0)
	if window == 0 {
		fail(fmt.Errorf("could not create the pattern window"))
	}
	showWindow.Call(window, 4) // show without taking the keyboard

	screen, _, _ := getDC.Call(window)
	memory, _, _ := createCompatible.Call(screen)
	header := bitmapHeader{Size: 40, Width: patternW, Height: -patternH, Planes: 1, Bits: 32}
	var bits unsafe.Pointer
	bitmap, _, _ := createDIBSection.Call(memory, uintptr(unsafe.Pointer(&header)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	selectObject.Call(memory, bitmap)
	pixels := unsafe.Slice((*byte)(bits), patternW*patternH*4)

	testcard.PaintAt(pixels, patternW*4, 0, 0, 0, true)
	end := clock.Now() + int64(seconds)*clock.Second
	frame := 0
	for ; clock.Now() < end; frame++ {
		var message [48]byte
		for {
			got, _, _ := peekMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0, 1)
			if got == 0 {
				break
			}
			translateMessage.Call(uintptr(unsafe.Pointer(&message)))
			dispatchMessage.Call(uintptr(unsafe.Pointer(&message)))
		}
		testcard.PaintAt(pixels, patternW*4, 0, 0, frame, false)
		bitBlt.Call(screen, 0, 0, patternW, patternH, memory, 0, 0, 0x00CC0020)
		dwmFlush.Call()
	}
	releaseDC.Call(window, screen)
	destroyWindow.Call(window)
	fmt.Printf("pattern: drew %d frames in %d seconds\n", frame, seconds)
}
