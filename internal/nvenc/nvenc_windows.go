//go:build windows

// Package nvenc drives the hardware video encoder of NVIDIA graphics cards
// directly, through the library every NVIDIA driver installs.
//
// Windows offers the same encoder through Media Foundation, but that route
// does not start on some machines, notably laptops that have a second
// graphics chip. This one does not depend on it. The structures below
// follow version 11.1 of NVIDIA's published interface, which drivers from
// mid-2021 onwards accept.
package nvenc

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"lazareplay/internal/com"
)

var (
	library        = syscall.NewLazyDLL("nvEncodeAPI64.dll")
	maxVersion     = library.NewProc("NvEncodeAPIGetMaxSupportedVersion")
	createInstance = library.NewProc("NvEncodeAPICreateInstance")
)

// Version numbers. Every structure carries one, built from the interface
// version and the structure's own revision.
const (
	apiVersion = 11 | 1<<24

	verFunctionList = apiVersion | 2<<16 | 7<<28
	verSession      = apiVersion | 1<<16 | 7<<28
	verPresetConfig = apiVersion | 4<<16 | 7<<28 | 1<<31
	verConfig       = apiVersion | 7<<16 | 7<<28 | 1<<31
	verRateControl  = apiVersion | 1<<16 | 7<<28
	verInitialize   = apiVersion | 5<<16 | 7<<28 | 1<<31
	verBitstream    = apiVersion | 1<<16 | 7<<28
	verRegister     = apiVersion | 3<<16 | 7<<28
	verMap          = apiVersion | 4<<16 | 7<<28
	verPicture      = apiVersion | 4<<16 | 7<<28 | 1<<31
	verLock         = apiVersion | 1<<16 | 7<<28
)

// Positions of the functions used in the table the library fills in.
const (
	fnInitialize       = 11
	fnCreateBitstream  = 14
	fnDestroyBitstream = 15
	fnEncodePicture    = 16
	fnLockBitstream    = 17
	fnUnlockBitstream  = 18
	fnMapResource      = 25
	fnUnmapResource    = 26
	fnDestroyEncoder   = 27
	fnOpenSession      = 29
	fnRegister         = 30
	fnUnregister       = 31
	fnLastError        = 37
	fnPresetConfig     = 39
)

var (
	codecH264   = com.ID("6bc82762-4e63-4ca4-aa85-1e50f321f6bf")
	profileHigh = com.ID("e7cbc309-4f7a-4b89-af2a-d537c92be310")
	presetP4    = com.ID("90a7b826-df06-4862-b9d2-cd6d73a08681") // the middle of seven speed settings
)

const (
	rateVariable = 1 // variable bitrate
	tuneLowDelay = 2 // one picture in, one picture out, in order
	formatNV12   = 1
	wholeFrame   = 1
	forceKey     = 0x2 | 0x4 // start a keyframe here and send the parameter sets with it
	pictureKey   = 3
	noLimit      = 0xFFFFFFFF
)

// functionList mirrors NV_ENCODE_API_FUNCTION_LIST.
type functionList struct {
	Version   uint32
	_         uint32
	Functions [41 + 277]uintptr
}

// sessionParams mirrors NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS.
type sessionParams struct {
	Version    uint32
	DeviceType uint32
	Device     unsafe.Pointer
	_          unsafe.Pointer
	APIVersion uint32
	_          [253]uint32
	_          [64]unsafe.Pointer
}

// rateControl mirrors NV_ENC_RC_PARAMS.
type rateControl struct {
	Version         uint32
	Mode            uint32
	ConstQP         [3]uint32
	AverageBitRate  uint32
	MaxBitRate      uint32
	VBVBufferSize   uint32
	VBVInitialDelay uint32
	Flags           uint32
	_               [9]uint32 // the three QP limits
	_               uint32
	_               [8]uint8
	_               [2]uint8
	LookaheadDepth  uint16
	_               [4]uint8
	_               [3]uint32
	_               [4]uint8
	_               [4]uint32
}

