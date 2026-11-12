//go:build windows

package dx

import (
	"unsafe"

	"lazareplay/internal/com"
)

// PreferProcessor makes new converters use the video processor of the
// graphics chip even where drawing would work. The self-test sets it to
// check that path too.
var PreferProcessor bool

// Converter turns desktop pictures into the format the video encoder
// takes. It changes BGRA pixels to NV12 and scales them, all on the
// graphics chip, so no frame is ever copied to main memory.
//
// It has two ways of doing so. The first choice is to draw: two small
// programs, run by the chip for every pixel, write the brightness plane
// and the colour plane of the result. That costs the processor almost
// nothing. Where a chip cannot draw into those planes, its video processor
// does the same job in one call, at more cost to the processor.
type Converter struct {
	device *Device
	draw   *drawer // nil when the video processor does the work

	enumerator unsafe.Pointer // ID3D11VideoProcessorEnumerator
	processor  unsafe.Pointer // ID3D11VideoProcessor

	views map[unsafe.Pointer]unsafe.Pointer // a handle on each source texture
	area  Rect                              // where the picture goes in the output

	InWidth, InHeight   int
	OutWidth, OutHeight int
}

// Target is a texture the converter writes to and the encoder reads.
type Target struct {
	Texture unsafe.Pointer // ID3D11Texture2D, NV12
	view    unsafe.Pointer // ID3D11VideoProcessorOutputView, for the video processor
	luma    unsafe.Pointer // ID3D11RenderTargetView of the brightness plane, for drawing
	chroma  unsafe.Pointer // ID3D11RenderTargetView of the colour plane
	idle    uintptr        // holders when nobody else is using it
}

// NewConverter prepares conversion from one size to another. The picture
// keeps its shape; any spare room is left black.
func (d *Device) NewConverter(inWidth, inHeight, outWidth, outHeight, rate int) (*Converter, error) {
	c := &Converter{
		device: d, views: map[unsafe.Pointer]unsafe.Pointer{}, area: fit(inWidth, inHeight, outWidth, outHeight),
		InWidth: inWidth, InHeight: inHeight, OutWidth: outWidth, OutHeight: outHeight,
	}
	if !PreferProcessor {
		if c.draw = d.newDrawer(c.area, inWidth > c.area.Width() || inHeight > c.area.Height()); c.draw != nil {
			// Make sure this chip really can draw into the two planes.
			if trial, err := c.NewTarget(); err == nil {
				trial.Close()
				return c, nil
			}
			c.draw.close()
			c.draw = nil
		}
	}
	if err := c.useProcessor(rate); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Drawn reports whether the converter draws, the cheaper of its two ways.
func (c *Converter) Drawn() bool { return c.draw != nil }

// NewTarget creates a texture for converted frames.
func (c *Converter) NewTarget() (*Target, error) {
	texture, err := c.device.NewTexture(c.OutWidth, c.OutHeight, FormatNV12, UsageDefault, BindRenderTarget|BindShaderResource, 0, nil, 0)
	if err != nil {
		return nil, err
	}
	t := &Target{Texture: texture}
	if c.draw != nil {
		err = c.draw.planes(c.device, t)
	} else {
		desc := [4]uint32{1} // a 2D texture, first level
		hr := com.Call(c.device.video, 9, uintptr(texture), uintptr(c.enumerator), uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&t.view)))
		err = com.Check("CreateVideoProcessorOutputView", hr)
	}
	if err != nil {
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
	com.Release(&t.luma)
	com.Release(&t.chroma)
	com.Release(&t.Texture)
}

// Convert writes the source picture into the target.
func (c *Converter) Convert(source unsafe.Pointer, target *Target) error {
	view, err := c.handle(source)
	if err != nil {
		return err
	}
	if c.draw != nil {
		c.draw.convert(c.device, view, target, c.area)
		return nil
	}
	stream := processorStream{Enable: 1, Input: view}
	hr := com.Call(c.device.videoContext, 53, uintptr(c.processor), uintptr(target.view), 0, 1, uintptr(unsafe.Pointer(&stream)))
	return com.Check("VideoProcessorBlt", hr)
}

