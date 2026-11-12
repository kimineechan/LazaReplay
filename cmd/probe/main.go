//go:build windows

// probe is a development aid. It exercises one stage of the recorder at a
// time and prints numbers, never pictures.
//
//	probe            which adapter drives each monitor, which encoders exist
//	probe colour     convert known colours on the graphics chip and check them
//	probe capture    capture the main screen for three seconds, print timing
package main

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"unsafe"

	"lazareplay/internal/clock"
	"lazareplay/internal/com"
	"lazareplay/internal/dx"
	"lazareplay/internal/mf"
)

func main() {
	runtime.LockOSThread()
	com.Init()
	if err := mf.Startup(); err != nil {
		fail(err)
	}
	defer mf.Shutdown()
	mode := ""
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "":
		adapters()
	case "colour":
		colour()
	case "capture":
		capture()
	case "pattern":
		pattern()
	case "encode":
		encode()
	case "codecs":
		codecs()
	case "record":
		record()
	case "verify":
		verify()
	default:
		fmt.Println("unknown mode", mode)
	}
}

// option reads a setting written as name=number after the mode.
func option(name string, fallback int) int {
	for _, arg := range os.Args[2:] {
		if len(arg) > len(name) && arg[:len(name)] == name && arg[len(name)] == '=' {
			var n int
			if _, err := fmt.Sscan(arg[len(name)+1:], &n); err == nil {
				return n
			}
		}
	}
	return fallback
}

func fail(err error) {
	fmt.Println("error:", err)
	os.Exit(1)
}

func adapters() {
	list, err := dx.Adapters()
	if err != nil {
		fail(err)
	}
	for _, a := range list {
		fmt.Printf("adapter: %s (%s, software=%v)\n", a.Name, a.Vendor(), a.Software)
		for _, o := range a.Outputs {
			fmt.Printf("    monitor %s: %dx%d at %d,%d rotation %d\n", o.Name, o.Desktop.Width(), o.Desktop.Height(), o.Desktop.Left, o.Desktop.Top, o.Rotation)
		}
		id := *(*[8]byte)(unsafe.Pointer(&a.LUID))
		encoders, err := mf.VideoEncoders(&id)
		if err != nil {
			fmt.Println("    encoders:", err)
			continue
		}
		for _, e := range encoders {
			fmt.Printf("    hardware encoder: %s\n", e.Name)
		}
		if len(encoders) == 0 {
			fmt.Println("    no hardware H.264 encoder")
		}
	}
	software, err := mf.VideoEncoders(nil)
	if err != nil {
		fmt.Println("software encoders:", err)
	}
	for _, e := range software {
		fmt.Printf("software encoder: %s\n", e.Name)
	}
	if a, o := dx.Primary(list); o != nil {
		fmt.Printf("main desktop: %s on %s\n", o.Name, a.Name)
	}
}

// primary opens a device on the adapter that drives the main screen.
func primary() (*dx.Device, *dx.Output) {
	list, err := dx.Adapters()
	if err != nil {
		fail(err)
	}
	if n := option("adapter", -1); n >= 0 && n < len(list) { // to test another chip
		device, err := dx.NewDevice(list[n])
		if err != nil {
			fail(err)
		}
		fmt.Println("device on", list[n].Name)
		if len(list[n].Outputs) == 0 {
			return device, nil
		}
		return device, &list[n].Outputs[0]
	}
	a, o := dx.Primary(list)
	if o == nil {
		fail(fmt.Errorf("no monitor found"))
	}
	device, err := dx.NewDevice(a)
	if err != nil {
		fail(err)
	}
	fmt.Println("device on", a.Name)
	return device, o
}

// colour converts six flat colours and compares the result with the values
// the BT.709 standard gives for studio range.
func colour() {
	device, _ := primary()
	const width, height = 1280, 720
	colours := []struct {
		name    string
		r, g, b byte
	}{
		{"white", 255, 255, 255}, {"black", 0, 0, 0}, {"red", 255, 0, 0},
		{"green", 0, 255, 0}, {"blue", 0, 0, 255}, {"grey", 128, 128, 128},
	}
	band := height / len(colours)
	pixels := make([]byte, width*height*4)
	for y := 0; y < height; y++ {
		c := colours[min(y/band, len(colours)-1)]
		for x := 0; x < width; x++ {
			p := pixels[(y*width+x)*4:]
			p[0], p[1], p[2], p[3] = c.b, c.g, c.r, 255
		}
	}
	source, err := device.NewTexture(width, height, dx.FormatBGRA, dx.UsageDefault, dx.BindRenderTarget|dx.BindShaderResource, 0, pixels, width*4)
	if err != nil {
		fail(err)
	}
	converter, err := device.NewConverter(width, height, width, height, 60)
	if err != nil {
		fail(err)
	}
	target, err := converter.NewTarget()
	if err != nil {
		fail(err)
	}
	if err := converter.Convert(source, target); err != nil {
		fail(err)
	}
	out, pitch, err := device.Read(target.Texture)
	if err != nil {
		fail(err)
	}
	fmt.Println("colour    measured Y  Cb  Cr     BT.709 Y  Cb  Cr")
	worst := 0.0
	for i, c := range colours {
		row := i*band + band/2
		y := out[row*pitch+width/2]
		chroma := out[(height+row/2)*pitch+(width/2)&^1:]
		r, g, b := float64(c.r)/255, float64(c.g)/255, float64(c.b)/255
		luma := 0.2126*r + 0.7152*g + 0.0722*b
		wantY, wantCb, wantCr := 16+219*luma, 128+224*(b-luma)/1.8556, 128+224*(r-luma)/1.5748
		fmt.Printf("%-8s  %10d %3d %3d     %8.1f %3.0f %3.0f\n", c.name, y, chroma[0], chroma[1], wantY, wantCb, wantCr)
		for _, d := range []float64{float64(y) - wantY, float64(chroma[0]) - wantCb, float64(chroma[1]) - wantCr} {
			if d < 0 {
				d = -d
			}
			worst = max(worst, d)
		}
	}
	fmt.Printf("largest difference: %.1f levels of 255\n", worst)
}

