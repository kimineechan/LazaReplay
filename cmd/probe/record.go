//go:build windows

package main

import (
	"fmt"
	"os"
	"time"

	"lazareplay/internal/dx"
	"lazareplay/internal/recorder"
	"lazareplay/internal/testcard"
)

// text reads a setting written as name=value after the mode.
func text(name, fallback string) string {
	for _, arg := range os.Args[2:] {
		if len(arg) > len(name) && arg[:len(name)] == name && arg[len(name)] == '=' {
			return arg[len(name)+1:]
		}
	}
	return fallback
}

// The generated source reports a mouse pointer of each kind Windows has,
// one after the other, each over a different colour bar. The colours that
// should result are known, so pointer drawing can be checked by numbers.
const pointerSpan = 60 // frames each pointer is shown for

var testPointers = func() [3]dx.Pointer {
	// A colour pointer: left half solid green, right half blue at half strength.
	colour := make([]byte, 48*48*4)
	for y := 0; y < 48; y++ {
		for x := 0; x < 48; x++ {
			p := colour[(y*48+x)*4:]
			if x < 24 {
				p[0], p[1], p[2], p[3] = 0, 255, 0, 255
			} else {
				p[0], p[1], p[2], p[3] = 255, 0, 0, 128
			}
		}
	}
	// A monochrome pointer in four quarters: black, white, see-through, inverting.
	mono := make([]byte, 64*4)
	for y := 0; y < 32; y++ {
		and, xor := mono[y*4:], mono[(y+32)*4:]
		if y >= 16 {
			and[0], and[1], and[2], and[3] = 0xFF, 0xFF, 0xFF, 0xFF
		}
		xor[2], xor[3] = 0xFF, 0xFF
	}
	// A masked pointer: left half replaces with magenta, right half inverts.
	masked := make([]byte, 32*32*4)
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			p := masked[(y*32+x)*4:]
			if x < 16 {
				p[0], p[1], p[2], p[3] = 255, 0, 255, 0
			} else {
				p[0], p[1], p[2], p[3] = 255, 255, 255, 0xFF
			}
		}
	}
	top := testcard.Y + 40
	return [3]dx.Pointer{
		{Visible: true, X: testcard.X + 16, Y: top, Serial: 1, Shape: &dx.Shape{Type: dx.ShapeColour, Width: 48, Height: 48, Pitch: 192, Data: colour}},
		{Visible: true, X: testcard.X + 80 + 24, Y: top, Serial: 2, Shape: &dx.Shape{Type: dx.ShapeMonochrome, Width: 32, Height: 64, Pitch: 4, Data: mono}},
		{Visible: true, X: testcard.X + 160 + 24, Y: top, Serial: 3, Shape: &dx.Shape{Type: dx.ShapeMaskedColor, Width: 32, Height: 32, Pitch: 128, Data: masked}},
	}
}()

// pointerAt returns the pointer shown in a frame, and which one it is
// (0 for none).
func pointerAt(frame int) (dx.Pointer, int) {
	if which := frame / pointerSpan; frame >= 0 && which >= 1 && which <= 3 {
		return testPointers[which-1], which
	}
	return dx.Pointer{}, 0
}

// spot is a point of the picture and the colour it should have.
type spot struct {
	name string
	x, y int
	want testcard.Colour
}

// spots lists the points under the three pointers and what each should
// measure in a frame.
func spots(frame int) []spot {
	_, which := pointerAt(frame)
	bar := func(i int) testcard.Colour { return testcard.Expected(i) }
	rgb := testcard.FromRGB
	choose := func(shown bool, with, without testcard.Colour) testcard.Colour {
		if shown {
			return with
		}
		return without
	}
	top := testcard.Y + 40
	c, m, k := testcard.X+16, testcard.X+80+24, testcard.X+160+24
	half := 255 * (1 - 128.0/255) // white seen through blue at alpha 128
	return []spot{
		{"colour pointer, solid green", c + 12, top + 24, choose(which == 1, rgb(0, 255, 0), bar(0))},
		{"colour pointer, half blue on white", c + 36, top + 24, choose(which == 1, rgb(half, half, 255), bar(0))},
		{"monochrome pointer, black", m + 8, top + 8, choose(which == 2, rgb(0, 0, 0), bar(1))},
		{"monochrome pointer, white", m + 24, top + 8, choose(which == 2, rgb(255, 255, 255), bar(1))},
		{"monochrome pointer, see-through", m + 8, top + 24, bar(1)},
		{"monochrome pointer, inverted yellow", m + 24, top + 24, choose(which == 2, rgb(0, 0, 255), bar(1))},
		{"masked pointer, magenta", k + 8, top + 16, choose(which == 3, rgb(255, 0, 255), bar(2))},
		{"masked pointer, inverted cyan", k + 24, top + 16, choose(which == 3, rgb(255, 0, 0), bar(2))},
	}
}

// pointed is the generated source with the test pointers added.
type pointed struct{ *testcard.Source }

func (p pointed) Pointer() dx.Pointer {
	pointer, _ := pointerAt(p.Frame())
	return pointer
}

// record runs the real recorder for a few seconds and saves a clip. With
// card=1 it records the generated test card instead of the screen.
func record() {
	seconds := option("seconds", 6)
	options := recorder.Options{
		Window:   time.Duration(option("window", 300)) * time.Second,
		Rate:     option("rate", 60),
		Quality:  option("quality", 1),
		Height:   option("height", 0),
		Monitor:  text("monitor", ""),
		Sound:    option("sound", 0) == 1,
		Software: option("software", 0) == 1,
	}
	if option("card", 0) == 1 {
		width, height, still := option("width", 1920), option("screen", 1080), option("still", 0)
		options.Source = func(device *dx.Device) (recorder.Screen, error) {
			source, err := testcard.NewSource(device, width, height)
			if err != nil {
				return nil, err
			}
			source.Still = still
			return pointed{source}, nil
		}
	}
	r := recorder.Start(options)
	for i := 0; i < seconds; i++ {
		time.Sleep(time.Second)
		s := r.Status()
		fmt.Printf("%2ds state=%d %dx%d %q hardware=%v pointer=%v sound=%v held=%.1fs %d KB frames=%d late=%d %s\n",
			i+1, s.State, s.Width, s.Height, s.Encoder, s.Hardware, s.Pointer, s.Sound, s.Length.Seconds(), s.Bytes/1024, s.Frames, s.Late, s.Problem)
	}
	began := time.Now()
	clip, err := r.Save(text("out", "probe.mp4"))
	if err != nil {
		fmt.Println("save:", err)
	} else {
		fmt.Printf("saved %.2f s, %d bytes, written in %.0f ms\n", clip.Length.Seconds(), clip.Bytes, float64(time.Since(began).Microseconds())/1000)
	}
	r.Stop()
	if s := r.Status(); s.Bytes != 0 || s.Length != 0 {
		fmt.Println("the loop was not wiped on stop")
	}
}
