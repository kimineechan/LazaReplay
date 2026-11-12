//go:build windows

// Package recorder runs the whole pipeline: it captures the screen,
// compresses it on the graphics chip, keeps the last few minutes in memory
// and writes a clip to disk when asked.
package recorder

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"lazareplay/internal/audio"
	"lazareplay/internal/clock"
	"lazareplay/internal/com"
	"lazareplay/internal/dx"
	"lazareplay/internal/h264"
	"lazareplay/internal/mf"
	"lazareplay/internal/nvenc"
	"lazareplay/internal/ring"
)

// Screen is where pictures come from: a monitor, or the generated test card
// the self-test records instead.
type Screen interface {
	Size() (width, height int)
	// Acquire fetches the current picture, waiting up to wait milliseconds
	// for a new one, and reports whether anything changed.
	Acquire(wait uint32) (changed bool, err error)
	Texture() unsafe.Pointer // the picture fetched last, in BGRA
	Pointer() dx.Pointer     // the mouse pointer as of that picture
	Close()
}

// Options are the choices the user makes.
type Options struct {
	Window  time.Duration // how much to keep
	Rate    int           // pictures per second, 30 or 60
	Quality int           // 0 low, 1 medium, 2 high
	Height  int           // tallest picture to record; 0 keeps the screen's own size
	Monitor string        // Windows name of the monitor; "" for the main screen
	Sound   bool          // also record what the computer plays

	// Source, when set, supplies the pictures in place of a monitor, and
	// SoundSource the sound in place of what the computer plays. The
	// self-test uses them to record material it can check. Both are given
	// the time that counts as zero for the recording.
	Source      func(device *dx.Device, epoch int64) (Screen, error)
	SoundSource func(epoch int64) (audio.Input, error)
	// Software forces the encoder that runs on the processor, for testing.
	Software bool
}

// megabits per second for a 1920 by 1080 picture at 60 per second.
var qualityRates = [3]float64{5, 10, 18}

// Bitrate returns the average bits per second for a picture of the given
// size and rate at the chosen quality.
func (o Options) Bitrate(width, height, rate int) int {
	q := min(max(o.Quality, 0), len(qualityRates)-1)
	bits := qualityRates[q] * 1e6 * float64(width*height) / (1920 * 1080)
	if rate <= 30 {
		bits *= 0.6
	}
	return max(int(bits), 500_000)
}

// Memory estimates the most memory the loop will use, in bytes, for a
// screen of the given size.
func (o Options) Memory(screenWidth, screenHeight int) int {
	width, height := o.Size(screenWidth, screenHeight)
	bits := o.Bitrate(width, height, o.Rate)
	if o.Sound {
		bits += soundBitrate
	}
	return int(float64(bits) / 8 * o.Window.Seconds())
}

// Size returns the picture size the options give for a screen. Hardware
// encoders stop at about 4096 by 2160, so nothing larger is asked of them.
func (o Options) Size(screenWidth, screenHeight int) (width, height int) {
	limit := o.Height
	if limit <= 0 || limit > 2160 {
		limit = 2160
	}
	return within(screenWidth, screenHeight, 4096, limit)
}

// within scales a size down, keeping its shape, until it fits the limits.
// Both results are even, as the video format requires.
func within(width, height, maxWidth, maxHeight int) (int, int) {
	if height > maxHeight {
		width, height = width*maxHeight/height, maxHeight
	}
	if width > maxWidth {
		width, height = maxWidth, height*maxWidth/width
	}
	return max(width&^1, 2), max(height&^1, 2)
}

// State says what the recorder is doing.
type State int

const (
	Starting  State = iota // setting up
	Recording              // capturing and keeping footage
	Waiting                // the screen cannot be captured just now
	Failed                 // something is wrong; it will try again
	Stopped
)

// Status is a picture of the recorder at one moment.
type Status struct {
	State    State
	Problem  string        // in plain words, when State is Waiting or Failed
	Screen   string        // Windows name of the monitor being recorded
	Width    int           // size of the recorded picture
	Height   int           //
	Rate     int           // pictures per second being recorded
	Encoder  string        // name of the video encoder in use
	Hardware bool          // whether it runs on the graphics chip
	Pointer  bool          // whether the mouse pointer can be drawn
	Rotated  bool          // the screen is turned; it is recorded as the panel sees it
	Sound    bool          // whether sound is being recorded
	Silent   string        // why not, in plain words, when it was asked for
	Length   time.Duration // footage held
	Bytes    int           // memory it takes
	Frames   uint64        // pictures recorded since the start
	Late     uint64        // pictures that had to wait for the encoder
}

