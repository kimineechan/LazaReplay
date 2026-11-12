//go:build windows

package autostart

import (
	"syscall"
	"testing"
	"unsafe"
)

// The test uses a key of its own, never the real start-up list, and removes
// it afterwards.
const testKey = `Software\LazaReplayTest`

func TestWriteReadRemove(t *testing.T) {
	defer func() {
		path, _ := syscall.UTF16PtrFromString(testKey)
		advapi32.NewProc("RegDeleteKeyW").Call(currentUser, uintptr(unsafe.Pointer(path)))
	}()
	if got := read(testKey, "Sample"); got != "" {
		t.Fatalf("an unset value reads as %q", got)
	}
	command := `"C:\Program Files\Sample App\Sample.exe" --tray`
	if err := write(testKey, "Sample", command); err != nil {
		t.Fatal(err)
	}
	if got := read(testKey, "Sample"); got != command {
		t.Fatalf("read back %q, want %q", got, command)
	}
	if err := remove(testKey, "Sample"); err != nil {
		t.Fatal(err)
	}
	if got := read(testKey, "Sample"); got != "" {
		t.Fatalf("after removal the value reads as %q", got)
	}
	if err := remove(testKey, "Sample"); err != nil {
		t.Fatalf("removing a value that is not there failed: %v", err)
	}
}
