//go:build windows

package mf

import (
	"fmt"
	"unsafe"

	"lazareplay/internal/com"
)

var (
	iidTransform      = com.ID("bf94c121-5b05-4e6f-8000-ba598961414d")
	iidEventGenerator = com.ID("2cd0bd52-bcd5-4b89-b62c-eadc0c031e7d")
	iidCodecAPI       = com.ID("901db4c7-31ce-41a2-85dc-8fa0bf41b8da")
	iidTexture2D      = com.ID("6f15aaf2-d208-4e89-9ab4-489535d34f9c")

	// Encoder settings, set through ICodecAPI.
	apiRateControl   = com.ID("1c0608e9-370c-4710-8a58-cb6181c42423")
	apiMeanBitRate   = com.ID("f7222374-2144-4815-b550-a37f8e12ee52")
	apiMaxBitRate    = com.ID("9651eae4-39b9-4ebf-85ef-d7f444ec7465")
	apiQualitySpeed  = com.ID("98332df8-03cd-476b-89fa-3f9e442dec9f")
	apiGOPSize       = com.ID("95f31b26-95a4-41aa-9303-246a7fc6eef1")
	apiBPictures     = com.ID("8d390aac-dc5c-4200-b57f-814d04babab2")
	apiLowLatency    = com.ID("9c27891a-ed7a-40e1-88e8-b22727a024ee")
	apiForceKeyFrame = com.ID("398c1b98-8353-475a-9ef2-8f265d260345")
)

const (
	errUnexpected = 0x8000FFFF

	eventError      = 1
	eventNeedInput  = 601
	eventHaveOutput = 602

	messageFlush          = 0
	messageSetD3DManager  = 2
	messageBeginStreaming = 0x10000000
	messageEndStreaming   = 0x10000001
	messageEndOfStream    = 0x10000002
	messageStartOfStream  = 0x10000003
)

// Config describes the video an encoder is to produce.
type Config struct {
	Width, Height int
	Rate          int // frames per second
	Bitrate       int // average bits per second
	Peak          int // highest bits per second over a short stretch
	KeyInterval   int // frames between keyframes at most
}

// Encoder compresses frames to H.264. A hardware encoder reads NV12 textures
// straight from the graphics chip and reports through a queue of events; the
// software encoder is driven by plain calls. Both are hidden behind Accepting,
// Submit and Collect, which never block.
type Encoder struct {
	Name     string
	Hardware bool

	transform unsafe.Pointer // IMFTransform
	events    unsafe.Pointer // IMFMediaEventGenerator, nil when driven by calls
	codec     unsafe.Pointer // ICodecAPI
	manager   unsafe.Pointer // IMFDXGIDeviceManager
	activate  Attributes

	config   Config
	credits  int            // frames the encoder has asked for
	ready    int            // outputs the encoder has announced
	provides bool           // the encoder supplies its own output samples
	own      unsafe.Pointer // output sample we supply otherwise
	header   []byte         // parameter sets from the output type, if given
}

// variant mirrors the VARIANT structure for the two kinds used here.
type variant struct {
	kind  uint16
	_     [3]uint16
	value uint64
	_     uint64
}

func number(v uint32) variant { return variant{kind: 19, value: uint64(v)} }
func boolean(v bool) variant {
	if v {
		return variant{kind: 11, value: 0xFFFF}
	}
	return variant{kind: 11}
}

func (e *Encoder) set(key *com.GUID, v variant) error {
	if e.codec == nil {
		return fmt.Errorf("encoder has no settings interface")
	}
	return com.Check("ICodecAPI.SetValue", com.Call(e.codec, 9, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&v))))
}

func (e *Encoder) message(kind, param uintptr) error {
	return com.Check("ProcessMessage", com.Call(e.transform, 23, kind, param))
}

