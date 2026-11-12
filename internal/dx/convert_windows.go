//go:build windows

package dx

import (
	"unsafe"

	"lazareplay/internal/com"
)

// contentDesc mirrors D3D11_VIDEO_PROCESSOR_CONTENT_DESC.
type contentDesc struct {
	FrameFormat               uint32
	InputRateN, InputRateD    uint32
	InputWidth, InputHeight   uint32
	OutputRateN, OutputRateD  uint32
	OutputWidth, OutputHeight uint32
	Usage                     uint32
}

// processorStream mirrors D3D11_VIDEO_PROCESSOR_STREAM.
type processorStream struct {
	Enable       int32
	OutputIndex  uint32
	InputFrame   uint32
	PastFrames   uint32
	FutureFrames uint32
	_            uint32
	Past         unsafe.Pointer
	Input        unsafe.Pointer
	Future       unsafe.Pointer
	PastRight    unsafe.Pointer
	InputRight   unsafe.Pointer
	FutureRight  unsafe.Pointer
}

// Converter turns desktop pictures into the format the video encoder takes.
// It changes BGRA pixels to NV12 and scales them, all on the graphics chip
// through its video processor, so no frame is ever copied to main memory.
type Converter struct {
	device     *Device
	enumerator unsafe.Pointer // ID3D11VideoProcessorEnumerator
	processor  unsafe.Pointer // ID3D11VideoProcessor
	views      map[unsafe.Pointer]unsafe.Pointer

	InWidth, InHeight   int
	OutWidth, OutHeight int
}

// Target is a texture the converter writes to and the encoder reads.
type Target struct {
	Texture unsafe.Pointer // ID3D11Texture2D, NV12
	view    unsafe.Pointer // ID3D11VideoProcessorOutputView
	idle    uintptr        // holders when nobody else is using it
}

// NewConverter prepares conversion from one size to another. The picture
// keeps its shape; any spare room is left black.
func (d *Device) NewConverter(inWidth, inHeight, outWidth, outHeight, rate int) (*Converter, error) {
	c := &Converter{
		device: d, views: map[unsafe.Pointer]unsafe.Pointer{},
		InWidth: inWidth, InHeight: inHeight, OutWidth: outWidth, OutHeight: outHeight,
	}
	desc := contentDesc{
		InputRateN: uint32(rate), InputRateD: 1, InputWidth: uint32(inWidth), InputHeight: uint32(inHeight),
		OutputRateN: uint32(rate), OutputRateD: 1, OutputWidth: uint32(outWidth), OutputHeight: uint32(outHeight),
		Usage: 1, // optimal speed
	}
	hr := com.Call(d.video, 10, uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&c.enumerator)))
	if err := com.Check("CreateVideoProcessorEnumerator", hr); err != nil {
		return nil, err
	}
	hr = com.Call(d.video, 4, uintptr(c.enumerator), 0, uintptr(unsafe.Pointer(&c.processor)))
	if err := com.Check("CreateVideoProcessor", hr); err != nil {
		c.Close()
		return nil, err
	}
	ctx, p := d.videoContext, uintptr(c.processor)

	// Input: the desktop, full-range RGB. Output: BT.709 colour at studio
	// range (16 to 235), which is what players assume for HD video.
	input, output := uint32(0), uint32(1<<2|1<<4)
	com.Call(ctx, 28, p, 0, uintptr(unsafe.Pointer(&input)))
	com.Call(ctx, 15, p, uintptr(unsafe.Pointer(&output)))
	com.Call(ctx, 27, p, 0, 0) // progressive frames
	com.Call(ctx, 37, p, 0, 0) // no automatic picture "enhancement"

	source := Rect{0, 0, int32(inWidth), int32(inHeight)}
	whole := Rect{0, 0, int32(outWidth), int32(outHeight)}
	fitted := fit(inWidth, inHeight, outWidth, outHeight)
	black := [4]float32{0, 0, 0, 1}
	com.Call(ctx, 30, p, 0, 1, uintptr(unsafe.Pointer(&source)))
	com.Call(ctx, 31, p, 0, 1, uintptr(unsafe.Pointer(&fitted)))
	com.Call(ctx, 13, p, 1, uintptr(unsafe.Pointer(&whole)))
	com.Call(ctx, 14, p, 0, uintptr(unsafe.Pointer(&black)))
	return c, nil
}

