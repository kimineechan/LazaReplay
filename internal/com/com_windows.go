//go:build windows

// Package com holds the few helpers needed to call Windows COM interfaces
// directly, without a binding generator or third-party module.
package com

import (
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// GUID identifies an interface, a class or a property.
type GUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// ID parses a GUID written as "770aae78-f26f-4dba-a829-253c83d1b387".
func ID(s string) GUID {
	hex := strings.ReplaceAll(s, "-", "")
	if len(hex) != 32 {
		panic("com: bad GUID " + s)
	}
	part := func(from, to int) uint64 {
		v, err := strconv.ParseUint(hex[from:to], 16, 64)
		if err != nil {
			panic("com: bad GUID " + s)
		}
		return v
	}
	g := GUID{Data1: uint32(part(0, 8)), Data2: uint16(part(8, 12)), Data3: uint16(part(12, 16))}
	for i := range g.Data4 {
		g.Data4[i] = byte(part(16+2*i, 18+2*i))
	}
	return g
}

// Format builds the GUID of a media format from its four-character code or number.
func Format(code uint32) GUID {
	return GUID{code, 0x0000, 0x0010, [8]byte{0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}}
}

// FourCC packs four characters the way media formats are numbered.
func FourCC(s string) uint32 {
	return uint32(s[0]) | uint32(s[1])<<8 | uint32(s[2])<<16 | uint32(s[3])<<24
}

// Error is a failed HRESULT with the name of the call that returned it.
type Error struct {
	Op   string
	Code uint32
}

func (e *Error) Error() string { return fmt.Sprintf("%s failed (0x%08X)", e.Op, e.Code) }

// Is reports whether err is the given HRESULT.
func Is(err error, code uint32) bool {
	e, ok := err.(*Error)
	return ok && e.Code == code
}

// Check turns an HRESULT into an error. Success codes give nil.
func Check(op string, hr uintptr) error {
	if int32(hr) < 0 {
		return &Error{op, uint32(hr)}
	}
	return nil
}

// Code returns the HRESULT inside err, or 0 when err is not a COM failure.
func Code(err error) uint32 {
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return 0
}

// Call invokes the method at the given position in an object's method table.
// Pointers converted to uintptr inside the argument list are kept alive and
// in place for the length of the call.
//
//go:uintptrescapes
func Call(object unsafe.Pointer, method int, args ...uintptr) uintptr {
	table := *(**[128]uintptr)(object)
	all := make([]uintptr, 0, 8)
	all = append(all, uintptr(object))
	all = append(all, args...)
	r, _, _ := syscall.SyscallN(table[method], all...)
	return r
}

// Release drops one reference and clears the pointer.
func Release(object *unsafe.Pointer) {
	if *object != nil {
		Call(*object, 2)
		*object = nil
	}
}

// Query asks an object for another of its interfaces.
func Query(object unsafe.Pointer, iid *GUID) (unsafe.Pointer, error) {
	var out unsafe.Pointer
	hr := Call(object, 0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	return out, Check("QueryInterface", hr)
}

var (
	ole32          = syscall.NewLazyDLL("ole32.dll")
	coInitializeEx = ole32.NewProc("CoInitializeEx")
	coTaskMemFree  = ole32.NewProc("CoTaskMemFree")
	coCreate       = ole32.NewProc("CoCreateInstance")
)

// Init prepares the calling thread for COM. Every thread that talks to
// Media Foundation or audio devices calls it once.
func Init() {
	coInitializeEx.Call(0, 0) // COINIT_MULTITHREADED
}

// Free releases memory that a COM call allocated for the caller.
func Free(p unsafe.Pointer) {
	if p != nil {
		coTaskMemFree.Call(uintptr(p))
	}
}

// Create makes an object of a registered class.
func Create(class, iid *GUID) (unsafe.Pointer, error) {
	var out unsafe.Pointer
	hr, _, _ := coCreate.Call(uintptr(unsafe.Pointer(class)), 0, 1|4, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	return out, Check("CoCreateInstance", hr)
}

// Text converts a NUL-terminated UTF-16 buffer to a string.
func Text(units []uint16) string { return syscall.UTF16ToString(units) }

// Wide converts a string to a NUL-terminated UTF-16 pointer.
func Wide(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}
