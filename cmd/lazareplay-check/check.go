//go:build windows

package main

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"lazareplay"
	"lazareplay/internal/audio"
	"lazareplay/internal/dx"
	"lazareplay/internal/mf"
	"lazareplay/internal/nvenc"
	"lazareplay/internal/recorder"
	"lazareplay/internal/testcard"
)

// failed counts the checks that did not pass.
var failed int

// line prints one result. A check that does not hold is marked and counted.
func line(label, text string, ok bool) {
	mark := "ok"
	if !ok {
		mark = "FAILED"
		failed++
	}
	fmt.Printf("  %-10s %-62s %s\n", label, text, mark)
}

// note prints a line that is information, not a check.
func note(label, text string) { fmt.Printf("  %-10s %s\n", label, text) }

// selfCheck runs the recorder on this computer and checks what comes out.
// It records a generated picture in which every pixel is known, plays the
// saved clip back through the decoders of Windows and compares, then does
// a short recording of each real screen to see that capture works. Every
// clip it makes is deleted again.
func selfCheck() {
	fmt.Printf("LazaReplay %s self-check\n", lazareplay.Version())
	system()
	monitors := recorder.Monitors()
	if len(monitors) == 0 {
		fmt.Println("\nNo screen was found, so nothing can be recorded.")
		failed++
	}
	for i, m := range monitors {
		name := "Main screen"
		if !m.Main {
			name = fmt.Sprintf("Screen %d", i+1)
		}
		fmt.Printf("\n%s, generated test picture\n", name)
		generated(recorder.Options{Monitor: m.Name, Rate: 60, Quality: 1, Height: 1080}, true)
		fmt.Printf("\n%s, live\n", name)
		live(m)
	}
	if len(monitors) > 0 {
		fmt.Println("\nSoftware encoder, the fallback on a computer with no usable hardware encoder")
		generated(recorder.Options{Monitor: monitors[0].Name, Rate: 60, Quality: 1, Height: 1080, Software: true}, false)
	}
	fmt.Println()
	if failed == 0 {
		fmt.Println("Result: every check passed.")
	} else {
		fmt.Printf("Result: %d checks failed. Please include this whole report if you ask for help.\n", failed)
	}
	if ownConsole() {
		fmt.Print("\nPress Enter to close.")
		bufio.NewReader(os.Stdin).ReadString('\n')
	}
	if failed > 0 {
		os.Exit(1)
	}
}