// handle returns the converter's handle on a source texture, creating it
// on first use. Desktop duplication hands back the same few textures over
// and over, so the handles are kept.
func (c *Converter) handle(texture unsafe.Pointer) (unsafe.Pointer, error) {
	if view, ok := c.views[texture]; ok {
		return view, nil
	}
	if len(c.views) >= 8 {
		c.Forget()
	}
	var view unsafe.Pointer
	if c.draw != nil {
		hr := com.Call(c.device.Object, 7, uintptr(texture), 0, uintptr(unsafe.Pointer(&view)))
		if err := com.Check("CreateShaderResourceView", hr); err != nil {
			return nil, err
		}
	} else {
		desc := [4]uint32{0, 1} // default format, a 2D texture
		hr := com.Call(c.device.video, 8, uintptr(texture), uintptr(c.enumerator), uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&view)))
		if err := com.Check("CreateVideoProcessorInputView", hr); err != nil {
			return nil, err
		}
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
	if c.draw != nil {
		c.draw.close()
		c.draw = nil
	}
	com.Release(&c.processor)
	com.Release(&c.enumerator)
}

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

// useProcessor sets up the video processor of the graphics chip.
func (c *Converter) useProcessor(rate int) error {
	d := c.device
	desc := contentDesc{
		InputRateN: uint32(rate), InputRateD: 1, InputWidth: uint32(c.InWidth), InputHeight: uint32(c.InHeight),
		OutputRateN: uint32(rate), OutputRateD: 1, OutputWidth: uint32(c.OutWidth), OutputHeight: uint32(c.OutHeight),
		Usage: 1, // optimal speed
	}
	hr := com.Call(d.video, 10, uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&c.enumerator)))
	if err := com.Check("CreateVideoProcessorEnumerator", hr); err != nil {
		return err
	}
	hr = com.Call(d.video, 4, uintptr(c.enumerator), 0, uintptr(unsafe.Pointer(&c.processor)))
	if err := com.Check("CreateVideoProcessor", hr); err != nil {
		return err
	}
	ctx, p := d.videoContext, uintptr(c.processor)

	// Input: the desktop, full-range RGB. Output: BT.709 colour at studio
	// range (16 to 235), which is what players assume for HD video.
	input, output := uint32(0), uint32(1<<2|1<<4)
	com.Call(ctx, 28, p, 0, uintptr(unsafe.Pointer(&input)))
	com.Call(ctx, 15, p, uintptr(unsafe.Pointer(&output)))
	com.Call(ctx, 27, p, 0, 0) // progressive frames
	com.Call(ctx, 37, p, 0, 0) // no automatic picture "enhancement"

	source := Rect{0, 0, int32(c.InWidth), int32(c.InHeight)}
	whole := Rect{0, 0, int32(c.OutWidth), int32(c.OutHeight)}
	black := [4]float32{0, 0, 0, 1}
	com.Call(ctx, 30, p, 0, 1, uintptr(unsafe.Pointer(&source)))
	com.Call(ctx, 31, p, 0, 1, uintptr(unsafe.Pointer(&c.area)))
	com.Call(ctx, 13, p, 1, uintptr(unsafe.Pointer(&whole)))
	com.Call(ctx, 14, p, 0, uintptr(unsafe.Pointer(&black)))
	return nil
}