const (
	keyGap       = 2 * clock.Second // a keyframe at least this often
	heartbeatGap = clock.Second     // a picture at least this often, even of a still screen
	soundBitrate = 160_000
)

// Recorder keeps the loop running until stopped.
type Recorder struct {
	options Options
	ring    *ring.Ring
	epoch   int64 // time zero of every timestamp
	stop    chan struct{}
	done    sync.WaitGroup

	mu     sync.Mutex
	status Status

	frames, late atomic.Uint64
	sound        atomic.Pointer[soundFormat] // set once sound has been recorded

	// Used only by the capture thread.
	scratch  []byte
	sps, pps []byte
	lastOut  int64
	avoid    map[string]int // encoders that failed soon after starting, and how often
}

// Start begins recording. It returns at once; progress is reported by
// Status. Media Foundation must already be started.
func Start(options Options) *Recorder {
	if options.Rate != 30 {
		options.Rate = 60
	}
	if options.Window <= 0 {
		options.Window = 5 * time.Minute
	}
	r := &Recorder{
		options: options,
		ring:    ring.New(options.Window.Nanoseconds()/100, clock.Second/int64(options.Rate), 0),
		epoch:   clock.Now(),
		stop:    make(chan struct{}),
		avoid:   map[string]int{},
	}
	r.done.Add(1)
	go r.run()
	if options.Sound {
		r.done.Add(1)
		go r.listen()
	}
	return r
}

// Stop ends recording and wipes the loop from memory.
func (r *Recorder) Stop() {
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
	r.done.Wait()
	r.ring.Reset()
	r.set(func(s *Status) { s.State, s.Problem = Stopped, "" })
}

// Status reports what the recorder is doing and how much it holds.
func (r *Recorder) Status() Status {
	r.mu.Lock()
	s := r.status
	r.mu.Unlock()
	length, size := r.ring.Stats()
	s.Length, s.Bytes = time.Duration(length*100), size
	s.Frames, s.Late = r.frames.Load(), r.late.Load()
	return s
}

func (r *Recorder) set(change func(*Status)) {
	r.mu.Lock()
	change(&r.status)
	r.mu.Unlock()
}

func (r *Recorder) stopped() bool {
	select {
	case <-r.stop:
		return true
	default:
		return false
	}
}

var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	getCurrentThread  = kernel32.NewProc("GetCurrentThread")
	setThreadPriority = kernel32.NewProc("SetThreadPriority")
)

// Profile adds up, in 100 ns units, where the capture thread spends its
// time: collecting from the encoder, fetching the picture (which includes
// waiting for one), drawing the pointer, converting, handing over to the
// encoder, and flushing. The seventh entry counts pictures and the eighth is
// the processor time the capture thread has used. It is filled only when
// the environment variable LAZAREPLAY_PROFILE is set.
var Profile [8]atomic.Int64

var profiling = os.Getenv("LAZAREPLAY_PROFILE") != ""

// lap adds the time since *from to a phase and moves *from to now.
func lap(phase int, from *int64) {
	if profiling {
		now := clock.Now()
		Profile[phase].Add(now - *from)
		*from = now
	}
}

// fault marks a failure that came from the video encoder.
type fault struct {
	encoder string
	err     error
}

func (f *fault) Error() string { return f.err.Error() }
func (f *fault) Unwrap() error { return f.err }