// usability mirrors NV_ENC_CONFIG_H264_VUI_PARAMETERS: what the stream says
// about its own colours.
type usability struct {
	_                  [2]uint32
	SignalTypePresent  uint32
	VideoFormat        uint32
	FullRange          uint32
	ColourDescribed    uint32
	Primaries          uint32
	Transfer           uint32
	Matrix             uint32
	_                  [3]uint32
	BitstreamRestricts uint32
	_                  [15]uint32
}

// h264Config mirrors NV_ENC_CONFIG_H264.
type h264Config struct {
	Flags     uint32 // bit 12: repeat the parameter sets with every keyframe
	Level     uint32
	IDRPeriod uint32
	_         [15]uint32
	Usability usability
	_         [7]uint32
	_         [267]uint32
	_         [64]unsafe.Pointer
}

// config mirrors NV_ENC_CONFIG.
type config struct {
	Version        uint32
	Profile        com.GUID
	GOPLength      uint32
	FrameIntervalP int32
	_              uint32
	FrameFieldMode uint32
	_              uint32
	Rate           rateControl
	H264           h264Config
	_              [278]uint32
	_              [64]unsafe.Pointer
}

// presetConfig mirrors NV_ENC_PRESET_CONFIG.
type presetConfig struct {
	Version uint32
	Config  config
	_       [255]uint32
	_       [64]unsafe.Pointer
}

// initialize mirrors NV_ENC_INITIALIZE_PARAMS.
type initialize struct {
	Version             uint32
	Codec               com.GUID
	Preset              com.GUID
	Width, Height       uint32
	AspectW, AspectH    uint32
	RateNum, RateDen    uint32
	Async               uint32
	DecidePictureTypes  uint32
	_                   uint32
	_                   uint32
	_                   unsafe.Pointer
	Config              *config
	MaxWidth, MaxHeight uint32
	_                   [8]uint32
	Tuning              uint32
	_                   uint32
	_                   [287]uint32
	_                   [64]unsafe.Pointer
}

// createBitstream mirrors NV_ENC_CREATE_BITSTREAM_BUFFER.
type createBitstream struct {
	Version uint32
	_       [3]uint32
	Buffer  unsafe.Pointer
	_       unsafe.Pointer
	_       [58]uint32
	_       [64]unsafe.Pointer
}

// registerResource mirrors NV_ENC_REGISTER_RESOURCE.
type registerResource struct {
	Version       uint32
	Kind          uint32
	Width, Height uint32
	Pitch         uint32
	_             uint32
	Resource      unsafe.Pointer
	Registered    unsafe.Pointer
	Format        uint32
	Usage         uint32
	_             [2]unsafe.Pointer
	_             [247]uint32
	_             [60]unsafe.Pointer
}

// mapResource mirrors NV_ENC_MAP_INPUT_RESOURCE.
type mapResource struct {
	Version    uint32
	_          uint32
	_          unsafe.Pointer
	Registered unsafe.Pointer
	Mapped     unsafe.Pointer
	Format     uint32
	_          [251]uint32
	_          [63]unsafe.Pointer
}

// picture mirrors the start of NV_ENC_PIC_PARAMS. The rest of that
// structure is left zero; the padding is larger than it needs to be.
type picture struct {
	Version       uint32
	Width, Height uint32
	Pitch         uint32
	Flags         uint32
	_             uint32
	Time          uint64
	Duration      uint64
	Input         unsafe.Pointer
	Output        unsafe.Pointer
	_             unsafe.Pointer
	Format        uint32
	Structure     uint32
	_             [8192]byte
}

// lock mirrors the start of NV_ENC_LOCK_BITSTREAM.
type lock struct {
	Version uint32
	_       uint32
	Output  unsafe.Pointer
	_       unsafe.Pointer
	_       [3]uint32
	Size    uint32
	Time    uint64
	_       uint64
	Data    unsafe.Pointer
	Kind    uint32
	_       [4096]byte
}

