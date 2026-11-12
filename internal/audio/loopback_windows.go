//go:build windows

package audio

import (
	"errors"
	"unsafe"

	"lazareplay/internal/clock"
	"lazareplay/internal/com"
)

var (
	classEnumerator = com.ID("bcde0395-e52f-467c-8e3d-c4579291692e")
	iidEnumerator   = com.ID("a95664d2-9614-4f35-a746-de8db63617e6")
	iidAudioClient  = com.ID("1cb9ad4c-dbfa-4c32-b178-c2f568a703b2")
	iidCapture      = com.ID("c8adbd64-e71e-48a0-a4de-185c395cd317")
)

// Input supplies sound as 16-bit samples, channels interleaved.
type Input interface {
	Format() (rate, channels int)
	// Read returns the next packet that has arrived and the time its first
	// sample was captured, or nil when none is waiting. The slice is valid
	// until the next call.
	Read() (samples []int16, at int64, err error)
	Close()
}

// ErrDeviceChanged is returned by Read when the user has switched to
// another playback device, so that capture can be moved to it.
var ErrDeviceChanged = errors.New("the playback device changed")

// waveFormat mirrors WAVEFORMATEX.
type waveFormat struct {
	Tag         uint16
	Channels    uint16
	Rate        uint32
	BytesPerSec uint32
	BlockAlign  uint16
	Bits        uint16
	Extra       uint16
}

const (
	loopbackRate     = 48000
	loopbackChannels = 2
)

// loopback records what is being played on the default playback device.
// Windows calls this loopback capture: it is a copy of the mixed sound on
// its way to the speakers, so no microphone is involved.
type loopback struct {
	enumerator unsafe.Pointer // IMMDeviceEnumerator
	client     unsafe.Pointer // IAudioClient
	capture    unsafe.Pointer // IAudioCaptureClient
	id         string         // which device this is
	checked    int64          // when the default device was last compared
	samples    []int16
}

// Loopback starts capturing what the computer plays, as 48 kHz stereo.
// Windows converts from whatever format the device itself uses.
func Loopback() (Input, error) {
	l := &loopback{checked: clock.Now()}
	var err error
	if l.enumerator, err = com.Create(&classEnumerator, &iidEnumerator); err != nil {
		return nil, err
	}
	device, id, err := l.playback()
	if err != nil {
		l.Close()
		return nil, err
	}
	defer com.Release(&device)
	l.id = id
	hr := com.Call(device, 3, uintptr(unsafe.Pointer(&iidAudioClient)), 23, 0, uintptr(unsafe.Pointer(&l.client)))
	if err := com.Check("IMMDevice.Activate", hr); err != nil {
		l.Close()
		return nil, err
	}
	format := waveFormat{Tag: 1, Channels: loopbackChannels, Rate: loopbackRate, BytesPerSec: loopbackRate * loopbackChannels * 2, BlockAlign: loopbackChannels * 2, Bits: 16}
	const loop, convert, resample = 0x00020000, 0x80000000, 0x08000000
	hr = com.Call(l.client, 3, 0, loop|convert|resample, clock.Second, 0, uintptr(unsafe.Pointer(&format)), 0)
	if err := com.Check("IAudioClient.Initialize", hr); err != nil {
		l.Close()
		return nil, err
	}
	hr = com.Call(l.client, 14, uintptr(unsafe.Pointer(&iidCapture)), uintptr(unsafe.Pointer(&l.capture)))
	if err := com.Check("IAudioClient.GetService", hr); err != nil {
		l.Close()
		return nil, err
	}
	if err := com.Check("IAudioClient.Start", com.Call(l.client, 10)); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// playback returns the default playback device and its ID.
func (l *loopback) playback() (device unsafe.Pointer, id string, err error) {
	hr := com.Call(l.enumerator, 4, 0, 0, uintptr(unsafe.Pointer(&device))) // render, console role
	if err := com.Check("GetDefaultAudioEndpoint", hr); err != nil {
		return nil, "", err
	}
	var text *uint16
	if int32(com.Call(device, 5, uintptr(unsafe.Pointer(&text)))) >= 0 && text != nil {
		id = com.String(text)
		com.Free(unsafe.Pointer(text))
	}
	return device, id, nil
}

func (l *loopback) Format() (rate, channels int) { return loopbackRate, loopbackChannels }

func (l *loopback) Read() ([]int16, int64, error) {
	var frames uint32
	if err := com.Check("GetNextPacketSize", com.Call(l.capture, 5, uintptr(unsafe.Pointer(&frames)))); err != nil {
		return nil, 0, err
	}
	if frames == 0 {
		// Nothing is playing. Every two seconds, see whether the user has
		// switched to another device, where sound may be playing instead.
		if now := clock.Now(); now-l.checked > 2*clock.Second {
			l.checked = now
			device, id, err := l.playback()
			if err != nil {
				return nil, 0, err
			}
			com.Release(&device)
			if id != l.id {
				return nil, 0, ErrDeviceChanged
			}
		}
		return nil, 0, nil
	}
	var data *byte
	var flags uint32
	var position, counter uint64
	hr := com.Call(l.capture, 3, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)), uintptr(unsafe.Pointer(&flags)),
		uintptr(unsafe.Pointer(&position)), uintptr(unsafe.Pointer(&counter)))
	if err := com.Check("IAudioCaptureClient.GetBuffer", hr); err != nil {
		return nil, 0, err
	}
	n := int(frames) * loopbackChannels
	if cap(l.samples) < n {
		l.samples = make([]int16, n)
	}
	out := l.samples[:n]
	if flags&2 != 0 || data == nil { // Windows marks the packet as silence
		clear(out)
	} else {
		copy(out, unsafe.Slice((*int16)(unsafe.Pointer(data)), n))
	}
	com.Call(l.capture, 4, uintptr(frames)) // ReleaseBuffer
	at := int64(counter)
	if flags&4 != 0 { // Windows could not time this packet
		at = 0
	}
	return out, at, nil
}

func (l *loopback) Close() {
	if l.client != nil {
		com.Call(l.client, 11) // Stop
	}
	com.Release(&l.capture)
	com.Release(&l.client)
	com.Release(&l.enumerator)
}