// run is the capture thread. It keeps a session going and starts a fresh
// one when a session fails, so a driver update or a lost device is ridden
// out without the user doing anything.
func (r *Recorder) run() {
	defer r.done.Done()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	com.Init()
	// The thread works for a fraction of a millisecond per picture. Raising
	// its priority one step keeps that prompt when a game has every core busy.
	thread, _, _ := getCurrentThread.Call()
	setThreadPriority.Call(thread, 1)
	for !r.stopped() {
		r.set(func(s *Status) {
			if s.State != Failed {
				s.State = Starting
			}
		})
		began := time.Now()
		err := r.session()
		if r.stopped() {
			return
		}
		if err != nil {
			r.set(func(s *Status) { s.State, s.Problem = Failed, explain(err) })
			// An encoder that breaks down soon after starting, twice, is
			// passed over from then on in favour of the next one. That is
			// how the recorder copes with hardware it has never met.
			var broken *fault
			lost := com.Code(err) == dx.ErrDeviceRemoved || com.Code(err) == dx.ErrDeviceReset
			if errors.As(err, &broken) && !lost {
				if time.Since(began) < 30*time.Second {
					r.avoid[broken.encoder]++
				} else {
					r.avoid[broken.encoder] = 0
				}
			}
		}
		select {
		case <-r.stop:
			return
		case <-time.After(time.Second):
		}
	}
}

// explain puts a failure in plain words.
func explain(err error) string {
	switch com.Code(err) {
	case dx.ErrDeviceRemoved, dx.ErrDeviceReset:
		return "The graphics driver restarted. Recording will resume by itself."
	case dx.ErrAccessDenied:
		return "Windows is showing a protected screen. Recording will resume by itself."
	case 0x887A0004: // DXGI_ERROR_UNSUPPORTED
		return "This screen cannot be captured, for example over Remote Desktop."
	case 0x887A0022: // DXGI_ERROR_NOT_CURRENTLY_AVAILABLE
		return "Too many programs are capturing the screen at once."
	}
	return err.Error()
}

// videoEncoder is what the recorder needs of a video encoder. Two kinds
// stand behind it: the encoders Windows offers through Media Foundation,
// and NVIDIA's own, driven directly.
type videoEncoder interface {
	Label() string  // its name, for the status
	OnChip() bool   // whether it runs on the graphics chip
	Header() []byte // parameter sets known before the first picture, if any
	Accepting() (bool, error)
	Free(t *dx.Target) bool // whether the encoder has finished reading a texture
	Submit(texture unsafe.Pointer, time, duration int64, key bool) error
	Collect(take func(data []byte, time int64)) error
	Close()
}

// foundation adapts a Media Foundation encoder. It holds a reference on a
// texture for as long as it reads it.
type foundation struct{ *mf.Encoder }

func (f foundation) Label() string          { return f.Name }
func (f foundation) OnChip() bool           { return f.Hardware }
func (f foundation) Free(t *dx.Target) bool { return t.Idle() }

// direct adapts NVIDIA's encoder, which keeps its own list of the textures
// it is reading.
type direct struct{ *nvenc.Encoder }

const directName = "NVIDIA NVENC H.264 Encoder"

func (d direct) Label() string          { return directName }
func (d direct) OnChip() bool           { return true }
func (d direct) Header() []byte         { return nil }
func (d direct) Free(t *dx.Target) bool { return !d.Holding(t.Texture) }

// pipeline holds what one session uses.
type pipeline struct {
	device    *dx.Device
	output    *dx.Output
	screen    Screen
	painter   *dx.Painter // nil when the pointer cannot be drawn
	converter *dx.Converter
	targets   []*dx.Target
	last      *dx.Target // the latest converted picture
	next      int
	encoder   videoEncoder
	width     int // recorded picture size
	height    int
	rate      int // pictures per second
}

func (p *pipeline) close() {
	if p.encoder != nil {
		p.encoder.Close()
	}
	p.dropScreen()
	p.dropConverter()
	if p.device != nil {
		p.device.Close()
	}
}

// dropScreen lets go of the screen. The converted pictures stay, so the
// last one can go on being repeated while the screen is away.
func (p *pipeline) dropScreen() {
	if p.converter != nil {
		p.converter.Forget()
	}
	p.painter.Close()
	p.painter = nil
	if p.screen != nil {
		p.screen.Close()
		p.screen = nil
	}
}

// dropConverter lets go of everything sized to the screen.
func (p *pipeline) dropConverter() {
	for _, t := range p.targets {
		t.Close()
	}
	p.targets, p.last = nil, nil
	if p.converter != nil {
		p.converter.Close()
		p.converter = nil
	}
}