// Open starts the encoder. device is the Direct3D device whose textures it
// will be given, or nil to have frames passed through main memory.
func (c *Codec) Open(device unsafe.Pointer, config Config) (*Encoder, error) {
	e := &Encoder{Name: c.Name, Hardware: c.Hardware, config: config}
	hr := com.Call(c.activate.P, 33, uintptr(unsafe.Pointer(&iidTransform)), uintptr(unsafe.Pointer(&e.transform)))
	if err := com.Check("ActivateObject", hr); err != nil {
		return nil, err
	}
	e.activate = c.activate
	com.Call(c.activate.P, 1) // the encoder keeps its own hold on the activation object
	if err := e.configure(device); err != nil {
		e.Close()
		return nil, fmt.Errorf("%s: %w", c.Name, err)
	}
	return e, nil
}

func (e *Encoder) configure(device unsafe.Pointer) error {
	c := e.config
	var attrs Attributes
	async := false
	if int32(com.Call(e.transform, 8, uintptr(unsafe.Pointer(&attrs.P)))) >= 0 && attrs.P != nil {
		defer attrs.Release()
		if v, _ := attrs.UINT32(&Async); v != 0 {
			async = true
			if err := attrs.SetUINT32(&AsyncUnlock, 1); err != nil {
				return err
			}
		}
		if aware, _ := attrs.UINT32(&D3D11Aware); aware != 0 && device != nil {
			var token uint32
			hr, _, _ := mfCreateManager.Call(uintptr(unsafe.Pointer(&token)), uintptr(unsafe.Pointer(&e.manager)))
			if err := com.Check("MFCreateDXGIDeviceManager", hr); err != nil {
				return err
			}
			if err := com.Check("ResetDevice", com.Call(e.manager, 7, uintptr(device), uintptr(token))); err != nil {
				return err
			}
			if err := e.message(messageSetD3DManager, uintptr(e.manager)); err != nil {
				return err
			}
		}
		attrs.SetUINT32(&LowLatency, 1)
	}
	if e.Hardware && e.manager == nil && device != nil {
		return fmt.Errorf("encoder cannot read frames from the graphics chip")
	}

	// Settings. Peak-constrained variable bitrate spends few bits on a still
	// desktop and is capped when the picture gets busy, which bounds memory.
	// No B-frames and low latency mean one frame in gives one frame out, in
	// the same order.
	e.codec, _ = com.Query(e.transform, &iidCodecAPI)
	if e.codec != nil {
		if e.set(&apiRateControl, number(1)) != nil { // peak-constrained VBR
			if e.set(&apiRateControl, number(2)) != nil { // unconstrained VBR
				e.set(&apiRateControl, number(0)) // constant bitrate
			}
		}
		e.set(&apiMeanBitRate, number(uint32(c.Bitrate)))
		e.set(&apiMaxBitRate, number(uint32(c.Peak)))
		e.set(&apiGOPSize, number(uint32(c.KeyInterval)))
		e.set(&apiBPictures, number(0))
		e.set(&apiLowLatency, boolean(true))
		e.set(&apiQualitySpeed, number(33)) // favour speed: least work for the chip
	}

	output, err := NewMediaType()
	if err != nil {
		return err
	}
	defer output.Release()
	output.SetGUID(&MajorType, &TypeVideo)
	output.SetGUID(&Subtype, &FormatH264)
	output.SetUINT32(&AvgBitrate, uint32(c.Bitrate))
	output.SetPair(&FrameSize, uint32(c.Width), uint32(c.Height))
	output.SetPair(&FrameRate, uint32(c.Rate), 1)
	output.SetPair(&PixelAspectRatio, 1, 1)
	output.SetUINT32(&InterlaceMode, 2) // progressive
	output.SetUINT32(&Profile, 100)     // High
	if err := com.Check("SetOutputType", com.Call(e.transform, 16, 0, uintptr(output.P), 0)); err != nil {
		output.SetUINT32(&Profile, 77) // Main
		if err := com.Check("SetOutputType", com.Call(e.transform, 16, 0, uintptr(output.P), 0)); err != nil {
			return err
		}
	}

	input, err := NewMediaType()
	if err != nil {
		return err
	}
	defer input.Release()
	input.SetGUID(&MajorType, &TypeVideo)
	input.SetGUID(&Subtype, &FormatNV12)
	input.SetPair(&FrameSize, uint32(c.Width), uint32(c.Height))
	input.SetPair(&FrameRate, uint32(c.Rate), 1)
	input.SetPair(&PixelAspectRatio, 1, 1)
	input.SetUINT32(&InterlaceMode, 2)
	if err := com.Check("SetInputType", com.Call(e.transform, 15, 0, uintptr(input.P), 0)); err != nil {
		return err
	}

	var info struct{ Flags, Size, Alignment uint32 }
	com.Call(e.transform, 7, 0, uintptr(unsafe.Pointer(&info)))
	e.provides = info.Flags&0x300 != 0
	if !e.provides {
		size := max(info.Size, uint32(c.Width*c.Height*2))
		if e.own, err = newMemorySample(size); err != nil {
			return err
		}
	}
	e.readHeader()

	if async {
		if e.events, err = com.Query(e.transform, &iidEventGenerator); err != nil {
			return err
		}
	}
	if err := e.message(messageBeginStreaming, 0); err != nil {
		return err
	}
	return e.message(messageStartOfStream, 0)
}

