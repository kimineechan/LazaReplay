//go:build windows

package audio

import (
	"errors"
	"runtime"
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
// another default device, so that capture can be moved to it.
var ErrDeviceChanged = errors.New("the sound device changed")

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

// Everything is captured as 48 kHz stereo, so that what the computer plays
// and a microphone can be added together sample by sample.
const (
	captureRate     = 48000
	captureChannels = 2
)

// The two directions sound flows in, as Windows numbers them.
const (
	playing   = 0 // towards the speakers
	recording = 1 // from a microphone
)

// endpoint records from one sound device: either a copy of what is being
// played on a playback device, or a microphone.
type endpoint struct {
	enumerator unsafe.Pointer // IMMDeviceEnumerator
	client     unsafe.Pointer // IAudioClient
	capture    unsafe.Pointer // IAudioCaptureClient
	flow       uintptr        // playing or recording
	follows    bool           // it is whichever device is the default, and moves when that changes
	id         string         // which device this is
	checked    int64          // when the default device was last compared
	samples    []int16
}

// Loopback starts capturing what the computer plays. Windows calls this
// loopback capture: it is a copy of the mixed sound on its way to the
// speakers of the default playback device, so no microphone is involved.
func Loopback() (Input, error) { return open(playing, "") }

// Microphone starts capturing from a microphone. id is the device's ID as
// Microphones gives it; "" is whichever microphone Windows has as its
// default, and capture moves if the default changes.
func Microphone(id string) (Input, error) { return open(recording, id) }

// open starts capture, as 48 kHz stereo. Windows converts from whatever
// format the device itself uses.
func open(flow uintptr, id string) (Input, error) {
	e := &endpoint{flow: flow, follows: id == "", checked: clock.Now()}
	var err error
	if e.enumerator, err = com.Create(&classEnumerator, &iidEnumerator); err != nil {
		return nil, err
	}
	var device unsafe.Pointer
	if id == "" {
		device, e.id, err = e.standard()
	} else {
		e.id = id
		hr := com.Call(e.enumerator, 5, uintptr(unsafe.Pointer(com.Wide(id))), uintptr(unsafe.Pointer(&device)))
		err = com.Check("IMMDeviceEnumerator.GetDevice", hr)
	}
	if err != nil {
		e.Close()
		return nil, err
	}
	defer com.Release(&device)
	hr := com.Call(device, 3, uintptr(unsafe.Pointer(&iidAudioClient)), 23, 0, uintptr(unsafe.Pointer(&e.client)))
	if err := com.Check("IMMDevice.Activate", hr); err != nil {
		e.Close()
		return nil, err
	}
	format := waveFormat{Tag: 1, Channels: captureChannels, Rate: captureRate, BytesPerSec: captureRate * captureChannels * 2, BlockAlign: captureChannels * 2, Bits: 16}
	const loop, convert, resample = 0x00020000, 0x80000000, 0x08000000
	flags := uintptr(convert | resample)
	if flow == playing {
		flags |= loop
	}
	hr = com.Call(e.client, 3, 0, flags, clock.Second, 0, uintptr(unsafe.Pointer(&format)), 0)
	if err := com.Check("IAudioClient.Initialize", hr); err != nil {
		e.Close()
		return nil, err
	}
	hr = com.Call(e.client, 14, uintptr(unsafe.Pointer(&iidCapture)), uintptr(unsafe.Pointer(&e.capture)))
	if err := com.Check("IAudioClient.GetService", hr); err != nil {
		e.Close()
		return nil, err
	}
	if err := com.Check("IAudioClient.Start", com.Call(e.client, 10)); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

// standard returns the default device for this direction and its ID.
func (e *endpoint) standard() (device unsafe.Pointer, id string, err error) {
	hr := com.Call(e.enumerator, 4, e.flow, 0, uintptr(unsafe.Pointer(&device))) // console role
	if err := com.Check("GetDefaultAudioEndpoint", hr); err != nil {
		return nil, "", err
	}
	return device, deviceID(device), nil
}

func deviceID(device unsafe.Pointer) string {
	var text *uint16
	if int32(com.Call(device, 5, uintptr(unsafe.Pointer(&text)))) < 0 || text == nil {
		return ""
	}
	defer com.Free(unsafe.Pointer(text))
	return com.String(text)
}

func (e *endpoint) Format() (rate, channels int) { return captureRate, captureChannels }

func (e *endpoint) Read() ([]int16, int64, error) {
	// Every two seconds, see whether the user has switched to another
	// default device. A playback device is only looked at while it is
	// silent, since sound may be playing on the new one instead.
	var frames uint32
	if err := com.Check("GetNextPacketSize", com.Call(e.capture, 5, uintptr(unsafe.Pointer(&frames)))); err != nil {
		return nil, 0, err
	}
	if now := clock.Now(); e.follows && (frames == 0 || e.flow == recording) && now-e.checked > 2*clock.Second {
		e.checked = now
		device, id, err := e.standard()
		if err != nil {
			return nil, 0, err
		}
		com.Release(&device)
		if id != e.id {
			return nil, 0, ErrDeviceChanged
		}
	}
	if frames == 0 {
		return nil, 0, nil
	}
	var data *byte
	var flags uint32
	var position, counter uint64
	hr := com.Call(e.capture, 3, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)), uintptr(unsafe.Pointer(&flags)),
		uintptr(unsafe.Pointer(&position)), uintptr(unsafe.Pointer(&counter)))
	if err := com.Check("IAudioCaptureClient.GetBuffer", hr); err != nil {
		return nil, 0, err
	}
	n := int(frames) * captureChannels
	if cap(e.samples) < n {
		e.samples = make([]int16, n)
	}
	out := e.samples[:n]
	if flags&2 != 0 || data == nil { // Windows marks the packet as silence
		clear(out)
	} else {
		copy(out, unsafe.Slice((*int16)(unsafe.Pointer(data)), n))
	}
	com.Call(e.capture, 4, uintptr(frames)) // ReleaseBuffer
	at := int64(counter)
	if flags&4 != 0 { // Windows could not time this packet
		at = 0
	}
	return out, at, nil
}

