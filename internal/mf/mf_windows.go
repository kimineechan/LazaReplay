//go:build windows

// Package mf wraps the parts of Windows Media Foundation that the recorder
// uses: finding the hardware video encoder of a graphics adapter, feeding it
// frames that stay on the GPU, and reading the compressed video it returns.
package mf

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"lazareplay/internal/com"
)

var (
	mfplat           = syscall.NewLazyDLL("mfplat.dll")
	mfStartup        = mfplat.NewProc("MFStartup")
	mfShutdown       = mfplat.NewProc("MFShutdown")
	mfCreateAttrs    = mfplat.NewProc("MFCreateAttributes")
	mfCreateType     = mfplat.NewProc("MFCreateMediaType")
	mfCreateSample   = mfplat.NewProc("MFCreateSample")
	mfCreateMemory   = mfplat.NewProc("MFCreateMemoryBuffer")
	mfCreateSurface  = mfplat.NewProc("MFCreateDXGISurfaceBuffer")
	mfCreateManager  = mfplat.NewProc("MFCreateDXGIDeviceManager")
	mfEnum2          = mfplat.NewProc("MFTEnum2")
	mfEnumEx         = mfplat.NewProc("MFTEnumEx")
	mfVersion        = uintptr(0x00020070)
	errNoMoreEvents  = uint32(0xC00D3E80) // MF_E_NO_EVENTS_AVAILABLE
	errNeedMoreInput = uint32(0xC00D6D72) // MF_E_TRANSFORM_NEED_MORE_INPUT
	errStreamChange  = uint32(0xC00D6D61) // MF_E_TRANSFORM_STREAM_CHANGE
	errNotAccepting  = uint32(0xC00D36B5) // MF_E_NOTACCEPTING
)

// Attribute keys, media types and formats.
var (
	MajorType        = com.ID("48eba18e-f8c9-4687-bf11-0a74c9f96a8f")
	Subtype          = com.ID("f7e34c9a-42e8-4714-b74b-cb29d72c35e5")
	AvgBitrate       = com.ID("20332624-fb0d-4d9e-bd0d-cbf6786c102e")
	FrameSize        = com.ID("1652c33d-d6b2-4012-b834-72030849a37d")
	FrameRate        = com.ID("c459a2e8-3d2c-4e44-b132-fee5156c7bb0")
	InterlaceMode    = com.ID("e2724bb8-e676-4806-b4b2-a8d6efb44ccd")
	PixelAspectRatio = com.ID("c6376a1e-8d0a-4027-be45-6d9a0ad39bb6")
	Profile          = com.ID("ad76a80b-2d5c-4e0b-b375-64e520137036")
	SequenceHeader   = com.ID("3c036de7-3ad0-4c9e-9216-ee6d6ac21cb3")
	AudioChannels    = com.ID("37e48bf5-645e-4c5b-89de-ada9e29b696a")
	AudioSampleRate  = com.ID("5faeeae7-0290-4c31-9e8a-c534f68d9dba")
	AudioBits        = com.ID("f2deb57f-40fa-4764-aa33-ed4f2d1ff669")
	AudioBytesPerSec = com.ID("1aab75c8-cfef-451c-ab95-ac034b8e1731")
	AudioBlockAlign  = com.ID("322de230-9eeb-43bd-ab7a-ff412251541d")
	AACPayloadType   = com.ID("bfbabe79-7434-4d1c-94f0-72a3b9e17188")
	AACProfileLevel  = com.ID("7632f0e6-9538-4d61-acda-ea29c8c14456")
	AsyncUnlock      = com.ID("e5666d6b-3422-4eb6-a421-da7db1f8e207")
	Async            = com.ID("f81a699a-649a-497d-8c73-29f8fed6ad7a")
	D3D11Aware       = com.ID("206b4fc8-fcf9-4c51-afe3-9764369e33a0")
	LowLatency       = com.ID("9c27891a-ed7a-40e1-88e8-b22727a024ee")
	FriendlyName     = com.ID("314ffbae-5b41-4c95-9c19-4e7d586face3")
	EnumAdapterLUID  = com.ID("1d39518c-e220-4da8-a07f-ba172552d6b1")

	TypeVideo = com.ID("73646976-0000-0010-8000-00aa00389b71")
	TypeAudio = com.ID("73647561-0000-0010-8000-00aa00389b71")

	FormatH264 = com.Format(com.FourCC("H264"))
	FormatNV12 = com.Format(com.FourCC("NV12"))
	FormatPCM  = com.Format(1)
	FormatAAC  = com.Format(0x1610)

	hardwareVendor       = com.ID("3aecb0cc-035b-4bcc-8185-2b8d551ef3af")
	categoryVideoEncoder = com.ID("f79eac7d-e545-4387-bdee-d647d7bde42a")
	categoryAudioEncoder = com.ID("91c64bd0-f91e-4d8c-9276-db248279d975")
)

// Startup initialises Media Foundation for the process. It fails with a
// plain explanation on the editions of Windows that ship without it.
func Startup() error {
	if err := mfStartup.Find(); err != nil {
		return errors.New("The media features of Windows are missing. On an N or KN edition of Windows, add the Media Feature Pack under Settings, Apps, Optional features, then start LazaReplay again.")
	}
	hr, _, _ := mfStartup.Call(mfVersion, 0)
	return com.Check("MFStartup", hr)
}

// Shutdown releases Media Foundation.
func Shutdown() { mfShutdown.Call() }

// Attributes is a key-value store. Media types, samples, events and
// activation objects all begin with this interface.
type Attributes struct{ P unsafe.Pointer }

