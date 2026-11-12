//go:build windows

// Package recorder runs the whole pipeline: it captures the screen,
// compresses it on the graphics chip, keeps the last few minutes in memory
// and writes a clip to disk when asked.
package recorder

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
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
var qualityRates = [3]float64{6, 12, 20}

// Bitrate returns the average bits per second the options give for a
// picture of the given size.
func (o Options) Bitrate(width, height int) int {
	q := min(max(o.Quality, 0), len(qualityRates)-1)
	rate := qualityRates[q] * 1e6 * float64(width*height) / (1920 * 1080)
	if o.Rate <= 30 {
		rate *= 0.6
	}
	return max(int(rate), 500_000)
}

// Memory estimates the most memory the loop will use, in bytes, for a
// picture of the given size.
func (o Options) Memory(width, height int) int {
	rate := o.Bitrate(width, height)
	if o.Sound {
		rate += soundBitrate
	}
	return int(float64(rate) / 8 * o.Window.Seconds())
}

// Size returns the picture size the options give for a screen.
func (o Options) Size(screenWidth, screenHeight int) (width, height int) {
	width, height = screenWidth, screenHeight
	if o.Height > 0 && height > o.Height {
		width = width * o.Height / height
		height = o.Height
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
	Encoder  string        // name of the video encoder in use
	Hardware bool          // whether it runs on the graphics chip
	Pointer  bool          // whether the mouse pointer can be drawn
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
	sound        atomic.Pointer[soundFormat] // set while sound is being recorded

	// Used only by the capture thread.
	scratch  []byte
	sps, pps []byte
	lastOut  int64
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
		err := r.session()
		if r.stopped() {
			return
		}
		if err != nil {
			r.set(func(s *Status) { s.State, s.Problem = Failed, explain(err) })
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
	encoder   *mf.Encoder
	width     int // recorded picture size
	height    int
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

// open starts capturing the screen and prepares conversion from its size.
func (p *pipeline) open(o Options, epoch int64) error {
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
	width, height := p.screen.Size()
	if p.width == 0 {
		p.width, p.height = o.Size(width, height)
	}
	if p.converter == nil || p.converter.InWidth != width || p.converter.InHeight != height {
		// First use, or the screen came back at another size. The recorded
		// size stays what it was; the converter scales to it.
		p.dropConverter()
		if p.converter, err = p.device.NewConverter(width, height, p.width, p.height, o.Rate); err != nil {
			return err
		}
		for i := 0; i < 6; i++ {
			t, err := p.converter.NewTarget()
			if err != nil {
				return err
			}
			p.targets = append(p.targets, t)
		}
	}
	// Without the painter the recording still works; it just has no pointer.
	p.painter, _ = p.device.NewPainter(width, height)
	return nil
}

// free returns a texture the encoder is not reading, or nil.
func (p *pipeline) free() *dx.Target {
	for range p.targets {
		p.next = (p.next + 1) % len(p.targets)
		if t := p.targets[p.next]; t.Idle() {
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
	if err := p.open(o, r.epoch); err != nil {
		return err
	}

	bitrate := o.Bitrate(p.width, p.height)
	config := mf.Config{
		Width: p.width, Height: p.height, Rate: o.Rate,
		Bitrate: bitrate, Peak: bitrate * 3 / 2, KeyInterval: o.Rate * int(keyGap/clock.Second),
	}
	var codecs []*mf.Codec
	if !o.Software {
		codecs, _ = mf.VideoEncoders(adapter.ID())
	}
	software, _ := mf.VideoEncoders(nil)
	var problems []string
	for _, codec := range append(codecs, software...) {
		if p.encoder == nil {
			if p.encoder, err = codec.Open(p.device.Object, config); err != nil {
				problems = append(problems, err.Error())
			}
		}
		codec.Release()
	}
	if p.encoder == nil {
		return fmt.Errorf("No video encoder could be started (%v).", problems)
	}
	if header := p.encoder.Header(); header != nil {
		_, picture := h264.Convert(nil, header)
		r.parameters(picture)
	}
	// Never hold more than the encoder's peak rate could fill.
	r.ring.SetLimit(int(float64(config.Peak+soundBitrate) / 8 * o.Window.Seconds()))
	r.set(func(s *Status) {
		s.State, s.Problem = Recording, ""
		s.Screen, s.Width, s.Height = output.Name, p.width, p.height
		s.Encoder, s.Hardware = p.encoder.Name, p.encoder.Hardware
		s.Pointer = p.painter != nil
	})

	interval := clock.Second / int64(o.Rate)
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
		if err := p.encoder.Collect(r.take); err != nil {
			return err
		}

		if p.screen == nil && tick >= retry {
			// The screen went away: a mode change, the lock screen, a
			// security prompt. Try to get it back twice a second.
			if err := p.open(o, r.epoch); err != nil {
				p.dropScreen()
				retry = tick + clock.Second/2
				if failures++; failures > 20 && com.Code(err) != dx.ErrAccessDenied {
					return err
				}
				r.set(func(s *Status) { s.State, s.Problem = Waiting, explain(err) })
			} else {
				failures = 0
				r.set(func(s *Status) { s.State, s.Problem = Recording, "" })
			}
		}
		if p.screen != nil && !pending {
			changed, err := p.screen.Acquire(wait)
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
			return err
		}
		target := p.last
		if pending {
			target = p.free()
		}
		if !ready || target == nil || !target.Idle() {
			// The encoder is still busy. The picture stays held and is
			// offered again on the next tick; nothing is lost, it is late.
			if pending {
				r.late.Add(1)
			}
			if refused++; refused > 3*o.Rate {
				return errors.New("The video encoder stopped responding.")
			}
			continue
		}
		refused = 0
		if pending {
			picture := p.painter.Paint(p.screen.Texture(), p.screen.Pointer())
			if err := p.converter.Convert(picture, target); err != nil {
				return err
			}
			p.last, pending = target, false
		}
		key := at-lastKey >= keyGap
		if err := p.encoder.Submit(target.Texture, at, interval, key); err != nil {
			return err
		}
		if key {
			lastKey = at
		}
		lastSubmit = at
		p.device.Flush()
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