// fit returns the largest rectangle of the input's shape inside the output,
// centred, on even coordinates.
func fit(inWidth, inHeight, outWidth, outHeight int) Rect {
	w, h := outWidth, outHeight
	if inWidth*outHeight > inHeight*outWidth {
		h = (inHeight*outWidth/inWidth + 1) &^ 1
	} else if inWidth*outHeight < inHeight*outWidth {
		w = (inWidth*outHeight/inHeight + 1) &^ 1
	}
	if w > outWidth {
		w = outWidth
	}
	if h > outHeight {
		h = outHeight
	}
	x, y := (outWidth-w)/2&^1, (outHeight-h)/2&^1
	return Rect{int32(x), int32(y), int32(x + w), int32(y + h)}
}

// NewTarget creates a texture for converted frames.
func (c *Converter) NewTarget() (*Target, error) {
	texture, err := c.device.NewTexture(c.OutWidth, c.OutHeight, FormatNV12, UsageDefault, BindRenderTarget|BindShaderResource, 0, nil, 0)
	if err != nil {
		return nil, err
	}
	t := &Target{Texture: texture}
	desc := [4]uint32{1} // a 2D texture, first level
	hr := com.Call(c.device.video, 9, uintptr(texture), uintptr(c.enumerator), uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&t.view)))
	if err := com.Check("CreateVideoProcessorOutputView", hr); err != nil {
		t.Close()
		return nil, err
	}
	t.idle = references(texture)
	return t, nil
}

// Idle reports whether nothing else is still using the target. The encoder
// takes a hold on a texture while it reads it and lets go when it is done.
func (t *Target) Idle() bool { return references(t.Texture) <= t.idle }

// Close releases the target.
func (t *Target) Close() {
	com.Release(&t.view)
	com.Release(&t.Texture)
}

// Convert draws the source picture into the target.
func (c *Converter) Convert(source unsafe.Pointer, target *Target) error {
	view, err := c.inputView(source)
	if err != nil {
		return err
	}
	stream := processorStream{Enable: 1, Input: view}
	hr := com.Call(c.device.videoContext, 53, uintptr(c.processor), uintptr(target.view), 0, 1, uintptr(unsafe.Pointer(&stream)))
	return com.Check("VideoProcessorBlt", hr)
}

// inputView returns the processor's handle on a source texture, creating it
// on first use. Desktop duplication hands back the same few textures over
// and over, so the handles are kept.
func (c *Converter) inputView(texture unsafe.Pointer) (unsafe.Pointer, error) {
	if view, ok := c.views[texture]; ok {
		return view, nil
	}
	if len(c.views) >= 8 {
		c.Forget()
	}
	desc := [4]uint32{0, 1} // default format, a 2D texture
	var view unsafe.Pointer
	hr := com.Call(c.device.video, 8, uintptr(texture), uintptr(c.enumerator), uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&view)))
	if err := com.Check("CreateVideoProcessorInputView", hr); err != nil {
		return nil, err
	}
	com.Call(texture, 1) // hold the texture so its address stays unique
	c.views[texture] = view
	return view, nil
}

// Forget drops the handles on source textures. It must be called before the
// duplication that owns those textures is closed.
func (c *Converter) Forget() {
	for texture, view := range c.views {
		com.Release(&view)
		com.Call(texture, 2)
		delete(c.views, texture)
	}
}

// Close releases the converter.
func (c *Converter) Close() {
	c.Forget()
	com.Release(&c.processor)
	com.Release(&c.enumerator)
}
