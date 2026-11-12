//go:build windows

package mf

import (
	"errors"
	"unsafe"

	"lazareplay/internal/com"
)

// AAC compresses sound with the AAC encoder built into Windows. It takes
// 16-bit samples and produces one compressed block for every 1024 samples
// per channel.
type AAC struct {
	transform unsafe.Pointer // IMFTransform
	output    unsafe.Pointer // sample the encoder fills
	rate      int
	channels  int
}

// NewAAC starts an encoder. bitrate is in bits per second; Windows offers
// 96000, 128000, 160000 and 192000.
func NewAAC(rate, channels, bitrate int) (*AAC, error) {
	wanted := typeInfo{TypeAudio, FormatAAC}
	var list *unsafe.Pointer
	var count uint32
	hr, _, _ := mfEnumEx.Call(uintptr(unsafe.Pointer(&categoryAudioEncoder)), 0x1|0x2|0x40, 0, uintptr(unsafe.Pointer(&wanted)),
		uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&count)))
	if err := com.Check("MFTEnumEx", hr); err != nil {
		return nil, err
	}
	a := &AAC{rate: rate, channels: channels}
	for _, activate := range unsafe.Slice(list, count) {
		if a.transform == nil {
			com.Call(activate, 33, uintptr(unsafe.Pointer(&iidTransform)), uintptr(unsafe.Pointer(&a.transform)))
		}
		com.Release(&activate)
	}
	com.Free(unsafe.Pointer(list))
	if a.transform == nil {
		return nil, errors.New("Windows has no AAC encoder")
	}
	if err := a.configure(bitrate); err != nil {
		a.Close()
		return nil, err
	}
	return a, nil
}

func (a *AAC) configure(bitrate int) error {
	input, err := NewMediaType()
	if err != nil {
		return err
	}
	defer input.Release()
	input.SetGUID(&MajorType, &TypeAudio)
	input.SetGUID(&Subtype, &FormatPCM)
	input.SetUINT32(&AudioBits, 16)
	input.SetUINT32(&AudioSampleRate, uint32(a.rate))
	input.SetUINT32(&AudioChannels, uint32(a.channels))
	input.SetUINT32(&AudioBlockAlign, uint32(2*a.channels))
	input.SetUINT32(&AudioBytesPerSec, uint32(2*a.channels*a.rate))

	output, err := NewMediaType()
	if err != nil {
		return err
	}
	defer output.Release()
	output.SetGUID(&MajorType, &TypeAudio)
	output.SetGUID(&Subtype, &FormatAAC)
	output.SetUINT32(&AudioBits, 16)
	output.SetUINT32(&AudioSampleRate, uint32(a.rate))
	output.SetUINT32(&AudioChannels, uint32(a.channels))
	output.SetUINT32(&AudioBytesPerSec, uint32(bitrate/8))
	output.SetUINT32(&AACPayloadType, 0)     // bare blocks, no extra headers
	output.SetUINT32(&AACProfileLevel, 0x29) // AAC-LC

	if err := com.Check("SetInputType", com.Call(a.transform, 15, 0, uintptr(input.P), 0)); err != nil {
		return err
	}
	if err := com.Check("SetOutputType", com.Call(a.transform, 16, 0, uintptr(output.P), 0)); err != nil {
		return err
	}
	var info struct{ Flags, Size, Alignment uint32 }
	com.Call(a.transform, 7, 0, uintptr(unsafe.Pointer(&info)))
	if a.output, err = newMemorySample(max(info.Size, 8192)); err != nil {
		return err
	}
	com.Call(a.transform, 23, messageBeginStreaming, 0)
	com.Call(a.transform, 23, messageStartOfStream, 0)
	return nil
}

// Encode compresses a run of samples (left and right alternating for
// stereo) that begins at the given time, in 100 ns units, and passes each
// finished block to emit. The encoder keeps what does not yet fill a block.
// The bytes given to emit are valid only during the call.
func (a *AAC) Encode(samples []int16, time int64, emit func(block []byte)) error {
	if len(samples) == 0 {
		return nil
	}
	size := uint32(len(samples) * 2)
	input, err := newMemorySample(size)
	if err != nil {
		return err
	}
	defer com.Release(&input)
	var buffer unsafe.Pointer
	if err := com.Check("GetBufferByIndex", com.Call(input, 40, 0, uintptr(unsafe.Pointer(&buffer)))); err != nil {
		return err
	}
	var memory *byte
	hr := com.Call(buffer, 3, uintptr(unsafe.Pointer(&memory)), 0, 0)
	if err := com.Check("Lock", hr); err != nil {
		com.Release(&buffer)
		return err
	}
	copy(unsafe.Slice((*int16)(unsafe.Pointer(memory)), len(samples)), samples)
	com.Call(buffer, 4)
	com.Call(buffer, 6, uintptr(size))
	com.Release(&buffer)
	// The encoder refuses sound that does not say when it is from.
	com.Call(input, 36, uintptr(time))
	com.Call(input, 38, uintptr(int64(len(samples)/a.channels)*10_000_000/int64(a.rate)))

	for attempt := 0; ; attempt++ {
		hr = com.Call(a.transform, 24, 0, uintptr(input), 0)
		if uint32(hr) != errNotAccepting || attempt > 0 {
			break
		}
		if err := a.drain(emit); err != nil { // make room, then offer it again
			return err
		}
	}
	if err := com.Check("ProcessInput", hr); err != nil {
		return err
	}
	return a.drain(emit)
}

// drain passes on every block the encoder has finished.
func (a *AAC) drain(emit func([]byte)) error {
	for {
		out := outputBuffer{Sample: a.output}
		var status uint32
		hr := com.Call(a.transform, 25, 0, 1, uintptr(unsafe.Pointer(&out)), uintptr(unsafe.Pointer(&status)))
		com.Release(&out.Events)
		if uint32(hr) == errNeedMoreInput {
			return nil
		}
		if err := com.Check("ProcessOutput", hr); err != nil {
			return err
		}
		var buffer unsafe.Pointer
		if int32(com.Call(a.output, 40, 0, uintptr(unsafe.Pointer(&buffer)))) < 0 {
			continue
		}
		var memory *byte
		var limit, length uint32
		if int32(com.Call(buffer, 3, uintptr(unsafe.Pointer(&memory)), uintptr(unsafe.Pointer(&limit)), uintptr(unsafe.Pointer(&length)))) >= 0 {
			if length > 0 {
				emit(unsafe.Slice(memory, length))
			}
			com.Call(buffer, 4)
		}
		com.Call(buffer, 6, 0)
		com.Release(&buffer)
	}
}

// Close frees the encoder.
func (a *AAC) Close() {
	com.Release(&a.output)
	com.Release(&a.transform)
}