// readHeader remembers the parameter sets attached to the output type.
func (e *Encoder) readHeader() {
	var current Attributes
	if int32(com.Call(e.transform, 18, 0, uintptr(unsafe.Pointer(&current.P)))) >= 0 && current.P != nil {
		if blob := current.Blob(&SequenceHeader); blob != nil {
			e.header = blob
		}
		current.Release()
	}
}

// Header returns the parameter sets the encoder announced with its output
// type, in start-code form, or nil when it only sends them inside the stream.
func (e *Encoder) Header() []byte { return e.header }

func newMemorySample(size uint32) (unsafe.Pointer, error) {
	var sample, buffer unsafe.Pointer
	hr, _, _ := mfCreateSample.Call(uintptr(unsafe.Pointer(&sample)))
	if err := com.Check("MFCreateSample", hr); err != nil {
		return nil, err
	}
	hr, _, _ = mfCreateMemory.Call(uintptr(size), uintptr(unsafe.Pointer(&buffer)))
	if err := com.Check("MFCreateMemoryBuffer", hr); err != nil {
		com.Release(&sample)
		return nil, err
	}
	com.Call(sample, 42, uintptr(buffer)) // AddBuffer
	com.Release(&buffer)
	return sample, nil
}

// pump reads the encoder's waiting events without blocking.
func (e *Encoder) pump() error {
	for e.events != nil {
		var event unsafe.Pointer
		hr := com.Call(e.events, 3, 1, uintptr(unsafe.Pointer(&event))) // GetEvent, no wait
		if uint32(hr) == errNoMoreEvents {
			return nil
		}
		if err := com.Check("GetEvent", hr); err != nil {
			return err
		}
		var kind, status uint32
		com.Call(event, 33, uintptr(unsafe.Pointer(&kind)))
		com.Call(event, 35, uintptr(unsafe.Pointer(&status)))
		com.Release(&event)
		switch kind {
		case eventNeedInput:
			e.credits++
		case eventHaveOutput:
			e.ready++
		case eventError:
			return &com.Error{Op: "encoder", Code: status}
		}
	}
	return nil
}

// Accepting reports whether the encoder will take a frame now.
func (e *Encoder) Accepting() (bool, error) {
	if e.events == nil {
		return true, nil
	}
	err := e.pump()
	return e.credits > 0, err
}

