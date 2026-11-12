// Package h264 reads just enough of an H.264 video stream to store it: it
// splits the stream into its units, tells keyframes from the rest, and reads
// the picture size and colour description from a sequence parameter set.
package h264

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// Unit types. A stream is a run of units; the low five bits of the first
// byte of each give its type.
const (
	UnitSlice    = 1  // part of an ordinary picture
	UnitKeySlice = 5  // part of a keyframe, which needs no earlier picture
	UnitSEI      = 6  // extra information
	UnitSPS      = 7  // sequence parameter set: size, profile, colour
	UnitPPS      = 8  // picture parameter set
	UnitAUD      = 9  // marks the start of a picture
	UnitFiller   = 12 // padding
)

var startCode = []byte{0, 0, 1}

// Split calls each for every unit of a stream in which units are divided by
// start codes (00 00 01), the form encoders produce. The slices handed to
// each point into stream.
func Split(stream []byte, each func(unit []byte)) {
	begin := -1
	for at := 0; ; {
		next := bytes.Index(stream[at:], startCode)
		end := len(stream)
		if next >= 0 {
			end = at + next
		}
		if begin >= 0 {
			unit := stream[begin:end]
			for len(unit) > 0 && unit[len(unit)-1] == 0 { // zeros before the next start code
				unit = unit[:len(unit)-1]
			}
			if len(unit) > 0 {
				each(unit)
			}
		}
		if next < 0 {
			return
		}
		begin = end + 3
		at = begin
	}
}

// Picture is what Convert learned about one encoded picture.
type Picture struct {
	Key      bool   // a keyframe
	SPS, PPS []byte // parameter sets carried with it, pointing into the input
}

// Convert appends one encoded picture to dst in the form MP4 files hold:
// each unit preceded by its length as four bytes instead of a start code.
// Parameter sets, picture markers and padding are left out, since an MP4
// file keeps parameter sets in its header.
func Convert(dst, stream []byte) ([]byte, Picture) {
	var p Picture
	Split(stream, func(unit []byte) {
		switch unit[0] & 0x1F {
		case UnitSPS:
			p.SPS = unit
			return
		case UnitPPS:
			p.PPS = unit
			return
		case UnitAUD, UnitFiller:
			return
		case UnitKeySlice:
			p.Key = true
		}
		dst = binary.BigEndian.AppendUint32(dst, uint32(len(unit)))
		dst = append(dst, unit...)
	})
	return dst, p
}

// SPS holds the fields of a sequence parameter set that matter here.
type SPS struct {
	Profile, Compatibility, Level int
	ChromaFormat                  int // 1 means 4:2:0
	BitDepthLuma, BitDepthChroma  int
	Width, Height                 int // visible picture, after cropping
	ReferenceFrames               int
	ColourDescribed, FullRange    bool
	Primaries, Transfer, Matrix   int
}

// bits reads a parameter set bit by bit. Inside a unit the bytes 00 00 03
// stand for 00 00; the reader skips the 03.
type bits struct {
	data  []byte
	pos   int // next bit
	zeros int
	err   error
}

func (b *bits) bit() int {
	index := b.pos >> 3
	if index >= len(b.data) {
		b.err = errors.New("h264: parameter set is cut short")
		return 0
	}
	if b.pos&7 == 0 {
		if b.zeros >= 2 && b.data[index] == 3 {
			b.zeros = 0
			b.pos += 8
			return b.bit()
		}
		if b.data[index] == 0 {
			b.zeros++
		} else {
			b.zeros = 0
		}
	}
	v := int(b.data[index]>>(7-uint(b.pos&7))) & 1
	b.pos++
	return v
}

func (b *bits) read(n int) int {
	v := 0
	for ; n > 0; n-- {
		v = v<<1 | b.bit()
	}
	return v
}

// ue reads an unsigned Exp-Golomb number: n zero bits, a one, then n bits.
func (b *bits) ue() int {
	n := 0
	for b.bit() == 0 {
		n++
		if n > 31 || b.err != nil {
			if b.err == nil {
				b.err = errors.New("h264: bad number in parameter set")
			}
			return 0
		}
	}
	return 1<<uint(n) - 1 + b.read(n)
}

// se reads a signed Exp-Golomb number.
func (b *bits) se() int {
	v := b.ue()
	if v&1 == 1 {
		return (v + 1) / 2
	}
	return -v / 2
}

// ParseSPS reads a sequence parameter set, given with its one-byte header.
func ParseSPS(unit []byte) (SPS, error) {
	var s SPS
	if len(unit) < 5 || unit[0]&0x1F != UnitSPS {
		return s, errors.New("h264: not a sequence parameter set")
	}
	s.Profile, s.Compatibility, s.Level = int(unit[1]), int(unit[2]), int(unit[3])
	s.ChromaFormat = 1
	b := &bits{data: unit[4:]}
	b.ue() // id
	switch s.Profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		s.ChromaFormat = b.ue()
		if s.ChromaFormat == 3 {
			b.bit() // separate colour planes
		}
		s.BitDepthLuma = b.ue()
		s.BitDepthChroma = b.ue()
		b.bit()           // transform bypass
		if b.bit() == 1 { // scaling matrices
			lists := 8
			if s.ChromaFormat == 3 {
				lists = 12
			}
			for i := 0; i < lists; i++ {
				if b.bit() == 0 {
					continue
				}
				size := 16
				if i >= 6 {
					size = 64
				}
				last, next := 8, 8
				for j := 0; j < size; j++ {
					if next != 0 {
						next = (last + b.se() + 256) % 256
					}
					if next != 0 {
						last = next
					}
				}
			}
		}
	}
	s.BitDepthLuma += 8
	s.BitDepthChroma += 8
	b.ue() // frame number bits
	switch b.ue() {
	case 0:
		b.ue()
	case 1:
		b.bit()
		b.se()
		b.se()
		for n := b.ue(); n > 0 && b.err == nil; n-- {
			b.se()
		}
	}
	s.ReferenceFrames = b.ue()
	b.bit() // gaps allowed
	widthMBs := b.ue() + 1
	heightUnits := b.ue() + 1
	frameOnly := b.bit()
	if frameOnly == 0 {
		b.bit() // adaptive frame/field
	}
	b.bit() // direct 8x8 inference
	s.Width = widthMBs * 16
	s.Height = (2 - frameOnly) * heightUnits * 16
	if b.bit() == 1 { // cropping
		left, right, top, bottom := b.ue(), b.ue(), b.ue(), b.ue()
		unitX, unitY := 1, 2-frameOnly
		switch s.ChromaFormat {
		case 1:
			unitX, unitY = 2, 2*(2-frameOnly)
		case 2:
			unitX = 2
		}
		s.Width -= unitX * (left + right)
		s.Height -= unitY * (top + bottom)
	}
	if b.bit() == 1 { // video usability information
		if b.bit() == 1 { // aspect ratio
			if b.read(8) == 255 {
				b.read(32)
			}
		}
		if b.bit() == 1 { // overscan
			b.bit()
		}
		if b.bit() == 1 { // video signal type
			b.read(3)
			s.FullRange = b.bit() == 1
			if b.bit() == 1 {
				s.ColourDescribed = true
				s.Primaries, s.Transfer, s.Matrix = b.read(8), b.read(8), b.read(8)
			}
		}
	}
	if b.err != nil {
		return s, b.err
	}
	if s.Width <= 0 || s.Height <= 0 {
		return s, errors.New("h264: parameter set gives no picture size")
	}
	return s, nil
}