// NewAttributes creates an empty store.
func NewAttributes() (Attributes, error) {
	var a Attributes
	hr, _, _ := mfCreateAttrs.Call(uintptr(unsafe.Pointer(&a.P)), 4)
	return a, com.Check("MFCreateAttributes", hr)
}

// NewMediaType creates an empty media type.
func NewMediaType() (Attributes, error) {
	var a Attributes
	hr, _, _ := mfCreateType.Call(uintptr(unsafe.Pointer(&a.P)))
	return a, com.Check("MFCreateMediaType", hr)
}

func (a *Attributes) Release() { com.Release(&a.P) }

func (a Attributes) SetUINT32(key *com.GUID, v uint32) error {
	return com.Check("SetUINT32", com.Call(a.P, 21, uintptr(unsafe.Pointer(key)), uintptr(v)))
}

func (a Attributes) SetUINT64(key *com.GUID, v uint64) error {
	return com.Check("SetUINT64", com.Call(a.P, 22, uintptr(unsafe.Pointer(key)), uintptr(v)))
}

// SetPair stores two 32-bit numbers in one value, as frame sizes and rates are kept.
func (a Attributes) SetPair(key *com.GUID, high, low uint32) error {
	return a.SetUINT64(key, uint64(high)<<32|uint64(low))
}

func (a Attributes) SetGUID(key, value *com.GUID) error {
	return com.Check("SetGUID", com.Call(a.P, 24, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(value))))
}

func (a Attributes) SetBlob(key *com.GUID, data []byte) error {
	return com.Check("SetBlob", com.Call(a.P, 26, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data))))
}

func (a Attributes) UINT32(key *com.GUID) (uint32, bool) {
	var v uint32
	hr := com.Call(a.P, 7, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&v)))
	return v, int32(hr) >= 0
}

func (a Attributes) UINT64(key *com.GUID) (uint64, bool) {
	var v uint64
	hr := com.Call(a.P, 8, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&v)))
	return v, int32(hr) >= 0
}

func (a Attributes) GUID(key *com.GUID) (com.GUID, bool) {
	var v com.GUID
	hr := com.Call(a.P, 10, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&v)))
	return v, int32(hr) >= 0
}

// Blob returns a copy of a binary value, or nil when the key is absent.
func (a Attributes) Blob(key *com.GUID) []byte {
	var size uint32
	if int32(com.Call(a.P, 14, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&size)))) < 0 || size == 0 {
		return nil
	}
	out := make([]byte, size)
	if int32(com.Call(a.P, 15, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&out[0])), uintptr(size), 0)) < 0 {
		return nil
	}
	return out
}

// String returns a text value, or "" when the key is absent.
func (a Attributes) String(key *com.GUID) string {
	var text *uint16
	var length uint32
	if int32(com.Call(a.P, 13, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&text)), uintptr(unsafe.Pointer(&length)))) < 0 || text == nil {
		return ""
	}
	defer com.Free(unsafe.Pointer(text))
	return syscall.UTF16ToString(unsafe.Slice(text, length))
}

// typeInfo mirrors MFT_REGISTER_TYPE_INFO.
type typeInfo struct{ Major, Sub com.GUID }

// Codec is a video encoder that Windows offers, not yet started.
type Codec struct {
	Name     string
	Hardware bool
	activate Attributes
}

func (c *Codec) Release() { c.activate.Release() }

// VideoEncoders lists the H.264 encoders. With an adapter ID it returns the
// hardware encoders of that graphics adapter, whose maker is given by its
// PCI vendor number; with nil it returns the software encoders that run on
// the processor.
func VideoEncoders(adapter *[8]byte, vendor uint32) ([]*Codec, error) {
	out := typeInfo{TypeVideo, FormatH264}
	var list *unsafe.Pointer
	var count uint32
	flags := uintptr(0x1 | 0x2 | 0x40) // synchronous and asynchronous, sorted
	var hr uintptr
	if adapter != nil {
		attrs, err := NewAttributes()
		if err != nil {
			return nil, err
		}
		defer attrs.Release()
		if err := attrs.SetBlob(&EnumAdapterLUID, adapter[:]); err != nil {
			return nil, err
		}
		flags = 0x4 | 0x40 // hardware, sorted
		if mfEnum2.Find() == nil {
			hr, _, _ = mfEnum2.Call(uintptr(unsafe.Pointer(&categoryVideoEncoder)), flags, 0, uintptr(unsafe.Pointer(&out)),
				uintptr(attrs.P), uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&count)))
		} else {
			// Early versions of Windows 10 cannot ask per adapter. List every
			// hardware encoder; those of other makers are dropped below.
			hr, _, _ = mfEnumEx.Call(uintptr(unsafe.Pointer(&categoryVideoEncoder)), flags, 0, uintptr(unsafe.Pointer(&out)),
				uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&count)))
		}
	} else {
		hr, _, _ = mfEnumEx.Call(uintptr(unsafe.Pointer(&categoryVideoEncoder)), flags, 0, uintptr(unsafe.Pointer(&out)),
			uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&count)))
	}
	if err := com.Check("MFTEnum", hr); err != nil {
		return nil, err
	}
	defer com.Free(unsafe.Pointer(list))
	var found []*Codec
	for _, p := range unsafe.Slice(list, count) {
		a := Attributes{p}
		if maker := a.String(&hardwareVendor); adapter != nil && maker != "" && !strings.EqualFold(maker, fmt.Sprintf("VEN_%04X", vendor)) {
			a.Release()
			continue
		}
		found = append(found, &Codec{Name: a.String(&FriendlyName), Hardware: adapter != nil, activate: a})
	}
	return found, nil
}