// attach starts capturing the screen.
func (p *pipeline) attach(o Options, epoch int64) error {
	var err error
	if o.Source != nil {
		p.screen, err = o.Source(p.device, epoch)
	} else {
		var dup *dx.Duplication
		if dup, err = p.device.Duplicate(p.output); err == nil {
			p.screen = dup
		}
	}
	if err != nil {
		return err
	}
	// Windows reports the pointer in the coordinates of the desktop. On a
	// turned screen those are not the coordinates of the picture, so the
	// pointer is left out there. Without the painter the recording still
	// works; it just has no pointer.
	if o.Source != nil || !p.rotated() {
		width, height := p.screen.Size()
		p.painter, _ = p.device.NewPainter(width, height)
	}
	return nil
}

// rotated reports whether the monitor is turned on its side or upside down.
func (p *pipeline) rotated() bool { return p.output != nil && p.output.Rotation > 1 }

// convert prepares conversion from the screen's size to the recorded size.
func (p *pipeline) convert() error {
	width, height := p.screen.Size()
	if p.converter != nil && p.converter.InWidth == width && p.converter.InHeight == height {
		return nil
	}
	// First use, or the screen came back at another size. The recorded size
	// stays what it was; the converter scales to it.
	p.dropConverter()
	var err error
	if p.converter, err = p.device.NewConverter(width, height, p.width, p.height, p.rate); err != nil {
		return err
	}
	for i := 0; i < 6; i++ {
		t, err := p.converter.NewTarget()
		if err != nil {
			return err
		}
		p.targets = append(p.targets, t)
	}
	return nil
}

// free returns a texture the encoder is not reading, or nil.
func (p *pipeline) free() *dx.Target {
	for range p.targets {
		p.next = (p.next + 1) % len(p.targets)
		if t := p.targets[p.next]; p.encoder.Free(t) {
			return t
		}
	}
	return nil
}

// pick finds the monitor with the given name, or the main screen.
func pick(adapters []*dx.Adapter, name string) (*dx.Adapter, *dx.Output) {
	for _, a := range adapters {
		for i := range a.Outputs {
			if name != "" && a.Outputs[i].Name == name {
				return a, &a.Outputs[i]
			}
		}
	}
	return dx.Primary(adapters)
}

// Monitor describes a screen that can be recorded.
type Monitor struct {
	Name          string // Windows' own name for it, as Options.Monitor takes it
	Width, Height int
	Main          bool
	Adapter       string // the graphics chip it is plugged into
	left          int32
}

// Monitors lists the screens that are connected, the main one first, then
// from left to right.
func Monitors() []Monitor {
	adapters, err := dx.Adapters()
	if err != nil {
		return nil
	}
	var list []Monitor
	for _, a := range adapters {
		for _, o := range a.Outputs {
			list = append(list, Monitor{
				Name: o.Name, Width: o.Desktop.Width(), Height: o.Desktop.Height(),
				Main: o.Desktop.Left == 0 && o.Desktop.Top == 0, Adapter: a.Name, left: o.Desktop.Left,
			})
		}
		a.Close()
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Main != list[j].Main {
			return list[i].Main
		}
		return list[i].left < list[j].left
	})
	return list
}

