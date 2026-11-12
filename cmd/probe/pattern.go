//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"

	"lazareplay/internal/clock"
)

// The test pattern is a small window with known contents: eight flat colour
// bars, a frame number drawn as sixteen black or white squares, and a square
// that moves every frame. A recording of it can be checked by numbers alone.
const (
	patternX, patternY = 40, 40
	patternW, patternH = 640, 360
	barsH              = 240
	bitY, bitSize      = 252, 20
	bitX, bitStep      = 20, 24
)

var patternBars = [8][3]byte{
	{255, 255, 255}, {255, 255, 0}, {0, 255, 255}, {0, 255, 0},
	{255, 0, 255}, {255, 0, 0}, {0, 0, 255}, {0, 0, 0},
}

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

	fill := func(x0, y0, x1, y1 int, r, g, b byte) {
		for y := y0; y < y1; y++ {
			row := pixels[(y*patternW+x0)*4 : (y*patternW+x1)*4]
			for i := 0; i < len(row); i += 4 {
				row[i], row[i+1], row[i+2], row[i+3] = b, g, r, 255
			}
		}
	}
	for i, c := range patternBars {
		fill(i*patternW/8, 0, (i+1)*patternW/8, barsH, c[0], c[1], c[2])
	}
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
		fill(0, barsH, patternW, patternH, 128, 128, 128)
		for bit := 0; bit < 16; bit++ {
			level := byte(0)
			if frame>>bit&1 == 1 {
				level = 255
			}
			x := bitX + bit*bitStep
			fill(x, bitY, x+bitSize, bitY+bitSize, level, level, level)
		}
		x := frame * 4 % (patternW - 40)
		fill(x, 300, x+40, 340, 255, 255, 255)
		bitBlt.Call(screen, 0, 0, patternW, patternH, memory, 0, 0, 0x00CC0020)
		dwmFlush.Call()
	}
	releaseDC.Call(window, screen)
	destroyWindow.Call(window)
	fmt.Printf("pattern: drew %d frames in %d seconds\n", frame, seconds)
}