// capture runs the capture loop for three seconds at 60 frames per second.
func capture() {
	device, output := primary()
	dup, err := device.Duplicate(output)
	if err != nil {
		fail(err)
	}
	defer dup.Close()
	fmt.Printf("screen %dx%d at %.0f Hz, rotation %d\n", dup.Width, dup.Height, dup.Refresh, dup.Rotation)
	converter, err := device.NewConverter(dup.Width, dup.Height, dup.Width, dup.Height, 60)
	if err != nil {
		fail(err)
	}
	defer converter.Close()
	var targets []*dx.Target
	for i := 0; i < 4; i++ {
		t, err := converter.NewTarget()
		if err != nil {
			fail(err)
		}
		targets = append(targets, t)
	}
	wait := option("wait", 0) // milliseconds to wait for a frame at each tick
	var encoder *mf.Encoder
	if option("encoder", 0) == 1 {
		config := mf.Config{Width: dup.Width, Height: dup.Height, Rate: 60, Bitrate: 10_000_000, Peak: 15_000_000, KeyInterval: 120}
		for _, codec := range encoders(device) {
			if encoder, err = codec.Open(device.Object, config); err == nil {
				break
			}
		}
		if encoder == nil {
			fail(err)
		}
		defer encoder.Close()
		fmt.Println("encoder open:", encoder.Name)
	}
	submit := option("submit", 0) == 1
	outputs := 0
	ticker := clock.NewTicker(clock.Second / 60)
	defer ticker.Close()
	var frames, quiet, direct, pointer, skipped, accumulated int
	var late, work, waited []int64
	shown := false
	for i := 0; i < 180; i++ {
		tick, missed := ticker.Wait()
		skipped += missed
		began := clock.Now()
		late = append(late, began-tick)
		changed, err := dup.Acquire(uint32(wait))
		if err != nil {
			fail(err)
		}
		waited = append(waited, clock.Now()-began)
		if !changed {
			quiet++
			continue
		}
		began = clock.Now()
		accumulated += dup.Accumulated
		frames++
		if dup.Pointer().Visible {
			pointer++
		}
		if !shown {
			d := dx.Describe(dup.Texture())
			fmt.Printf("desktop texture: %dx%d format %d bind 0x%X misc 0x%X\n", d.Width, d.Height, d.Format, d.BindFlags, d.MiscFlags)
			shown = true
		}
		target := targets[frames%len(targets)]
		if err := converter.Convert(dup.Texture(), target); err != nil {
			fmt.Println("direct conversion:", err)
		} else {
			direct++
		}
		if encoder != nil && submit {
			encoder.Collect(func([]byte, int64) { outputs++ })
			if ok, _ := encoder.Accepting(); ok && target.Idle() {
				if err := encoder.Submit(target.Texture, tick, clock.Second/60, frames == 1); err != nil {
					fmt.Println("submit:", err)
				}
			}
		}
		device.Flush()
		work = append(work, clock.Now()-began)
	}
	if encoder != nil {
		fmt.Println("encoder outputs:", outputs)
	}
	fmt.Printf("180 ticks waiting up to %d ms: %d frames, %d ticks with nothing new, %d ticks skipped\n", wait, frames, quiet, skipped)
	fmt.Printf("screen updates folded into those frames: %d; time spent waiting: median %.2f ms, worst %.2f ms\n", accumulated, ms(median(waited)), ms(largest(waited)))
	fmt.Printf("converted straight from the desktop texture: %d of %d, pointer showing in %d\n", direct, frames, pointer)
	fmt.Printf("wake-up lateness: median %.2f ms, worst %.2f ms\n", ms(median(late)), ms(largest(late)))
	if len(work) > 0 {
		fmt.Printf("processor time per frame: median %.3f ms, worst %.3f ms\n", ms(median(work)), ms(largest(work)))
	}
	if shape := dup.Pointer().Shape; shape != nil {
		fmt.Printf("pointer shape: type %d, %dx%d\n", shape.Type, shape.Width, shape.Height)
	}
}

func ms(t int64) float64 { return float64(t) / 10000 }

func median(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

func largest(v []int64) int64 {
	var m int64
	for _, x := range v {
		m = max(m, x)
	}
	return m
}