// Submit hands the encoder one NV12 texture. time and duration are in 100 ns
// units. With key set the frame is made a keyframe, a point a clip can start
// from.
func (e *Encoder) Submit(texture unsafe.Pointer, time, duration int64, key bool) error {
	var buffer, sample unsafe.Pointer
	hr, _, _ := mfCreateSurface.Call(uintptr(unsafe.Pointer(&iidTexture2D)), uintptr(texture), 0, 0, uintptr(unsafe.Pointer(&buffer)))
	if err := com.Check("MFCreateDXGISurfaceBuffer", hr); err != nil {
		return err
	}
	defer com.Release(&buffer)
	com.Call(buffer, 6, uintptr(e.config.Width*e.config.Height*3/2)) // SetCurrentLength
	hr, _, _ = mfCreateSample.Call(uintptr(unsafe.Pointer(&sample)))
	if err := com.Check("MFCreateSample", hr); err != nil {
		return err
	}
	defer com.Release(&sample)
	com.Call(sample, 42, uintptr(buffer))   // AddBuffer
	com.Call(sample, 36, uintptr(time))     // SetSampleTime
	com.Call(sample, 38, uintptr(duration)) // SetSampleDuration
	if key {
		e.set(&apiForceKeyFrame, number(1))
	}
	hr = com.Call(e.transform, 24, 0, uintptr(sample), 0) // ProcessInput
	if e.events != nil && uint32(hr) != errNotAccepting {
		e.credits--
	}
	return com.Check("ProcessInput", hr)
}

// outputBuffer mirrors MFT_OUTPUT_DATA_BUFFER.
type outputBuffer struct {
	Stream uint32
	_      uint32
	Sample unsafe.Pointer
	Status uint32
	_      uint32
	Events unsafe.Pointer
}

// Collect passes every finished frame to take, oldest first. The bytes are
// only valid during the call. It returns at once when nothing is ready.
func (e *Encoder) Collect(take func(data []byte, time int64)) error {
	if err := e.pump(); err != nil {
		return err
	}
	for e.events == nil || e.ready > 0 {
		out := outputBuffer{Sample: e.own}
		var status uint32
		hr := com.Call(e.transform, 25, 0, 1, uintptr(unsafe.Pointer(&out)), uintptr(unsafe.Pointer(&status)))
		com.Release(&out.Events)
		switch uint32(hr) {
		case errNeedMoreInput:
			return nil
		case errStreamChange:
			// The encoder has settled its output format. Accept it and ask again.
			var offered Attributes
			if err := com.Check("GetOutputAvailableType", com.Call(e.transform, 14, 0, 0, uintptr(unsafe.Pointer(&offered.P)))); err != nil {
				return err
			}
			err := com.Check("SetOutputType", com.Call(e.transform, 16, 0, uintptr(offered.P), 0))
			offered.Release()
			if err != nil {
				return err
			}
			e.readHeader()
			continue
		case errUnexpected:
			if e.events != nil { // announced output was the format change itself
				e.ready--
				continue
			}
		}
		if e.events != nil {
			e.ready--
		}
		if err := com.Check("ProcessOutput", hr); err != nil {
			return err
		}
		if out.Sample == nil {
			continue
		}
		e.read(out.Sample, take)
		if e.provides {
			com.Release(&out.Sample)
		}
	}
	return nil
}

// read hands the bytes of one output sample to take.
func (e *Encoder) read(sample unsafe.Pointer, take func([]byte, int64)) {
	var time int64
	com.Call(sample, 35, uintptr(unsafe.Pointer(&time))) // GetSampleTime
	var buffer unsafe.Pointer
	if int32(com.Call(sample, 41, uintptr(unsafe.Pointer(&buffer)))) < 0 { // ConvertToContiguousBuffer
		return
	}
	var data *byte
	var limit, length uint32
	if int32(com.Call(buffer, 3, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&limit)), uintptr(unsafe.Pointer(&length)))) >= 0 {
		if length > 0 {
			take(unsafe.Slice(data, length), time)
		}
		com.Call(buffer, 4) // Unlock
	}
	if !e.provides {
		com.Call(buffer, 6, 0) // empty our own buffer for the next frame
	}
	com.Release(&buffer)
}

// Close stops the encoder and frees it.
func (e *Encoder) Close() {
	if e.transform != nil {
		e.message(messageEndOfStream, 0)
		e.message(messageEndStreaming, 0)
		e.message(messageFlush, 0)
	}
	com.Release(&e.own)
	com.Release(&e.events)
	com.Release(&e.codec)
	com.Release(&e.transform)
	if e.activate.P != nil {
		com.Call(e.activate.P, 34) // ShutdownObject
		e.activate.Release()
	}
	com.Release(&e.manager)
}
