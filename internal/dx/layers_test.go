package dx

import (
	"bytes"
	"testing"
)

func TestFitKeepsTheShape(t *testing.T) {
	cases := []struct {
		inW, inH, outW, outH int
		want                 Rect
	}{
		{1920, 1080, 1920, 1080, Rect{0, 0, 1920, 1080}},   // same size: the whole output
		{3840, 2160, 1920, 1080, Rect{0, 0, 1920, 1080}},   // same shape: the whole output
		{1920, 1200, 1920, 1080, Rect{96, 0, 1824, 1080}},  // taller screen: bars left and right
		{2560, 1080, 1920, 1080, Rect{0, 134, 1920, 944}},  // wider screen: bars above and below
		{1080, 1920, 1920, 1080, Rect{656, 0, 1264, 1080}}, // a screen on its side
	}
	for _, c := range cases {
		got := fit(c.inW, c.inH, c.outW, c.outH)
		if got != c.want {
			t.Errorf("%dx%d into %dx%d: got %+v, want %+v", c.inW, c.inH, c.outW, c.outH, got, c.want)
		}
		if got.Left&1 != 0 || got.Top&1 != 0 || got.Width()&1 != 0 || got.Height()&1 != 0 {
			t.Errorf("%dx%d into %dx%d: %+v is not on even coordinates", c.inW, c.inH, c.outW, c.outH, got)
		}
		if got.Left < 0 || got.Top < 0 || int(got.Right) > c.outW || int(got.Bottom) > c.outH {
			t.Errorf("%dx%d into %dx%d: %+v lies outside the output", c.inW, c.inH, c.outW, c.outH, got)
		}
	}
}

// pixel returns one pixel of a layer as blue, green, red, alpha.
func pixel(layer []byte, width, x, y int) [4]byte {
	p := layer[(y*width+x)*4:]
	return [4]byte{p[0], p[1], p[2], p[3]}
}

func TestMonochromeLayers(t *testing.T) {
	// 16 by 2, in four blocks of four columns: black, white, see-through,
	// inverting. The AND mask comes first, then the XOR mask.
	shape := &Shape{Type: ShapeMonochrome, Width: 16, Height: 4, Pitch: 2, Data: []byte{
		0x00, 0xFF, 0x00, 0xFF, // AND: 0 0 1 1
		0x0F, 0x0F, 0x0F, 0x0F, // XOR: 0 1 0 1
	}}
	colour, invert, width, height := Layers(shape)
	if width != 16 || height != 2 {
		t.Fatalf("size %dx%d", width, height)
	}
	want := []struct {
		x              int
		colour, invert [4]byte
	}{
		{1, [4]byte{0, 0, 0, 255}, [4]byte{}},        // black
		{5, [4]byte{255, 255, 255, 255}, [4]byte{}},  // white
		{9, [4]byte{}, [4]byte{}},                    // see-through
		{13, [4]byte{}, [4]byte{255, 255, 255, 255}}, // inverting
	}
	for _, w := range want {
		for y := 0; y < 2; y++ {
			if got := pixel(colour, width, w.x, y); got != w.colour {
				t.Errorf("colour layer at %d,%d is %v, want %v", w.x, y, got, w.colour)
			}
			if got := pixel(invert, width, w.x, y); got != w.invert {
				t.Errorf("invert layer at %d,%d is %v, want %v", w.x, y, got, w.invert)
			}
		}
	}
}

func TestColourLayer(t *testing.T) {
	// Two pixels: one half see-through, one fully see-through. The row is
	// padded, as Windows pads it.
	shape := &Shape{Type: ShapeColour, Width: 2, Height: 1, Pitch: 12, Data: []byte{10, 20, 30, 128, 1, 2, 3, 0, 9, 9, 9, 9}}
	colour, invert, width, height := Layers(shape)
	if width != 2 || height != 1 || invert != nil {
		t.Fatalf("size %dx%d, invert layer %v", width, height, invert)
	}
	if !bytes.Equal(colour, []byte{10, 20, 30, 128, 0, 0, 0, 0}) {
		t.Fatalf("colour layer %v", colour)
	}
}

func TestMaskedColourLayers(t *testing.T) {
	// A fourth byte of 0 replaces the screen; 255 XORs with it, which for
	// black leaves the screen alone.
	shape := &Shape{Type: ShapeMaskedColor, Width: 3, Height: 1, Pitch: 12, Data: []byte{1, 2, 3, 0, 255, 255, 255, 255, 0, 0, 0, 255}}
	colour, invert, _, _ := Layers(shape)
	if !bytes.Equal(colour, []byte{1, 2, 3, 255, 0, 0, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf("colour layer %v", colour)
	}
	if !bytes.Equal(invert, []byte{0, 0, 0, 0, 255, 255, 255, 255, 0, 0, 0, 0}) {
		t.Fatalf("invert layer %v", invert)
	}
}

func TestEmptyLayersAreNil(t *testing.T) {
	// A fully see-through colour pointer has nothing to draw.
	colour, invert, _, _ := Layers(&Shape{Type: ShapeColour, Width: 2, Height: 2, Pitch: 8, Data: make([]byte, 16)})
	if colour != nil || invert != nil {
		t.Fatal("an invisible pointer produced layers")
	}
}

func TestDamagedShapesAreRefused(t *testing.T) {
	bad := []*Shape{
		nil,
		{Type: ShapeColour, Width: 0, Height: 4, Pitch: 16, Data: make([]byte, 64)},
		{Type: ShapeColour, Width: 4, Height: 4, Pitch: 16, Data: make([]byte, 10)},    // too little data
		{Type: ShapeColour, Width: 4, Height: 4, Pitch: 8, Data: make([]byte, 32)},     // rows too short for the width
		{Type: ShapeMonochrome, Width: 32, Height: 2, Pitch: 2, Data: make([]byte, 4)}, // rows too short for the width
		{Type: ShapeMonochrome, Width: 8, Height: 1, Pitch: 1, Data: make([]byte, 1)},  // no room for two masks
	}
	for i, shape := range bad {
		if colour, invert, _, _ := Layers(shape); colour != nil || invert != nil {
			t.Errorf("shape %d was accepted", i)
		}
	}
}