// system describes the computer: Windows, the graphics chips, the screens
// on each and the video encoders each offers.
func system() {
	var major, minor, build uint32
	syscall.NewLazyDLL("ntdll.dll").NewProc("RtlGetNtVersionNumbers").Call(
		uintptr(unsafe.Pointer(&major)), uintptr(unsafe.Pointer(&minor)), uintptr(unsafe.Pointer(&build)))
	fmt.Printf("Windows %d.%d build %d, %d processor threads\n\nGraphics\n", major, minor, build&0xFFFF, runtime.NumCPU())
	adapters, err := dx.Adapters()
	if err != nil {
		fmt.Println("  could not list graphics chips:", err)
		return
	}
	for _, a := range adapters {
		if a.Software {
			a.Close()
			continue
		}
		fmt.Printf("  %s\n", a.Name)
		for _, o := range a.Outputs {
			turned := ""
			if o.Rotation > 1 {
				turned = ", turned"
			}
			fmt.Printf("      screen %s, %d by %d%s\n", strings.TrimPrefix(o.Name, `\\.\`), o.Desktop.Width(), o.Desktop.Height(), turned)
		}
		if a.VendorID == 0x10DE && nvenc.Available() {
			fmt.Println("      encoder: NVIDIA NVENC, driven directly")
		}
		codecs, _ := mf.VideoEncoders(a.ID(), a.VendorID)
		for _, c := range codecs {
			fmt.Printf("      encoder: %s\n", c.Name)
			c.Release()
		}
		a.Close()
	}
	software, _ := mf.VideoEncoders(nil, 0)
	for _, c := range software {
		fmt.Printf("  software encoder: %s\n", c.Name)
		c.Release()
	}
}

// run records for a few seconds and saves the result to a temporary file,
// which the caller deletes. It returns the state of the recorder just
// before the save.
func run(options recorder.Options, seconds int) (recorder.Status, string, error) {
	options.Window = time.Minute
	r := recorder.Start(options)
	defer r.Stop()
	var s recorder.Status
	for waited := 0; waited < 150; waited++ { // up to fifteen seconds to get going
		if s = r.Status(); s.State == recorder.Recording {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if s.State != recorder.Recording {
		problem := s.Problem
		if problem == "" {
			problem = "The recorder did not start within fifteen seconds."
		}
		return s, "", fmt.Errorf("%s", problem)
	}
	time.Sleep(time.Duration(seconds) * time.Second)
	s = r.Status()
	file, err := os.CreateTemp("", "lazareplay-check-*.mp4")
	if err != nil {
		return s, "", err
	}
	path := file.Name()
	file.Close()
	if _, err := r.Save(path); err != nil {
		os.Remove(path)
		return s, "", err
	}
	return s, path, nil
}

// describe says which encoder a recording used and how.
func describe(s recorder.Status) {
	where := "on the processor"
	if s.Hardware {
		where = "on the graphics chip"
	}
	note("encoder", strings.ReplaceAll(s.Encoder, "®", "")+", "+where)
	how := "by the chip's video processor"
	if s.Drawn {
		how = "by drawing"
	}
	note("recorded", fmt.Sprintf("%d by %d, %d a second, converted %s", s.Width, s.Height, s.Rate, how))
}

// generated records the test card through the chip of one screen and
// checks the clip against it. With sound set it also records clicks made
// on the second and checks them against a lamp on the card, and adds a
// tone in the place of a microphone and checks that it is there too.
func generated(options recorder.Options, sound bool) {
	const width, height = 1920, 1080
	options.Source = func(device *dx.Device, epoch int64) (recorder.Screen, error) {
		source, err := testcard.NewSource(device, width, height)
		if err != nil {
			return nil, err
		}
		source.Epoch = epoch
		return pointed{source}, nil
	}
	if sound {
		options.Sound = true
		options.SoundSource = func(epoch int64) (audio.Input, error) { return &clicker{epoch: epoch}, nil }
		options.Mic = true
		options.MicSource = func(epoch int64) (audio.Input, error) { return &clicker{epoch: epoch, tone: true}, nil }
	}
	s, path, err := run(options, 5)
	if err != nil {
		line("recording", err.Error(), false)
		return
	}
	defer os.Remove(path)
	describe(s)
	scale := float64(s.Width) / width
	pointers := s.Pointer && scale == 1
	f, err := examine(path, scale, pointers, false)
	if err != nil {
		line("playback", "the saved clip could not be played: "+err.Error(), false)
		return
	}
	whole := f.Card && f.Missing == 0 && f.Repeated == 0 && f.Reverse == 0 && f.Unread == 0 && f.Pictures >= 3*s.Rate
	line("pictures", fmt.Sprintf("%d: %d missing, %d repeated, %d out of order, %d unreadable", f.Pictures, f.Missing, f.Repeated, f.Reverse, f.Unread), whole)
	line("colours", fmt.Sprintf("within %.1f of 255", math.Max(f.Bars, f.Square)), f.Card && f.Bars <= 4 && f.Square <= 6)
	switch {
	case !s.Pointer:
		line("pointer", "the mouse pointer cannot be drawn on this chip", false)
	case pointers:
		line("pointer", fmt.Sprintf("three kinds drawn, within %.1f of 255", f.Spots), f.Spots <= 6)
	}
	step := 1000 / float64(s.Rate)
	line("timing", fmt.Sprintf("%.2f ms between pictures, %d waited for the encoder", f.Median, s.Late), math.Abs(f.Median-step) <= 1)
	if sound {
		together := f.Sound && math.Abs(f.SoundLength-f.Stated) <= 0.25 && len(f.Gaps) > 0 && math.Abs(middle(f.Gaps)) <= 45
		text := "no sound track in the clip"
		if f.Sound {
			text = fmt.Sprintf("%.1f s of %d Hz sound, %.0f ms from the picture", f.SoundLength, f.Rate, math.Abs(middle(f.Gaps)))
		}
		line("sound", text, together)
		// The stand-in for the microphone hums at 1000 Hz, quieter than the
		// clicks but all the time, so most of the sound should be its hum.
		line("microphone", fmt.Sprintf("added to the sound: its tone is %.0f%% of what is heard", 100*f.Tone), s.Mic && f.Tone >= 0.5)
	}
}

// live records the real screen and what the computer plays for three
// seconds. It can only check that pictures arrive and that the clip plays;
// what is on the screen is not looked at.
func live(m recorder.Monitor) {
	s, path, err := run(recorder.Options{Monitor: m.Name, Rate: 60, Quality: 1, Height: 1080, Sound: true}, 3)
	if err != nil {
		line("recording", err.Error(), false)
		return
	}
	defer os.Remove(path)
	describe(s)
	line("capture", fmt.Sprintf("%d pictures in three seconds (a still screen gives few)", s.Frames), s.Frames >= 1)
	if s.Rotated {
		note("screen", "this screen is turned: it is recorded on its side and without the pointer")
	}
	if s.Sound {
		line("sound", "recording what the computer plays", true)
	} else {
		note("sound", "not recording: "+s.Silent)
	}
	note("microphone", "not used here: the self-check never listens to a real microphone")
	f, err := examine(path, 1, false, false)
	if err != nil {
		line("playback", "the saved clip could not be played: "+err.Error(), false)
		return
	}
	line("playback", fmt.Sprintf("the clip plays: %d pictures over %.1f s, then deleted", f.Pictures, f.Stated), f.Pictures >= 1 && f.Stated > 1)
}

// ownConsole reports whether this program has a console window to itself,
// which is the case when it was started by a double click. The window
// would close the moment the program ends, taking the report with it.
func ownConsole() bool {
	var list [2]uint32
	n, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleProcessList").Call(uintptr(unsafe.Pointer(&list[0])), 2)
	return n == 1
}