// choose starts a video encoder. It prefers the hardware encoder of the
// chip that drives the screen, since that one reads pictures where they
// already are, and falls back to the software encoder that every copy of
// Windows has.
func (r *Recorder) choose(p *pipeline, adapter *dx.Adapter) error {
	o := r.options
	// candidate is an encoder that could be started.
	type candidate struct {
		Name     string
		Hardware bool
		Open     func(config mf.Config) (videoEncoder, error)
	}
	var list []candidate
	var codecs []*mf.Codec
	if !o.Software {
		if adapter.VendorID == 0x10DE && nvenc.Available() {
			// NVIDIA's encoder, reached directly. It is tried before the
			// one Windows lists because that one fails to start on some
			// machines, laptops with two graphics chips among them.
			list = append(list, candidate{directName, true, func(c mf.Config) (videoEncoder, error) {
				e, err := nvenc.Open(p.device.Object, nvenc.Config{Width: c.Width, Height: c.Height, Rate: c.Rate, Bitrate: c.Bitrate, Peak: c.Peak})
				if err != nil {
					return nil, err
				}
				return direct{e}, nil
			}})
		}
		codecs, _ = mf.VideoEncoders(adapter.ID(), adapter.VendorID)
	}
	software, _ := mf.VideoEncoders(nil, 0)
	codecs = append(codecs, software...)
	defer func() {
		for _, c := range codecs {
			c.Release()
		}
	}()
	for _, c := range codecs {
		list = append(list, candidate{c.Name, c.Hardware, func(config mf.Config) (videoEncoder, error) {
			e, err := c.Open(p.device.Object, config)
			if err != nil {
				return nil, err
			}
			return foundation{e}, nil
		}})
	}
	usable := 0
	for _, c := range list {
		if r.avoid[c.Name] < 2 {
			usable++
		}
	}
	if usable == 0 {
		clear(r.avoid) // every encoder has let us down: give them all another go
	}
	screenWidth, screenHeight := p.screen.Size()
	var problems []string
	for _, c := range list {
		if r.avoid[c.Name] >= 2 {
			continue
		}
		width, height := o.Size(screenWidth, screenHeight)
		rate := o.Rate
		if !c.Hardware {
			// Compressing on the processor takes real work away from
			// whatever else is running, so it is kept light.
			width, height = within(width, height, 1280, 720)
			rate = 30
		}
		sizes := [][2]int{{width, height}}
		if width > 1920 || height > 1080 {
			w, h := within(width, height, 1920, 1080)
			sizes = append(sizes, [2]int{w, h}) // for an encoder that cannot go as large
		}
		for _, size := range sizes {
			bitrate := o.Bitrate(size[0], size[1], rate)
			config := mf.Config{
				Width: size[0], Height: size[1], Rate: rate,
				Bitrate: bitrate, Peak: bitrate * 3 / 2, KeyInterval: rate * int(keyGap/clock.Second),
			}
			encoder, err := c.Open(config)
			if err == nil {
				p.encoder, p.width, p.height, p.rate = encoder, size[0], size[1], rate
				// Never hold more than the encoder's peak rate could fill.
				r.ring.SetLimit(int(float64(config.Peak+soundBitrate) / 8 * o.Window.Seconds()))
				r.ring.SetFrame(clock.Second / int64(rate))
				return nil
			}
			problems = append(problems, err.Error())
		}
	}
	return fmt.Errorf("No video encoder could be started (%s).", strings.Join(problems, "; "))
}