// The two programs that convert by drawing. Each is run by the chip for
// every pixel of one plane of the result. The numbers are those of the
// BT.709 standard at studio range: brightness from 16 to 235 and colour
// from 16 to 240, each out of 255.
//
// Four samples are averaged for each pixel. For the colour plane, which has
// half the resolution, that covers the four screen pixels behind each of
// its pixels. For the brightness plane the samples coincide unless the
// picture is being shrunk, when they smooth what a single sample would skip.
const convertShader = quadShader + `
Texture2D screen : register(t0);
SamplerState smooth : register(s0);
cbuffer Steps : register(b0) { float4 steps; };

float3 average(float2 uv, float2 d) {
	return (screen.Sample(smooth, uv + d * float2(-1, -1)).rgb + screen.Sample(smooth, uv + d * float2(1, -1)).rgb +
	        screen.Sample(smooth, uv + d * float2(-1, 1)).rgb + screen.Sample(smooth, uv + d * float2(1, 1)).rgb) * 0.25;
}

float luma(Corner c) : SV_Target {
	float3 rgb = average(c.uv, steps.xy);
	return dot(rgb, float3(0.2126, 0.7152, 0.0722)) * (219.0 / 255.0) + 16.0 / 255.0;
}

float2 chroma(Corner c) : SV_Target {
	float3 rgb = average(c.uv, steps.zw);
	float y = dot(rgb, float3(0.2126, 0.7152, 0.0722));
	return float2((rgb.b - y) / 1.8556, (rgb.r - y) / 1.5748) * (224.0 / 255.0) + 128.0 / 255.0;
}
`

// drawer holds what converting by drawing needs.
type drawer struct {
	lock      unsafe.Pointer // ID3D10Multithread
	vertex    unsafe.Pointer // ID3D11VertexShader
	luma      unsafe.Pointer // ID3D11PixelShader
	chroma    unsafe.Pointer // ID3D11PixelShader
	sampler   unsafe.Pointer // ID3D11SamplerState
	constants unsafe.Pointer // ID3D11Buffer
}

// samplerDesc mirrors D3D11_SAMPLER_DESC.
type samplerDesc struct {
	Filter                       uint32
	AddressU, AddressV, AddressW uint32
	Bias                         float32
	MaxAnisotropy                uint32
	Comparison                   uint32
	Border                       [4]float32
	MinLOD, MaxLOD               float32
}

// newDrawer prepares conversion by drawing into the given part of the
// output, or returns nil when this chip or this copy of Windows cannot.
func (d *Device) newDrawer(area Rect, shrinking bool) *drawer {
	w := &drawer{}
	var err error
	if w.lock, err = com.Query(d.Context, &iidMultithread); err != nil {
		return nil
	}
	if w.vertex, err = d.compile(convertShader, "vertex", "vs_4_0", 12); err == nil {
		if w.luma, err = d.compile(convertShader, "luma", "ps_4_0", 15); err == nil {
			w.chroma, err = d.compile(convertShader, "chroma", "ps_4_0", 15)
		}
	}
	if err == nil {
		sampler := samplerDesc{Filter: 0x15, AddressU: 3, AddressV: 3, AddressW: 3, MaxAnisotropy: 1, Comparison: 1, MaxLOD: 3.4e38}
		err = com.Check("CreateSamplerState", com.Call(d.Object, 23, uintptr(unsafe.Pointer(&sampler)), uintptr(unsafe.Pointer(&w.sampler))))
	}
	if err == nil {
		// How far apart the four samples lie, as fractions of the picture:
		// a quarter of an output pixel for brightness when shrinking, and
		// half an output pixel for colour, whose pixels are twice as large.
		width, height := float32(area.Width()), float32(area.Height())
		steps := [4]float32{0, 0, 0.5 / width, 0.5 / height}
		if shrinking {
			steps[0], steps[1] = 0.25/width, 0.25/height
		}
		desc := [6]uint32{16, 0, 0x4} // sixteen bytes, used as shader constants
		initial := subresourceData{Memory: unsafe.Pointer(&steps)}
		hr := com.Call(d.Object, 3, uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&initial)), uintptr(unsafe.Pointer(&w.constants)))
		err = com.Check("CreateBuffer", hr)
	}
	if err != nil {
		w.close()
		return nil
	}
	return w
}

