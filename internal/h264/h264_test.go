package h264

import (
	"bytes"
	"testing"
)

// writer builds a parameter set bit by bit, the mirror image of the reader.
type writer struct {
	data []byte
	used int // bits used in the last byte
}

func (w *writer) bit(v int) {
	if w.used == 0 {
		w.data = append(w.data, 0)
	}
	w.data[len(w.data)-1] |= byte(v&1) << (7 - uint(w.used))
	w.used = (w.used + 1) % 8
}

func (w *writer) put(v, n int) {
	for i := n - 1; i >= 0; i-- {
		w.bit(v >> uint(i))
	}
}

func (w *writer) ue(v int) {
	v++
	n := 0
	for v>>uint(n+1) != 0 {
		n++
	}
	w.put(0, n)
	w.put(v, n+1)
}

// unit returns the bits as a unit: header bytes first, a stop bit after, and
// 03 inserted wherever two zero bytes would be followed by a byte below 4.
func (w *writer) unit(head ...byte) []byte {
	w.bit(1)
	out := append([]byte(nil), head...)
	zeros := 0
	for _, b := range w.data {
		if zeros >= 2 && b <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// highSPS builds a High-profile set for a 1920 by 1080 picture: 1088 coded
// rows with eight cropped, and colour described as BT.709 studio range.
func highSPS() []byte {
	w := &writer{}
	w.ue(0)     // id
	w.ue(1)     // 4:2:0
	w.ue(0)     // 8-bit luma
	w.ue(0)     // 8-bit chroma
	w.bit(0)    // no transform bypass
	w.bit(0)    // no scaling matrices
	w.ue(0)     // frame number bits
	w.ue(2)     // picture order type 2
	w.ue(1)     // one reference frame
	w.bit(0)    // no gaps
	w.ue(119)   // 120 macroblocks wide
	w.ue(67)    // 68 macroblocks high
	w.bit(1)    // frames only
	w.bit(1)    // direct 8x8
	w.bit(1)    // cropping
	w.ue(0)     // left
	w.ue(0)     // right
	w.ue(0)     // top
	w.ue(4)     // bottom: four units of two rows
	w.bit(1)    // usability information present
	w.bit(1)    // aspect ratio present
	w.put(1, 8) // square pixels
	w.bit(0)    // no overscan information
	w.bit(1)    // video signal type present
	w.put(5, 3) // unspecified format
	w.bit(0)    // studio range
	w.bit(1)    // colour described
	w.put(1, 8) // BT.709 primaries
	w.put(1, 8) // BT.709 transfer
	w.put(1, 8) // BT.709 matrix
	w.bit(0)    // no chroma location
	w.bit(0)    // no timing
	return w.unit(0x67, 100, 0, 42)
}

func TestParseHighProfile(t *testing.T) {
	unit := highSPS()
	if !bytes.Contains(unit, []byte{0, 0, 3}) {
		t.Log("note: this parameter set needed no escape bytes")
	}
	s, err := ParseSPS(unit)
	if err != nil {
		t.Fatal(err)
	}
	want := SPS{
		Profile: 100, Level: 42, ChromaFormat: 1, BitDepthLuma: 8, BitDepthChroma: 8,
		Width: 1920, Height: 1080, ReferenceFrames: 1,
		ColourDescribed: true, Primaries: 1, Transfer: 1, Matrix: 1,
	}
	if s != want {
		t.Fatalf("got  %+v\nwant %+v", s, want)
	}
}

func TestParseBaseline(t *testing.T) {
	s, err := ParseSPS([]byte{0x67, 0x42, 0x00, 0x1E, 0xF4, 0x0A, 0x0F, 0xC8})
	if err != nil {
		t.Fatal(err)
	}
	if s.Profile != 66 || s.Level != 30 || s.Width != 320 || s.Height != 240 || s.ChromaFormat != 1 || s.ColourDescribed {
		t.Fatalf("got %+v", s)
	}
}

func TestParseSkipsEscapeBytes(t *testing.T) {
	// A set whose bits hold two zero bytes followed by a small one, so the
	// writer has to insert an escape byte and the reader has to skip it.
	w := &writer{}
	w.ue(0)
	w.ue(0)
	w.ue(0)
	w.ue(0)
	w.ue(1)
	w.bit(0)
	w.ue(1<<22 - 1) // absurdly wide: its code starts with 22 zero bits
	w.ue(14)
	w.bit(1)
	w.bit(1)
	w.bit(0)
	w.bit(0)
	unit := w.unit(0x67, 66, 0, 30)
	if !bytes.Contains(unit, []byte{0, 0, 3}) {
		t.Fatal("test set has no escape byte; adjust it")
	}
	s, err := ParseSPS(unit)
	if err != nil {
		t.Fatal(err)
	}
	if s.Width != (1<<22)*16 || s.Height != 240 {
		t.Fatalf("got %dx%d", s.Width, s.Height)
	}
}

func TestParseRejectsDamage(t *testing.T) {
	good := highSPS()
	for _, bad := range [][]byte{nil, {0x67}, {0x68, 1, 2, 3, 4}, good[:6], good[:len(good)-6]} {
		if _, err := ParseSPS(bad); err == nil {
			t.Errorf("accepted %X", bad)
		}
	}
}

func TestSplit(t *testing.T) {
	stream := []byte{
		0, 0, 0, 1, 0x09, 0xF0, // marker, four-byte start code
		0, 0, 1, 0x67, 1, 2, 3, // three-byte start code
		0, 0, 0, 1, 0x68, 9, 0, // trailing zero belongs to the next start code
		0, 0, 1, 0x65, 0xAA, 0, 0, 3, 1, 0xBB, // escape bytes inside a unit stay
	}
	var units [][]byte
	Split(stream, func(u []byte) { units = append(units, u) })
	want := [][]byte{{0x09, 0xF0}, {0x67, 1, 2, 3}, {0x68, 9}, {0x65, 0xAA, 0, 0, 3, 1, 0xBB}}
	if len(units) != len(want) {
		t.Fatalf("%d units, want %d: %X", len(units), len(want), units)
	}
	for i := range want {
		if !bytes.Equal(units[i], want[i]) {
			t.Errorf("unit %d is %X, want %X", i, units[i], want[i])
		}
	}
	Split(nil, func([]byte) { t.Error("unit found in nothing") })
	Split([]byte{1, 2, 3}, func([]byte) { t.Error("unit found without a start code") })
}

func TestConvert(t *testing.T) {
	key := []byte{
		0, 0, 0, 1, 0x09, 0x10,
		0, 0, 0, 1, 0x67, 0x42, 0, 0x1E,
		0, 0, 0, 1, 0x68, 0xCE,
		0, 0, 1, 0x06, 5, 5,
		0, 0, 1, 0x65, 0x88, 0x80,
		0, 0, 1, 0x65, 0x77,
	}
	out, picture := Convert([]byte{0xEE}, key)
	want := []byte{0xEE, 0, 0, 0, 3, 0x06, 5, 5, 0, 0, 0, 3, 0x65, 0x88, 0x80, 0, 0, 0, 2, 0x65, 0x77}
	if !bytes.Equal(out, want) {
		t.Fatalf("got  %X\nwant %X", out, want)
	}
	if !picture.Key || !bytes.Equal(picture.SPS, []byte{0x67, 0x42, 0, 0x1E}) || !bytes.Equal(picture.PPS, []byte{0x68, 0xCE}) {
		t.Fatalf("picture %+v", picture)
	}
	out, picture = Convert(nil, []byte{0, 0, 0, 1, 0x09, 0x30, 0, 0, 1, 0x41, 0x9A, 0x01})
	if !bytes.Equal(out, []byte{0, 0, 0, 3, 0x41, 0x9A, 0x01}) || picture.Key || picture.SPS != nil {
		t.Fatalf("ordinary picture: %X %+v", out, picture)
	}
}