// Config describes the video to produce.
type Config struct {
	Width, Height int
	Rate          int // pictures per second
	Bitrate       int // average bits per second
	Peak          int // most bits per second over a short stretch
}

// job is a picture that has been handed over and not yet collected.
type job struct {
	texture unsafe.Pointer
	mapped  unsafe.Pointer
	output  unsafe.Pointer
}

// Encoder is one encoding session.
type Encoder struct {
	api      functionList
	session  unsafe.Pointer
	config   Config
	outputs  []unsafe.Pointer                  // buffers for compressed pictures, all of them
	spare    []unsafe.Pointer                  // those not in use
	textures map[unsafe.Pointer]unsafe.Pointer // each input texture's registration
	pending  []job
}

// call runs one function of the library.
//
//go:uintptrescapes
func (e *Encoder) call(function int, args ...uintptr) error {
	status, _, _ := syscall.SyscallN(e.api.Functions[function], args...)
	if uint32(status) == 0 {
		return nil
	}
	return &Error{Code: uint32(status), Detail: e.detail()}
}

// detail asks the library to describe its last failure.
func (e *Encoder) detail() string {
	if e.session == nil || e.api.Functions[fnLastError] == 0 {
		return ""
	}
	text, _, _ := syscall.SyscallN(e.api.Functions[fnLastError], uintptr(e.session))
	if text == 0 {
		return ""
	}
	var out []byte
	for p := *(*unsafe.Pointer)(unsafe.Pointer(&text)); len(out) < 200; p = unsafe.Add(p, 1) {
		b := *(*byte)(p)
		if b == 0 {
			break
		}
		out = append(out, b)
	}
	return string(out)
}

// Error is a failure reported by the encoder.
type Error struct {
	Code   uint32
	Detail string
}

func (e *Error) Error() string {
	names := map[uint32]string{
		1: "no encoder on this device", 2: "device not supported", 4: "invalid device", 8: "invalid setting",
		10: "out of memory, or too many encoding sessions at once", 12: "setting not supported", 15: "driver too old or too new for this interface",
		18: "encoder busy", 20: "encoder failure", 21: "the driver refused this client", 23: "texture could not be registered",
	}
	text := names[e.Code]
	if text == "" {
		text = "error"
	}
	if e.Detail != "" {
		text += ": " + e.Detail
	}
	return fmt.Sprintf("NVIDIA encoder: %s (%d)", text, e.Code)
}

// Available reports whether an NVIDIA driver with a recent enough encoder
// library is installed.
func Available() bool {
	if maxVersion.Find() != nil || createInstance.Find() != nil {
		return false
	}
	var version uint32
	if status, _, _ := maxVersion.Call(uintptr(unsafe.Pointer(&version))); status != 0 {
		return false
	}
	return version >= 11<<4|1 // the library counts versions as major times 16 plus minor
}

