//go:build windows

// Package dx wraps the parts of DXGI and Direct3D 11 that screen capture
// needs: finding the graphics adapter that drives a monitor, duplicating its
// desktop image, and converting frames on the GPU.
package dx

import (
	"syscall"
	"unsafe"

	"lazareplay/internal/com"
)

var (
	dxgi           = syscall.NewLazyDLL("dxgi.dll")
	createFactory1 = dxgi.NewProc("CreateDXGIFactory1")

	iidFactory1    = com.ID("770aae78-f26f-4dba-a829-253c83d1b387")
	iidOutput1     = com.ID("00cddea8-939b-4b83-a340-a685226666cc")
	iidTexture2D   = com.ID("6f15aaf2-d208-4e89-9ab4-489535d34f9c")
	iidMultithread = com.ID("9b7e4e00-342c-4106-a19f-4f2704f689f0")
)

// LUID is the locally unique ID Windows gives each graphics adapter.
type LUID struct {
	Low  uint32
	High int32
}

// adapterDesc mirrors DXGI_ADAPTER_DESC1.
type adapterDesc struct {
	Description           [128]uint16
	VendorID, DeviceID    uint32
	SubSysID, Revision    uint32
	DedicatedVideoMemory  uintptr
	DedicatedSystemMemory uintptr
	SharedSystemMemory    uintptr
	LUID                  LUID
	Flags                 uint32
}

// outputDesc mirrors DXGI_OUTPUT_DESC.
type outputDesc struct {
	DeviceName [32]uint16
	Desktop    Rect
	Attached   int32
	Rotation   uint32
	Monitor    uintptr
}

// Output is one monitor connected to an adapter.
type Output struct {
	Name     string
	Desktop  Rect
	Rotation uint32
	Monitor  uintptr
	object   unsafe.Pointer // IDXGIOutput
}

// Adapter is one graphics chip.
type Adapter struct {
	Name     string
	VendorID uint32
	LUID     LUID
	Software bool
	Outputs  []Output
	object   unsafe.Pointer // IDXGIAdapter1
}

// Vendor names the maker of the adapter.
func (a *Adapter) Vendor() string {
	switch a.VendorID {
	case 0x8086:
		return "Intel"
	case 0x10DE:
		return "NVIDIA"
	case 0x1002, 0x1022:
		return "AMD"
	}
	return "Other"
}

// ID returns the adapter's LUID as the eight bytes Media Foundation takes
// when asked for the encoders of one adapter.
func (a *Adapter) ID() *[8]byte {
	id := *(*[8]byte)(unsafe.Pointer(&a.LUID))
	return &id
}

// Adapters lists every graphics adapter with the monitors attached to it.
func Adapters() ([]*Adapter, error) {
	var factory unsafe.Pointer
	hr, _, _ := createFactory1.Call(uintptr(unsafe.Pointer(&iidFactory1)), uintptr(unsafe.Pointer(&factory)))
	if err := com.Check("CreateDXGIFactory1", hr); err != nil {
		return nil, err
	}
	defer com.Release(&factory)
	var list []*Adapter
	for i := uintptr(0); ; i++ {
		var object unsafe.Pointer
		if int32(com.Call(factory, 12, i, uintptr(unsafe.Pointer(&object)))) < 0 { // EnumAdapters1
			break
		}
		var desc adapterDesc
		com.Call(object, 10, uintptr(unsafe.Pointer(&desc))) // GetDesc1
		a := &Adapter{
			Name: com.Text(desc.Description[:]), VendorID: desc.VendorID, LUID: desc.LUID,
			Software: desc.Flags&2 != 0, object: object,
		}
		for j := uintptr(0); ; j++ {
			var out unsafe.Pointer
			if int32(com.Call(object, 7, j, uintptr(unsafe.Pointer(&out)))) < 0 { // EnumOutputs
				break
			}
			var od outputDesc
			com.Call(out, 7, uintptr(unsafe.Pointer(&od))) // GetDesc
			if od.Attached == 0 {
				com.Release(&out)
				continue
			}
			a.Outputs = append(a.Outputs, Output{
				Name: com.Text(od.DeviceName[:]), Desktop: od.Desktop, Rotation: od.Rotation, Monitor: od.Monitor, object: out,
			})
		}
		list = append(list, a)
	}
	return list, nil
}

// Close releases the adapter and its outputs.
func (a *Adapter) Close() {
	for i := range a.Outputs {
		com.Release(&a.Outputs[i].object)
	}
	com.Release(&a.object)
}

// Primary returns the adapter and output that show the main desktop: the
// monitor whose top-left corner is the origin of the desktop.
func Primary(list []*Adapter) (*Adapter, *Output) {
	var firstAdapter *Adapter
	var firstOutput *Output
	for _, a := range list {
		for i := range a.Outputs {
			o := &a.Outputs[i]
			if o.Desktop.Left == 0 && o.Desktop.Top == 0 {
				return a, o
			}
			if firstOutput == nil {
				firstAdapter, firstOutput = a, o
			}
		}
	}
	return firstAdapter, firstOutput
}