// planes creates the handles that let the chip draw into the two planes of
// an NV12 texture, and paints both black.
func (w *drawer) planes(d *Device, t *Target) error {
	const oneByte, twoBytes, texture2D = 61, 49, 4 // R8, R8G8, a 2D texture
	luma, chroma := [5]uint32{oneByte, texture2D}, [5]uint32{twoBytes, texture2D}
	hr := com.Call(d.Object, 9, uintptr(t.Texture), uintptr(unsafe.Pointer(&luma)), uintptr(unsafe.Pointer(&t.luma)))
	if err := com.Check("CreateRenderTargetView", hr); err != nil {
		return err
	}
	hr = com.Call(d.Object, 9, uintptr(t.Texture), uintptr(unsafe.Pointer(&chroma)), uintptr(unsafe.Pointer(&t.chroma)))
	if err := com.Check("CreateRenderTargetView", hr); err != nil {
		return err
	}
	black, grey := [4]float32{16.0 / 255}, [4]float32{128.0 / 255, 128.0 / 255}
	com.Call(d.Context, 50, uintptr(t.luma), uintptr(unsafe.Pointer(&black)))
	com.Call(d.Context, 50, uintptr(t.chroma), uintptr(unsafe.Pointer(&grey)))
	return nil
}

// convert draws both planes of the target from the source.
func (w *drawer) convert(d *Device, source unsafe.Pointer, t *Target, area Rect) {
	ctx := d.Context
	// The encoder uses the same device from its own threads. Holding the
	// device lock keeps this run of calls together.
	com.Call(w.lock, 3)
	defer com.Call(w.lock, 4)
	whole := [6]float32{float32(area.Left), float32(area.Top), float32(area.Width()), float32(area.Height()), 0, 1}
	half := [6]float32{whole[0] / 2, whole[1] / 2, whole[2] / 2, whole[3] / 2, 0, 1}
	com.Call(ctx, 24, 5)                                         // IASetPrimitiveTopology: triangle strip
	com.Call(ctx, 17, 0)                                         // IASetInputLayout: none
	com.Call(ctx, 43, 0)                                         // RSSetState: default
	com.Call(ctx, 35, 0, 0, 0xFFFFFFFF)                          // OMSetBlendState: no blending
	com.Call(ctx, 11, uintptr(w.vertex), 0, 0)                   // VSSetShader
	com.Call(ctx, 8, 0, 1, uintptr(unsafe.Pointer(&source)))     // PSSetShaderResources
	com.Call(ctx, 10, 0, 1, uintptr(unsafe.Pointer(&w.sampler))) // PSSetSamplers
	com.Call(ctx, 16, 0, 1, uintptr(unsafe.Pointer(&w.constants)))
	for _, pass := range [2]struct {
		plane, program unsafe.Pointer
		viewport       *[6]float32
	}{{t.luma, w.luma, &whole}, {t.chroma, w.chroma, &half}} {
		plane := pass.plane
		com.Call(ctx, 33, 1, uintptr(unsafe.Pointer(&plane)), 0)     // OMSetRenderTargets
		com.Call(ctx, 44, 1, uintptr(unsafe.Pointer(pass.viewport))) // RSSetViewports
		com.Call(ctx, 9, uintptr(pass.program), 0, 0)                // PSSetShader
		com.Call(ctx, 13, 4, 0)                                      // Draw
	}
	var none unsafe.Pointer
	com.Call(ctx, 8, 0, 1, uintptr(unsafe.Pointer(&none)))
	com.Call(ctx, 33, 0, 0, 0)
}

func (w *drawer) close() {
	for _, object := range []*unsafe.Pointer{&w.constants, &w.sampler, &w.chroma, &w.luma, &w.vertex, &w.lock} {
		com.Release(object)
	}
}