// session records until stopped or until something breaks.
func (r *Recorder) session() error {
	o := r.options
	adapters, err := dx.Adapters()
	if err != nil {
		return err
	}
	defer func() {
		for _, a := range adapters {
			a.Close()
		}
	}()
	adapter, output := pick(adapters, o.Monitor)
	if output == nil {
		return errors.New("No screen was found.")
	}
	p := &pipeline{output: output}
	defer p.close()
	if p.device, err = dx.NewDevice(adapter); err != nil {
		return err
	}
	if err := p.attach(o, r.epoch); err != nil {
		return err
	}
	if err := r.choose(p, adapter); err != nil {
		return err
	}
	if err := p.convert(); err != nil {
		return err
	}
	if header := p.encoder.Header(); header != nil {
		_, picture := h264.Convert(nil, header)
		r.parameters(picture)
	}
	broken := func(err error) error { return &fault{encoder: p.encoder.Label(), err: err} }
	r.set(func(s *Status) {
		s.State, s.Problem = Recording, ""
		s.Screen, s.Width, s.Height, s.Rate = output.Name, p.width, p.height, p.rate
		s.Encoder, s.Hardware = p.encoder.Label(), p.encoder.OnChip()
		s.Pointer, s.Rotated = p.painter != nil, p.rotated() && o.Source == nil
	})

	interval := clock.Second / int64(p.rate)
	// At each tick the thread asks for the newest picture and is willing to
	// wait most of a tick for one. A still screen costs nothing: the thread
	// sleeps inside that wait.
	wait := uint32(max(interval/10000-2, 1))
	ticker := clock.NewTicker(interval)
	defer ticker.Close()
	const never = -1 << 62
	lastSubmit, lastKey := int64(never), int64(never)
	var retry int64 // when to try for the screen again
	pending := false
	refused, failures := 0, 0
	for !r.stopped() {
		tick, _ := ticker.Wait()
		at := tick - r.epoch
		mark := clock.Now()
		if err := p.encoder.Collect(r.take); err != nil {
			return broken(err)
		}
		lap(0, &mark)

		if p.screen == nil && tick >= retry {
			// The screen went away: a mode change, the lock screen, a
			// security prompt. Try to get it back twice a second.
			err := p.attach(o, r.epoch)
			if err == nil {
				err = p.convert()
			}
			if err != nil {
				p.dropScreen()
				retry = tick + clock.Second/2
				if failures++; failures > 20 && com.Code(err) != dx.ErrAccessDenied {
					return err
				}
				r.set(func(s *Status) { s.State, s.Problem = Waiting, explain(err) })
			} else {
				failures = 0
				r.set(func(s *Status) { s.State, s.Problem, s.Pointer = Recording, "", p.painter != nil })
			}
		}
		if p.screen != nil && !pending {
			changed, err := p.screen.Acquire(wait)
			lap(1, &mark)
			switch com.Code(err) {
			case 0:
				if err != nil {
					return err
				}
				pending = changed
			case dx.ErrAccessLost, dx.ErrAccessDenied, 0x887A0001: // also an invalid call after a mode change
				// Until the screen is back the last picture is repeated.
				p.dropScreen()
				retry = 0
				continue
			default:
				return err
			}
		}

		// A picture goes to the encoder when the screen changed, and once a
		// second when it did not, so that any moment of the loop is near a
		// keyframe and a clip can start close to where it should.
		heartbeat := !pending && p.last != nil && at-lastSubmit >= heartbeatGap
		if !pending && !heartbeat {
			continue
		}
		ready, err := p.encoder.Accepting()
		if err != nil {
			return broken(err)
		}
		target := p.last
		if pending {
			target = p.free()
		}
		if !ready || target == nil || !p.encoder.Free(target) {
			// The encoder is still busy. The picture stays held and is
			// offered again on the next tick; nothing is lost, it is late.
			if pending {
				r.late.Add(1)
			}
			if refused++; refused > 3*p.rate {
				return broken(errors.New("The video encoder stopped responding."))
			}
			continue
		}
		refused = 0
		mark = clock.Now()
		if pending {
			picture := p.painter.Paint(p.screen.Texture(), p.screen.Pointer())
			lap(2, &mark)
			if err := p.converter.Convert(picture, target); err != nil {
				return err
			}
			lap(3, &mark)
			p.last, pending = target, false
		}
		key := at-lastKey >= keyGap
		if err := p.encoder.Submit(target.Texture, at, interval, key); err != nil {
			return broken(err)
		}
		lap(4, &mark)
		if key {
			lastKey = at
		}
		lastSubmit = at
		p.device.Flush()
		lap(5, &mark)
		if profiling && Profile[6].Add(1)%30 == 0 {
			var created, exited, inKernel, inUser int64
			thread, _, _ := getCurrentThread.Call()
			kernel32.NewProc("GetThreadTimes").Call(thread, uintptr(unsafe.Pointer(&created)), uintptr(unsafe.Pointer(&exited)),
				uintptr(unsafe.Pointer(&inKernel)), uintptr(unsafe.Pointer(&inUser)))
			Profile[7].Store(inKernel + inUser)
		}
		r.frames.Add(1)
	}
	return nil
}

func (r *Recorder) parameters(picture h264.Picture) {
	changed := false
	if picture.SPS != nil && !bytes.Equal(picture.SPS, r.sps) {
		r.sps, changed = bytes.Clone(picture.SPS), true
	}
	if picture.PPS != nil && !bytes.Equal(picture.PPS, r.pps) {
		r.pps, changed = bytes.Clone(picture.PPS), true
	}
	if changed && r.sps != nil && r.pps != nil {
		r.ring.SetParams(r.sps, r.pps)
	}
}

// take receives one compressed picture from the encoder and puts it in the
// loop, in the form an MP4 file holds.
func (r *Recorder) take(data []byte, at int64) {
	var picture h264.Picture
	r.scratch, picture = h264.Convert(r.scratch[:0], data)
	r.parameters(picture)
	if len(r.scratch) == 0 {
		return
	}
	if at <= r.lastOut {
		at = r.lastOut + 1
	}
	r.lastOut = at
	r.ring.AddVideo(r.scratch, at, picture.Key)
}
