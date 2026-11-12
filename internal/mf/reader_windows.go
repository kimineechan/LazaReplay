//go:build windows

package mf

import (
	"syscall"
	"unsafe"

	"lazareplay/internal/com"
)

var (
	mfreadwrite  = syscall.NewLazyDLL("mfreadwrite.dll")
	createReader = mfreadwrite.NewProc("MFCreateSourceReaderFromURL")

	defaultStride = com.ID("644b4e48-1e02-4516-b0eb-c01ca9d49ac6")
	fileDuration  = com.ID("6c990d33-bb8e-477a-8598-0d5d96fcd88a")
)

const (
	firstVideo  = 0xFFFFFFFC
	firstAudio  = 0xFFFFFFFD
	mediaSource = 0xFFFFFFFF
)

// Reader decodes a media file with the decoders built into Windows. The
// recorder never uses it; it is how the self-test checks that a saved clip
// plays and holds what it should.
type Reader struct {
	object unsafe.Pointer // IMFSourceReader
}

// OpenReader opens a file for decoding.
func OpenReader(path string) (*Reader, error) {
	r := &Reader{}
	hr, _, _ := createReader.Call(uintptr(unsafe.Pointer(com.Wide(path))), 0, uintptr(unsafe.Pointer(&r.object)))
	if err := com.Check("MFCreateSourceReaderFromURL", hr); err != nil {
		return nil, err
	}
	return r, nil
}

// Close releases the reader.
func (r *Reader) Close() { com.Release(&r.object) }

// Duration returns the length the file states for itself, in 100 ns units.
func (r *Reader) Duration() int64 {
	var value variant
	if int32(com.Call(r.object, 12, mediaSource, uintptr(unsafe.Pointer(&fileDuration)), uintptr(unsafe.Pointer(&value)))) < 0 {
		return 0
	}
	return int64(value.value)
}

// Video asks for pictures decoded to NV12: a plane of brightness values,
// then a half-size plane of colour pairs.
func (r *Reader) Video() error {
	return r.choose(firstVideo, &TypeVideo, &FormatNV12)
}

// Audio asks for sound decoded to plain 16-bit samples. It returns the
// sample rate and the number of channels.
func (r *Reader) Audio() (rate, channels int, err error) {
	if err = r.choose(firstAudio, &TypeAudio, &FormatPCM); err != nil {
		return 0, 0, err
	}
	var current Attributes
	if err = com.Check("GetCurrentMediaType", com.Call(r.object, 6, firstAudio, uintptr(unsafe.Pointer(&current.P)))); err != nil {
		return 0, 0, err
	}
	defer current.Release()
	sampleRate, _ := current.UINT32(&AudioSampleRate)
	count, _ := current.UINT32(&AudioChannels)
	return int(sampleRate), int(count), nil
}

func (r *Reader) choose(stream uintptr, major, format *com.GUID) error {
	wanted, err := NewMediaType()
	if err != nil {
		return err
	}
	defer wanted.Release()
	wanted.SetGUID(&MajorType, major)
	wanted.SetGUID(&Subtype, format)
	return com.Check("SetCurrentMediaType", com.Call(r.object, 7, stream, 0, uintptr(wanted.P)))
}

// Layout returns the size of the decoded picture and the number of bytes
// per row. The height can be larger than the visible picture: H.264 codes
// whole blocks of 16 rows.
func (r *Reader) Layout() (width, height, stride int) {
	var current Attributes
	if int32(com.Call(r.object, 6, firstVideo, uintptr(unsafe.Pointer(&current.P)))) < 0 {
		return 0, 0, 0
	}
	defer current.Release()
	size, _ := current.UINT64(&FrameSize)
	width, height = int(size>>32), int(uint32(size))
	stride = width
	if s, ok := current.UINT32(&defaultStride); ok && int32(s) > 0 {
		stride = int(s)
	}
	return width, height, stride
}

// NextVideo returns the next picture and its time in 100 ns units. At the
// end of the file data is nil.
func (r *Reader) NextVideo() (data []byte, time int64, err error) {
	return r.next(firstVideo)
}

// NextAudio returns the next block of sound.
func (r *Reader) NextAudio() (data []byte, time int64, err error) {
	return r.next(firstAudio)
}

func (r *Reader) next(stream uintptr) ([]byte, int64, error) {
	for {
		var actual, flags uint32
		var time int64
		var sample unsafe.Pointer
		hr := com.Call(r.object, 9, stream, 0, uintptr(unsafe.Pointer(&actual)), uintptr(unsafe.Pointer(&flags)),
			uintptr(unsafe.Pointer(&time)), uintptr(unsafe.Pointer(&sample)))
		if err := com.Check("ReadSample", hr); err != nil {
			return nil, 0, err
		}
		if flags&1 != 0 {
			return nil, 0, &com.Error{Op: "decoding", Code: 0x80004005}
		}
		if sample == nil {
			if flags&2 != 0 { // end of stream
				return nil, 0, nil
			}
			continue // a gap marker or a format change: ask again
		}
		var buffer unsafe.Pointer
		hr = com.Call(sample, 41, uintptr(unsafe.Pointer(&buffer)))
		com.Release(&sample)
		if err := com.Check("ConvertToContiguousBuffer", hr); err != nil {
			return nil, 0, err
		}
		var memory *byte
		var limit, length uint32
		hr = com.Call(buffer, 3, uintptr(unsafe.Pointer(&memory)), uintptr(unsafe.Pointer(&limit)), uintptr(unsafe.Pointer(&length)))
		if err := com.Check("Lock", hr); err != nil {
			com.Release(&buffer)
			return nil, 0, err
		}
		data := append([]byte(nil), unsafe.Slice(memory, length)...)
		com.Call(buffer, 4)
		com.Release(&buffer)
		return data, time, nil
	}
}
