//go:build windows

package dx

import (
	"unsafe"

	"lazareplay/internal/com"
)

// frameInfo mirrors DXGI_OUTDUPL_FRAME_INFO.
type frameInfo struct {
	LastPresentTime     int64
	LastMouseUpdateTime int64
	AccumulatedFrames   uint32
	RectsCoalesced      int32
	ProtectedMasked     int32
	PointerX, PointerY  int32
	PointerVisible      int32
	MetadataSize        uint32
	PointerShapeSize    uint32
}

// duplicationDesc mirrors DXGI_OUTDUPL_DESC.
type duplicationDesc struct {
	Width, Height      uint32
	RefreshN, RefreshD uint32
	Format             uint32
	ScanlineOrdering   uint32
	Scaling            uint32
	Rotation           uint32
	InSystemMemory     int32
}

// shapeInfo mirrors DXGI_OUTDUPL_POINTER_SHAPE_INFO.
type shapeInfo struct {
	Type, Width, Height, Pitch uint32
	HotX, HotY                 int32
}

// Pointer shape types.
const (
	ShapeMonochrome  = 1 // one bit per pixel: an AND mask above an XOR mask
	ShapeColour      = 2 // four bytes per pixel with transparency
	ShapeMaskedColor = 4 // four bytes per pixel, the fourth choosing replace or XOR
)

// Shape is the picture of the mouse pointer as Windows reports it.
type Shape struct {
	Type          uint32
	Width, Height int
	Pitch         int
	Data          []byte
}

// Duplication delivers the picture of one monitor as it changes. Windows
// calls this desktop duplication.
type Duplication struct {
	object  unsafe.Pointer // IDXGIOutputDuplication
	texture unsafe.Pointer // ID3D11Texture2D of the frame being held
	held    bool

	Width, Height int
	Rotation      uint32
	Refresh       float64

	// Accumulated is how many screen updates the last frame folds together.
	Accumulated int

	// Pointer state, kept current as frames arrive. PointerX and PointerY
	// are the top-left corner of the pointer picture on this monitor.
	PointerVisible bool
	PointerX       int
	PointerY       int
	Shape          *Shape
	ShapeSerial    int // counts changes of Shape
}

// Duplicate starts duplicating a monitor. The device must be on the adapter
// that drives it.
func (d *Device) Duplicate(o *Output) (*Duplication, error) {
	output, err := com.Query(o.object, &iidOutput1)
	if err != nil {
		return nil, err
	}
	defer com.Release(&output)
	dup := &Duplication{}
	hr := com.Call(output, 22, uintptr(d.Object), uintptr(unsafe.Pointer(&dup.object)))
	if err := com.Check("DuplicateOutput", hr); err != nil {
		return nil, err
	}
	var desc duplicationDesc
	com.Call(dup.object, 7, uintptr(unsafe.Pointer(&desc)))
	dup.Width, dup.Height, dup.Rotation = int(desc.Width), int(desc.Height), desc.Rotation
	if desc.RefreshD != 0 {
		dup.Refresh = float64(desc.RefreshN) / float64(desc.RefreshD)
	}
	return dup, nil
}

// Acquire lets go of the frame held since the previous call and asks for the
// current one, waiting up to wait milliseconds. It reports whether anything
// visible changed: the desktop picture, or a pointer that is or was showing.
//
// Keeping hold of a frame between calls is deliberate. While a frame is held
// Windows only notes which parts of the desktop change; it copies the latest
// picture once, when the next frame is asked for. Letting go early would
// have it copy every update, at the full refresh rate of the monitor.
func (d *Duplication) Acquire(wait uint32) (changed bool, err error) {
	d.Release()
	var info frameInfo
	var resource unsafe.Pointer
	hr := com.Call(d.object, 8, uintptr(wait), uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&resource)))
	if uint32(hr) == ErrWaitTimeout {
		return false, nil
	}
	if err := com.Check("AcquireNextFrame", hr); err != nil {
		return false, err
	}
	d.held = true
	d.texture, err = com.Query(resource, &iidTexture2D)
	com.Release(&resource)
	if err != nil {
		d.Release()
		return false, err
	}
	d.Accumulated = int(info.AccumulatedFrames)
	changed = info.LastPresentTime != 0
	if info.LastMouseUpdateTime != 0 {
		was := d.PointerVisible
		d.PointerVisible = info.PointerVisible != 0
		d.PointerX, d.PointerY = int(info.PointerX), int(info.PointerY)
		if info.PointerShapeSize != 0 {
			d.readShape(info.PointerShapeSize)
		}
		changed = changed || was || d.PointerVisible
	}
	return changed, nil
}

// Texture returns the desktop picture of the frame being held, or nil.
func (d *Duplication) Texture() unsafe.Pointer { return d.texture }

func (d *Duplication) readShape(size uint32) {
	data := make([]byte, size)
	var needed uint32
	var info shapeInfo
	hr := com.Call(d.object, 11, uintptr(size), uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&info)))
	if int32(hr) < 0 {
		return
	}
	d.Shape = &Shape{Type: info.Type, Width: int(info.Width), Height: int(info.Height), Pitch: int(info.Pitch), Data: data}
	d.ShapeSerial++
}

// Release lets go of the held frame.
func (d *Duplication) Release() {
	if d.held {
		com.Release(&d.texture)
		com.Call(d.object, 14)
		d.held = false
	}
}

// Close stops duplicating.
func (d *Duplication) Close() {
	d.Release()
	com.Release(&d.object)
}
