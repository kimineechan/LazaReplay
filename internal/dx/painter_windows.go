//go:build windows

package dx

import (
	"errors"
	"syscall"
	"unsafe"

	"lazareplay/internal/com"
)

// The picture Windows hands over has no mouse pointer in it; Windows draws
// the pointer separately. Painter puts it back: it copies the desktop
// picture and draws the pointer on the copy, on the graphics chip.

var (
	compiler = syscall.NewLazyDLL("d3dcompiler_47.dll")
	compile  = compiler.NewProc("D3DCompile")
)

// quadShader is the part shared by every program run on the graphics chip
// here: it places the four corners of a rectangle that fills the viewport,
// so where something is drawn is chosen by setting the viewport alone.
const quadShader = `
struct Corner { float4 position : SV_Position; float2 uv : TEXCOORD0; };

Corner vertex(uint id : SV_VertexID) {
	Corner c;
	float2 at = float2(id & 1, id >> 1);
	c.position = float4(at.x * 2 - 1, 1 - at.y * 2, 0, 1);
	c.uv = at;
	return c;
}
`

// pointerShader copies the picture of the pointer, pixel for pixel.
const pointerShader = quadShader + `
Texture2D image : register(t0);

float4 pixel(Corner c) : SV_Target {
	uint width, height;
	image.GetDimensions(width, height);
	return image.Load(int3(c.uv * float2(width, height), 0));
}
`

// targetBlend mirrors D3D11_RENDER_TARGET_BLEND_DESC.
type targetBlend struct {
	Enable                       int32
	Source, Dest, Op             uint32
	SourceAlpha, DestAlpha, OpAl uint32
	WriteMask                    uint8
	_                            [3]uint8
}

// blendDesc mirrors D3D11_BLEND_DESC.
type blendDesc struct {
	AlphaToCoverage int32
	Independent     int32
	Targets         [8]targetBlend
}

// Blend factors.
const (
	blendZero           = 1
	blendOne            = 2
	blendInvSourceColor = 4
	blendSourceAlpha    = 5
	blendInvSourceAlpha = 6
	blendInvDestColor   = 10
	blendAdd            = 1
)

// Painter draws the mouse pointer onto a copy of the desktop picture.
type Painter struct {
	device *Device
	lock   unsafe.Pointer // ID3D10Multithread
	vertex unsafe.Pointer // ID3D11VertexShader
	pixel  unsafe.Pointer // ID3D11PixelShader
	over   unsafe.Pointer // blend state: paint through alpha
	invert unsafe.Pointer // blend state: invert where white

	canvas unsafe.Pointer // BGRA texture the size of the screen
	target unsafe.Pointer // render target view of it

	serial        int
	colour, mask  unsafe.Pointer // shader resource views of the two layers
	width, height int            // of the pointer picture
}

// NewPainter prepares pointer drawing for a screen of the given size.
func (d *Device) NewPainter(width, height int) (*Painter, error) {
	p := &Painter{device: d, serial: -1}
	var err error
	if p.lock, err = com.Query(d.Context, &iidMultithread); err != nil {
		return nil, err
	}
	if p.vertex, err = d.compile(pointerShader, "vertex", "vs_4_0", 12); err == nil {
		p.pixel, err = d.compile(pointerShader, "pixel", "ps_4_0", 15)
	}
	if err != nil {
		p.Close()
		return nil, err
	}
	states := []struct {
		state        *unsafe.Pointer
		source, dest uint32
	}{{&p.over, blendSourceAlpha, blendInvSourceAlpha}, {&p.invert, blendInvDestColor, blendInvSourceColor}}
	for _, s := range states {
		desc := blendDesc{}
		desc.Targets[0] = targetBlend{
			Enable: 1, Source: s.source, Dest: s.dest, Op: blendAdd,
			SourceAlpha: blendZero, DestAlpha: blendOne, OpAl: blendAdd, WriteMask: 0x0F,
		}
		hr := com.Call(d.Object, 20, uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(s.state)))
		if err := com.Check("CreateBlendState", hr); err != nil {
			p.Close()
			return nil, err
		}
	}
	if p.canvas, err = d.NewTexture(width, height, FormatBGRA, UsageDefault, BindRenderTarget|BindShaderResource, 0, nil, 0); err == nil {
		err = com.Check("CreateRenderTargetView", com.Call(d.Object, 9, uintptr(p.canvas), 0, uintptr(unsafe.Pointer(&p.target))))
	}
	if err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