// Open starts a session on a Direct3D 11 device, which must be on an
// NVIDIA adapter. Pictures are given to it as NV12 textures of that device.
func Open(device unsafe.Pointer, c Config) (*Encoder, error) {
	if !Available() {
		return nil, errors.New("NVIDIA encoder: no suitable driver is installed")
	}
	e := &Encoder{config: c, textures: map[unsafe.Pointer]unsafe.Pointer{}}
	e.api.Version = verFunctionList
	if status, _, _ := createInstance.Call(uintptr(unsafe.Pointer(&e.api))); status != 0 {
		return nil, &Error{Code: uint32(status)}
	}
	for _, function := range []int{fnInitialize, fnCreateBitstream, fnDestroyBitstream, fnEncodePicture, fnLockBitstream, fnUnlockBitstream,
		fnMapResource, fnUnmapResource, fnDestroyEncoder, fnOpenSession, fnRegister, fnUnregister, fnPresetConfig} {
		if e.api.Functions[function] == 0 {
			return nil, errors.New("NVIDIA encoder: the driver's library is incomplete")
		}
	}
	session := sessionParams{Version: verSession, Device: device, APIVersion: apiVersion}
	if err := e.call(fnOpenSession, uintptr(unsafe.Pointer(&session)), uintptr(unsafe.Pointer(&e.session))); err != nil {
		e.session = nil
		return nil, err
	}
	if err := e.start(); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

func (e *Encoder) start() error {
	c := e.config
	// Begin from the driver's own settings for this speed, then change the
	// few that matter here.
	preset := &presetConfig{Version: verPresetConfig}
	preset.Config.Version = verConfig
	codec, speed := codecH264, presetP4
	if err := e.call(fnPresetConfig, uintptr(e.session), uintptr(unsafe.Pointer(&codec)), uintptr(unsafe.Pointer(&speed)), tuneLowDelay, uintptr(unsafe.Pointer(preset))); err != nil {
		return err
	}
	settings := &preset.Config
	settings.Version = verConfig
	settings.Profile = profileHigh
	settings.GOPLength = noLimit // keyframes are asked for by time, not by count
	settings.FrameIntervalP = 1  // no pictures that depend on later ones
	settings.FrameFieldMode = wholeFrame
	settings.Rate.Version = verRateControl
	settings.Rate.Mode = rateVariable
	settings.Rate.AverageBitRate = uint32(c.Bitrate)
	settings.Rate.MaxBitRate = uint32(c.Peak)
	settings.Rate.VBVBufferSize = uint32(c.Peak)
	settings.Rate.VBVInitialDelay = uint32(c.Peak)
	settings.Rate.LookaheadDepth = 0
	settings.Rate.Flags &^= 1 << 5 // no looking ahead
	settings.H264.IDRPeriod = noLimit
	settings.H264.Flags |= 1 << 12 // parameter sets with every keyframe
	// Say in the stream itself that the colours are BT.709 at studio range.
	settings.H264.Usability.SignalTypePresent = 1
	settings.H264.Usability.VideoFormat = 5
	settings.H264.Usability.FullRange = 0
	settings.H264.Usability.ColourDescribed = 1
	settings.H264.Usability.Primaries, settings.H264.Usability.Transfer, settings.H264.Usability.Matrix = 1, 1, 1

	params := &initialize{
		Version: verInitialize, Codec: codecH264, Preset: presetP4,
		Width: uint32(c.Width), Height: uint32(c.Height), AspectW: uint32(c.Width), AspectH: uint32(c.Height),
		RateNum: uint32(c.Rate), RateDen: 1, DecidePictureTypes: 1, Config: settings,
		MaxWidth: uint32(c.Width), MaxHeight: uint32(c.Height), Tuning: tuneLowDelay,
	}
	if err := e.call(fnInitialize, uintptr(e.session), uintptr(unsafe.Pointer(params))); err != nil {
		return err
	}
	for i := 0; i < 4; i++ {
		buffer := createBitstream{Version: verBitstream}
		if err := e.call(fnCreateBitstream, uintptr(e.session), uintptr(unsafe.Pointer(&buffer))); err != nil {
			return err
		}
		e.outputs = append(e.outputs, buffer.Buffer)
	}
	e.spare = append(e.spare, e.outputs...)
	return nil
}

// Accepting reports whether the encoder will take a picture now.
func (e *Encoder) Accepting() (bool, error) { return len(e.spare) > 0, nil }

// Holding reports whether the encoder is still reading a texture.
func (e *Encoder) Holding(texture unsafe.Pointer) bool {
	for _, j := range e.pending {
		if j.texture == texture {
			return true
		}
	}
	return false
}

// registration returns the encoder's handle on an input texture.
func (e *Encoder) registration(texture unsafe.Pointer) (unsafe.Pointer, error) {
	if r, ok := e.textures[texture]; ok {
		return r, nil
	}
	if len(e.textures) >= 16 {
		// Textures from before the screen changed size. Let go of those
		// that are not in use.
		for old, r := range e.textures {
			if !e.Holding(old) {
				e.call(fnUnregister, uintptr(e.session), uintptr(r))
				delete(e.textures, old)
			}
		}
	}
	r := registerResource{
		Version: verRegister, Width: uint32(e.config.Width), Height: uint32(e.config.Height),
		Resource: texture, Format: formatNV12,
	}
	if err := e.call(fnRegister, uintptr(e.session), uintptr(unsafe.Pointer(&r))); err != nil {
		return nil, err
	}
	e.textures[texture] = r.Registered
	return r.Registered, nil
}

// Submit hands the encoder one NV12 texture. time and duration are in
// 100 ns units. With key set the picture is made a keyframe.
func (e *Encoder) Submit(texture unsafe.Pointer, time, duration int64, key bool) error {
	if len(e.spare) == 0 {
		return errors.New("NVIDIA encoder: no room for another picture")
	}
	registered, err := e.registration(texture)
	if err != nil {
		return err
	}
	m := mapResource{Version: verMap, Registered: registered}
	if err := e.call(fnMapResource, uintptr(e.session), uintptr(unsafe.Pointer(&m))); err != nil {
		return err
	}
	output := e.spare[len(e.spare)-1]
	p := &picture{
		Version: verPicture, Width: uint32(e.config.Width), Height: uint32(e.config.Height), Pitch: uint32(e.config.Width),
		Time: uint64(time), Duration: uint64(duration), Input: m.Mapped, Output: output, Format: m.Format, Structure: wholeFrame,
	}
	if key {
		p.Flags = forceKey
	}
	if err := e.call(fnEncodePicture, uintptr(e.session), uintptr(unsafe.Pointer(p))); err != nil {
		e.call(fnUnmapResource, uintptr(e.session), uintptr(m.Mapped))
		return err
	}
	e.spare = e.spare[:len(e.spare)-1]
	e.pending = append(e.pending, job{texture: texture, mapped: m.Mapped, output: output})
	return nil
}

// Collect passes every picture handed over so far to take, oldest first.
// The bytes are only valid during the call. It waits for a picture that is
// still being compressed, which takes a few milliseconds, so it is meant to
// be called a tick after Submit, by when the picture is done.
func (e *Encoder) Collect(take func(data []byte, time int64)) error {
	for len(e.pending) > 0 {
		j := e.pending[0]
		l := &lock{Version: verLock, Output: j.output}
		err := e.call(fnLockBitstream, uintptr(e.session), uintptr(unsafe.Pointer(l)))
		if err == nil {
			if l.Size > 0 && l.Data != nil {
				take(unsafe.Slice((*byte)(l.Data), l.Size), int64(l.Time))
			}
			e.call(fnUnlockBitstream, uintptr(e.session), uintptr(j.output))
		}
		e.call(fnUnmapResource, uintptr(e.session), uintptr(j.mapped))
		e.spare = append(e.spare, j.output)
		e.pending = e.pending[1:]
		if err != nil {
			return err
		}
	}
	return nil
}

// Close ends the session.
func (e *Encoder) Close() {
	if e.session == nil {
		return
	}
	for _, j := range e.pending {
		e.call(fnUnmapResource, uintptr(e.session), uintptr(j.mapped))
	}
	for _, r := range e.textures {
		e.call(fnUnregister, uintptr(e.session), uintptr(r))
	}
	for _, output := range e.outputs {
		e.call(fnDestroyBitstream, uintptr(e.session), uintptr(output))
	}
	e.call(fnDestroyEncoder, uintptr(e.session))
	e.session, e.pending, e.textures, e.outputs, e.spare = nil, nil, nil, nil, nil
}