func (e *endpoint) Close() {
	if e.client != nil {
		com.Call(e.client, 11) // Stop
	}
	com.Release(&e.capture)
	com.Release(&e.client)
	com.Release(&e.enumerator)
}

// Device is a microphone that can be recorded from.
type Device struct {
	ID   string // Windows' own name for it, as Microphone takes it
	Name string // the name Windows shows the user
}

// propertyKey mirrors PROPERTYKEY.
type propertyKey struct {
	Format com.GUID
	ID     uint32
}

// variant mirrors the part of PROPVARIANT that holds a string.
type variant struct {
	Kind uint16
	_    [3]uint16
	Text *uint16
	_    uintptr
}

// Microphones lists the microphones that are plugged in and switched on.
// It can be called from any thread.
func Microphones() []Device {
	found := make(chan []Device)
	// The listing needs a thread prepared for it, which the caller's may
	// not be, so it is done on one of its own.
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		com.Init()
		found <- microphones()
	}()
	return <-found
}

func microphones() []Device {
	enumerator, err := com.Create(&classEnumerator, &iidEnumerator)
	if err != nil {
		return nil
	}
	defer com.Release(&enumerator)
	const active = 1
	var collection unsafe.Pointer
	if int32(com.Call(enumerator, 3, recording, active, uintptr(unsafe.Pointer(&collection)))) < 0 {
		return nil
	}
	defer com.Release(&collection)
	var count uint32
	com.Call(collection, 3, uintptr(unsafe.Pointer(&count)))
	var list []Device
	name := propertyKey{com.ID("a45c254e-df1c-4efd-8020-67d146a850e0"), 14} // the friendly name of a device
	for i := uint32(0); i < count; i++ {
		var device unsafe.Pointer
		if int32(com.Call(collection, 4, uintptr(i), uintptr(unsafe.Pointer(&device)))) < 0 {
			continue
		}
		d := Device{ID: deviceID(device)}
		var store unsafe.Pointer
		if int32(com.Call(device, 4, 0, uintptr(unsafe.Pointer(&store)))) >= 0 { // open its properties for reading
			var value variant
			const text = 31
			if int32(com.Call(store, 5, uintptr(unsafe.Pointer(&name)), uintptr(unsafe.Pointer(&value)))) >= 0 && value.Kind == text {
				d.Name = com.String(value.Text)
				com.Free(unsafe.Pointer(value.Text))
			}
			com.Release(&store)
		}
		com.Release(&device)
		if d.Name == "" {
			d.Name = "Microphone"
		}
		if d.ID != "" {
			list = append(list, d)
		}
	}
	return list
}