// compile turns one function of a program into a shader the chip can run.
// create is the position of the device method that makes that kind of
// shader: 12 for one that places corners, 15 for one that colours pixels.
func (d *Device) compile(program, entry, model string, create int) (unsafe.Pointer, error) {
	if err := compile.Find(); err != nil {
		return nil, errors.New("the shader compiler of Windows is missing")
	}
	source := []byte(program)
	name, target := append([]byte(entry), 0), append([]byte(model), 0)
	var code, messages unsafe.Pointer
	hr, _, _ := compile.Call(uintptr(unsafe.Pointer(&source[0])), uintptr(len(source)), 0, 0, 0,
		uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(&target[0])), 0, 0,
		uintptr(unsafe.Pointer(&code)), uintptr(unsafe.Pointer(&messages)))
	com.Release(&messages)
	if err := com.Check("D3DCompile", hr); err != nil {
		return nil, err
	}
	defer com.Release(&code)
	var shader unsafe.Pointer
	hr = com.Call(d.Object, create, com.Call(code, 3), com.Call(code, 4), 0, uintptr(unsafe.Pointer(&shader)))
	return shader, com.Check("CreateShader", hr)
}

// load replaces the pointer picture.
func (p *Painter) load(shape *Shape) {
	com.Release(&p.colour)
	com.Release(&p.mask)
	colour, invert, width, height := Layers(shape)
	p.width, p.height = width, height
	p.colour = p.layer(colour)
	p.mask = p.layer(invert)
}

func (p *Painter) layer(pixels []byte) unsafe.Pointer {
	if pixels == nil {
		return nil
	}
	texture, err := p.device.NewTexture(p.width, p.height, FormatBGRA, UsageDefault, BindShaderResource, 0, pixels, p.width*4)
	if err != nil {
		return nil
	}
	defer com.Release(&texture)
	var view unsafe.Pointer
	com.Call(p.device.Object, 7, uintptr(texture), 0, uintptr(unsafe.Pointer(&view)))
	return view
}

// Paint returns the picture to record for a frame: the frame itself when no
// pointer is showing, otherwise a copy with the pointer drawn on it. The
// copy is only valid until the next call.
func (p *Painter) Paint(frame unsafe.Pointer, pointer Pointer) unsafe.Pointer {
	if p == nil || !pointer.Visible || pointer.Shape == nil {
		return frame
	}
	if pointer.Serial != p.serial {
		p.load(pointer.Shape)
		p.serial = pointer.Serial
	}
	if p.colour == nil && p.mask == nil {
		return frame
	}
	ctx := p.device.Context
	// The encoder uses the same device from its own threads. Holding the
	// device lock keeps this run of calls together.
	com.Call(p.lock, 3)
	defer com.Call(p.lock, 4)
	p.device.Copy(p.canvas, frame)
	viewport := [6]float32{float32(pointer.X), float32(pointer.Y), float32(p.width), float32(p.height), 0, 1}
	com.Call(ctx, 33, 1, uintptr(unsafe.Pointer(&p.target)), 0) // OMSetRenderTargets
	com.Call(ctx, 44, 1, uintptr(unsafe.Pointer(&viewport)))    // RSSetViewports
	com.Call(ctx, 24, 5)                                        // IASetPrimitiveTopology: triangle strip
	com.Call(ctx, 17, 0)                                        // IASetInputLayout: none
	com.Call(ctx, 43, 0)                                        // RSSetState: default
	com.Call(ctx, 11, uintptr(p.vertex), 0, 0)                  // VSSetShader
	com.Call(ctx, 9, uintptr(p.pixel), 0, 0)                    // PSSetShader
	for _, pass := range [2]struct{ view, blend unsafe.Pointer }{{p.colour, p.over}, {p.mask, p.invert}} {
		if pass.view == nil {
			continue
		}
		view := pass.view
		com.Call(ctx, 8, 0, 1, uintptr(unsafe.Pointer(&view))) // PSSetShaderResources
		com.Call(ctx, 35, uintptr(pass.blend), 0, 0xFFFFFFFF)  // OMSetBlendState
		com.Call(ctx, 13, 4, 0)                                // Draw
	}
	var none unsafe.Pointer
	com.Call(ctx, 8, 0, 1, uintptr(unsafe.Pointer(&none)))
	com.Call(ctx, 33, 0, 0, 0)
	return p.canvas
}

// Close releases the painter. It is safe on a nil painter.
func (p *Painter) Close() {
	if p == nil {
		return
	}
	for _, object := range []*unsafe.Pointer{&p.colour, &p.mask, &p.target, &p.canvas, &p.invert, &p.over, &p.pixel, &p.vertex, &p.lock} {
		com.Release(object)
	}
}
