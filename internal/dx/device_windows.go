//go:build windows

package dx

import (
	"syscall"
	"unsafe"

	"lazareplay/internal/com"
)

var (
	d3d11        = syscall.NewLazyDLL("d3d11.dll")
	createDevice = d3d11.NewProc("D3D11CreateDevice")

	iidVideoDevice  = com.ID("10ec4d5b-975a-4689-b9e4-d0aac30fe333")
	iidVideoContext = com.ID("61f21c45-3c0e-4a74-9cea-67100d9ad5e4")
)

// Pixel formats.
const (
	FormatBGRA = 87  // four bytes per pixel: blue, green, red, alpha
	FormatNV12 = 103 // brightness plane, then a half-size colour plane
)

// Texture usage, binding and access flags.
const (
	UsageDefault = 0
	UsageStaging = 3

	BindShaderResource = 0x8
	BindRenderTarget   = 0x20

	AccessRead = 0x20000
)

// Failure codes the recorder reacts to.
const (
	ErrWaitTimeout   = 0x887A0027 // no new frame within the wait
	ErrAccessLost    = 0x887A0026 // the desktop changed mode or switched
	ErrDeviceRemoved = 0x887A0005
	ErrDeviceReset   = 0x887A0007
	ErrAccessDenied  = 0x80070005 // the secure desktop is showing
)

// Device is a Direct3D 11 device on one adapter, with its video interfaces.
type Device struct {
	Adapter *Adapter
	Object  unsafe.Pointer // ID3D11Device
	Context unsafe.Pointer // ID3D11DeviceContext

	video        unsafe.Pointer // ID3D11VideoDevice
	videoContext unsafe.Pointer // ID3D11VideoContext
}

// NewDevice creates a device on the adapter. The device can run video
// processing and may be used from several threads, which the hardware
// encoder needs because it works on threads of its own.
func NewDevice(a *Adapter) (*Device, error) {
	levels := [...]uint32{0xb100, 0xb000, 0xa100, 0xa000}
	d := &Device{Adapter: a}
	var level uint32
	const flags = 0x20 | 0x800 // BGRA support, video support
	hr, _, _ := createDevice.Call(uintptr(a.object), 0, 0, flags,
		uintptr(unsafe.Pointer(&levels[0])), uintptr(len(levels)), 7,
		uintptr(unsafe.Pointer(&d.Object)), uintptr(unsafe.Pointer(&level)), uintptr(unsafe.Pointer(&d.Context)))
	if err := com.Check("D3D11CreateDevice", hr); err != nil {
		return nil, err
	}
	if lock, err := com.Query(d.Context, &iidMultithread); err == nil {
		com.Call(lock, 5, 1) // SetMultithreadProtected
		com.Release(&lock)
	}
	var err error
	if d.video, err = com.Query(d.Object, &iidVideoDevice); err != nil {
		d.Close()
		return nil, err
	}
	if d.videoContext, err = com.Query(d.Context, &iidVideoContext); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// Close releases the device.
func (d *Device) Close() {
	com.Release(&d.videoContext)
	com.Release(&d.video)
	com.Release(&d.Context)
	com.Release(&d.Object)
}

// Flush sends queued drawing commands to the graphics chip now.
func (d *Device) Flush() { com.Call(d.Context, 111) }

// TextureDesc mirrors D3D11_TEXTURE2D_DESC.
type TextureDesc struct {
	Width, Height  uint32
	MipLevels      uint32
	ArraySize      uint32
	Format         uint32
	SampleCount    uint32
	SampleQuality  uint32
	Usage          uint32
	BindFlags      uint32
	CPUAccessFlags uint32
	MiscFlags      uint32
}

type subresourceData struct {
	Memory     unsafe.Pointer
	Pitch      uint32
	SlicePitch uint32
}

// NewTexture creates a texture. When pixels is not nil it holds the starting
// contents, pitch bytes per row.
func (d *Device) NewTexture(width, height int, format, usage, bind, access uint32, pixels []byte, pitch int) (unsafe.Pointer, error) {
	desc := TextureDesc{
		Width: uint32(width), Height: uint32(height), MipLevels: 1, ArraySize: 1, Format: format,
		SampleCount: 1, Usage: usage, BindFlags: bind, CPUAccessFlags: access,
	}
	var texture unsafe.Pointer
	var hr uintptr
	if pixels != nil {
		initial := subresourceData{Memory: unsafe.Pointer(&pixels[0]), Pitch: uint32(pitch)}
		hr = com.Call(d.Object, 5, uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&initial)), uintptr(unsafe.Pointer(&texture)))
	} else {
		hr = com.Call(d.Object, 5, uintptr(unsafe.Pointer(&desc)), 0, uintptr(unsafe.Pointer(&texture)))
	}
	return texture, com.Check("CreateTexture2D", hr)
}

// Describe returns the properties of a texture.
func Describe(texture unsafe.Pointer) TextureDesc {
	var desc TextureDesc
	com.Call(texture, 10, uintptr(unsafe.Pointer(&desc)))
	return desc
}

// Update replaces the contents of a texture from memory, pitch bytes per row.
func (d *Device) Update(texture unsafe.Pointer, pixels []byte, pitch int) {
	com.Call(d.Context, 48, uintptr(texture), 0, 0, uintptr(unsafe.Pointer(&pixels[0])), uintptr(pitch), 0)
}

// Copy copies one whole texture onto another of the same size and format.
func (d *Device) Copy(to, from unsafe.Pointer) {
	com.Call(d.Context, 47, uintptr(to), uintptr(from))
}

// Read copies a texture into memory, pitch bytes per row. It exists for
// tests and diagnostics: the recorder itself never brings frames back from
// the graphics chip.
func (d *Device) Read(texture unsafe.Pointer) (pixels []byte, pitch int, err error) {
	desc := Describe(texture)
	staging, err := d.NewTexture(int(desc.Width), int(desc.Height), desc.Format, UsageStaging, 0, AccessRead, nil, 0)
	if err != nil {
		return nil, 0, err
	}
	defer com.Release(&staging)
	d.Copy(staging, texture)
	var mapped struct {
		Data     unsafe.Pointer
		RowPitch uint32
		Depth    uint32
	}
	hr := com.Call(d.Context, 14, uintptr(staging), 0, 1, 0, uintptr(unsafe.Pointer(&mapped)))
	if err := com.Check("Map", hr); err != nil {
		return nil, 0, err
	}
	rows := int(desc.Height)
	if desc.Format == FormatNV12 {
		rows += rows / 2
	}
	size := int(mapped.RowPitch) * rows
	pixels = make([]byte, size)
	copy(pixels, unsafe.Slice((*byte)(mapped.Data), size))
	com.Call(d.Context, 15, uintptr(staging), 0)
	return pixels, int(mapped.RowPitch), nil
}

// references reports how many holders a COM object has.
func references(object unsafe.Pointer) uintptr {
	com.Call(object, 1)
	return com.Call(object, 2)
}
