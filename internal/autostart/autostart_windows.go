//go:build windows

// Package autostart makes the program start when the user signs in to
// Windows, or stops it doing so. It uses the per-user list that Windows
// itself shows under Task Manager, Start-up apps, so the user can see and
// undo it there. No administrator rights are involved.
package autostart

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	advapi32    = syscall.NewLazyDLL("advapi32.dll")
	createKey   = advapi32.NewProc("RegCreateKeyExW")
	closeKey    = advapi32.NewProc("RegCloseKey")
	setValue    = advapi32.NewProc("RegSetValueExW")
	queryValue  = advapi32.NewProc("RegQueryValueExW")
	deleteValue = advapi32.NewProc("RegDeleteValueW")
)

const (
	currentUser = 0x80000001
	runKey      = `Software\Microsoft\Windows\CurrentVersion\Run`
)

// Command returns the command stored for the program, or "" when it does
// not start with Windows.
func Command(name string) string { return read(runKey, name) }

// Enable makes the command run when the user signs in.
func Enable(name, command string) error { return write(runKey, name, command) }

// Disable stops the program starting with Windows.
func Disable(name string) error { return remove(runKey, name) }

func open(key string) (uintptr, error) {
	path, err := syscall.UTF16PtrFromString(key)
	if err != nil {
		return 0, err
	}
	var handle uintptr
	const query, set = 0x1, 0x2
	if r, _, _ := createKey.Call(currentUser, uintptr(unsafe.Pointer(path)), 0, 0, 0, query|set, 0, uintptr(unsafe.Pointer(&handle)), 0); r != 0 {
		return 0, fmt.Errorf("could not open the start-up list (Windows error %d)", r)
	}
	return handle, nil
}

func read(key, name string) string {
	handle, err := open(key)
	if err != nil {
		return ""
	}
	defer closeKey.Call(handle)
	value, _ := syscall.UTF16PtrFromString(name)
	buffer := make([]uint16, 2048)
	size, kind := uint32(len(buffer)*2), uint32(0)
	r, _, _ := queryValue.Call(handle, uintptr(unsafe.Pointer(value)), 0, uintptr(unsafe.Pointer(&kind)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
	if r != 0 || kind != 1 {
		return ""
	}
	return syscall.UTF16ToString(buffer[:size/2])
}

func write(key, name, command string) error {
	handle, err := open(key)
	if err != nil {
		return err
	}
	defer closeKey.Call(handle)
	value, _ := syscall.UTF16PtrFromString(name)
	data, err := syscall.UTF16FromString(command)
	if err != nil {
		return err
	}
	if r, _, _ := setValue.Call(handle, uintptr(unsafe.Pointer(value)), 0, 1, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)*2)); r != 0 {
		return fmt.Errorf("could not add to the start-up list (Windows error %d)", r)
	}
	return nil
}

func remove(key, name string) error {
	handle, err := open(key)
	if err != nil {
		return err
	}
	defer closeKey.Call(handle)
	value, _ := syscall.UTF16PtrFromString(name)
	if r, _, _ := deleteValue.Call(handle, uintptr(unsafe.Pointer(value))); r != 0 && r != 2 { // 2: it was not there
		return fmt.Errorf("could not remove from the start-up list (Windows error %d)", r)
	}
	return nil
}
