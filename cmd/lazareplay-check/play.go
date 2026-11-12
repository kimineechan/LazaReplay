//go:build windows

package main

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// playTone plays two seconds of a very quiet 1000 Hz tone through the
// default playback device, so that capture of what the computer plays can
// be checked against a known sound. It is about a two-hundredth of full
// volume.
func playTone() {
	const rate, seconds, level = 48000, 2, 160
	data := make([]byte, 0, rate*seconds*4)
	for i := 0; i < rate*seconds; i++ {
		v := int16(level * math.Sin(2*math.Pi*1000*float64(i)/rate))
		data = binary.LittleEndian.AppendUint16(data, uint16(v))
		data = binary.LittleEndian.AppendUint16(data, uint16(v))
	}
	file := []byte("RIFF")
	file = binary.LittleEndian.AppendUint32(file, uint32(36+len(data)))
	file = append(file, "WAVEfmt "...)
	file = binary.LittleEndian.AppendUint32(file, 16)
	file = binary.LittleEndian.AppendUint16(file, 1) // plain samples
	file = binary.LittleEndian.AppendUint16(file, 2) // stereo
	file = binary.LittleEndian.AppendUint32(file, rate)
	file = binary.LittleEndian.AppendUint32(file, rate*4)
	file = binary.LittleEndian.AppendUint16(file, 4)
	file = binary.LittleEndian.AppendUint16(file, 16)
	file = append(file, "data"...)
	file = binary.LittleEndian.AppendUint32(file, uint32(len(data)))
	file = append(file, data...)
	path := filepath.Join(os.TempDir(), "lazareplay-test-tone.wav")
	if err := os.WriteFile(path, file, 0600); err != nil {
		fail(err)
	}
	name, _ := syscall.UTF16PtrFromString(path)
	const async, noDefault, fromFile = 0x1, 0x2, 0x20000
	syscall.NewLazyDLL("winmm.dll").NewProc("PlaySoundW").Call(uintptr(unsafe.Pointer(name)), 0, async|noDefault|fromFile)
}
