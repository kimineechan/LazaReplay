// Package testcard draws a picture in which every pixel is known, so that a
// recording of it can be checked by numbers instead of by eye.
//
// The card has eight flat colour bars, its own frame number written as
// sixteen black or white squares, and a white square that moves a little
// every frame.
package testcard

// Where the card sits in the picture, and how large it is.
const (
	X, Y          = 40, 40
	Width, Height = 640, 360

	barsHeight      = 240
	bitX, bitY      = 20, 252
	bitSize         = 20
	bitStep         = 24
	Bits            = 16
	blockY          = 300
	blockSize       = 40
	blockStep       = 4
	backgroundLevel = 128
	lampX, lampY    = 480, 246
	lampSize        = 32
)

// Bars are the colours of the bars from left to right, as red, green, blue.
var Bars = [8][3]byte{
	{255, 255, 255}, {255, 255, 0}, {0, 255, 255}, {0, 255, 0},
	{255, 0, 255}, {255, 0, 0}, {0, 0, 255}, {0, 0, 0},
}

// Paint draws the card for frame number n into a picture of four bytes per
// pixel (blue, green, red, alpha), stride bytes per row. With all false only
// the parts that change from frame to frame are drawn.
func Paint(pixels []byte, stride, n int, all bool) { PaintAt(pixels, stride, X, Y, n, all) }

// PaintAt is Paint with the card at a chosen place in the picture.
func PaintAt(pixels []byte, stride, left, top, n int, all bool) {
	fill := func(x0, y0, x1, y1 int, r, g, b byte) {
		for y := y0 + top; y < y1+top; y++ {
			row := pixels[y*stride+(x0+left)*4 : y*stride+(x1+left)*4]
			for i := 0; i < len(row); i += 4 {
				row[i], row[i+1], row[i+2], row[i+3] = b, g, r, 255
			}
		}
	}
	if all {
		for i, c := range Bars {
			fill(i*Width/8, 0, (i+1)*Width/8, barsHeight, c[0], c[1], c[2])
		}
	}
	fill(0, barsHeight, Width, Height, backgroundLevel, backgroundLevel, backgroundLevel)
	for bit := 0; bit < Bits; bit++ {
		level := byte(0)
		if n>>bit&1 == 1 {
			level = 255
		}
		x := bitX + bit*bitStep
		fill(x, bitY, x+bitSize, bitY+bitSize, level, level, level)
	}
	x := BlockX(n)
	fill(x, blockY, x+blockSize, blockY+blockSize, 255, 255, 255)
}

// Lamp draws the lamp of the card lit or dark. The self-test lights it for
// the first half of every second, and plays a click at the start of every
// second, to check that sound and picture stay in step.
func Lamp(pixels []byte, stride int, lit bool) {
	level := byte(0)
	if lit {
		level = 255
	}
	for y := Y + lampY; y < Y+lampY+lampSize; y++ {
		row := pixels[y*stride+(X+lampX)*4 : y*stride+(X+lampX+lampSize)*4]
		for i := 0; i < len(row); i += 4 {
			row[i], row[i+1], row[i+2], row[i+3] = level, level, level, 255
		}
	}
}

// LampCentre returns the middle of the lamp in picture coordinates.
func LampCentre() (x, y int) { return X + lampX + lampSize/2, Y + lampY + lampSize/2 }

// BlockX returns where the moving square is in frame n, relative to the card.
func BlockX(n int) int { return n * blockStep % (Width - blockSize) }

// Luma reads the brightness of a decoded picture at a point of the card.
type Luma func(x, y int) int

// Number reads the frame number back from a decoded picture. Brightness
// runs from 16 for black to 235 for white. ok is false when a square is
// neither clearly black nor clearly white, which means the picture is not
// the card or is badly damaged.
func Number(luma Luma) (n int, ok bool) {
	for bit := 0; bit < Bits; bit++ {
		v := luma(X+bitX+bit*bitStep+bitSize/2, Y+bitY+bitSize/2)
		switch {
		case v > 190:
			n |= 1 << bit
		case v > 60:
			return 0, false
		}
	}
	return n, true
}

// Colour is a colour as video stores it: brightness and two colour
// differences, each 0 to 255.
type Colour struct{ Y, Cb, Cr float64 }

// FromRGB returns what a screen colour should measure in video that follows
// the BT.709 standard at studio range, the form the recorder produces.
func FromRGB(red, green, blue float64) Colour {
	r, g, b := red/255, green/255, blue/255
	luma := 0.2126*r + 0.7152*g + 0.0722*b
	return Colour{16 + 219*luma, 128 + 224*(b-luma)/1.8556, 128 + 224*(r-luma)/1.5748}
}

// Expected returns what bar i should measure.
func Expected(i int) Colour {
	return FromRGB(float64(Bars[i][0]), float64(Bars[i][1]), float64(Bars[i][2]))
}

// Distance returns the largest difference between two colours on any of
// their three values.
func (c Colour) Distance(d Colour) float64 {
	worst := 0.0
	for _, v := range [3]float64{c.Y - d.Y, c.Cb - d.Cb, c.Cr - d.Cr} {
		if v < 0 {
			v = -v
		}
		if v > worst {
			worst = v
		}
	}
	return worst
}

// BarCentre returns the middle of bar i in picture coordinates.
func BarCentre(i int) (x, y int) {
	return X + i*Width/8 + Width/16, Y + barsHeight/2
}

// BlockCentre returns the middle of the moving square in frame n.
func BlockCentre(n int) (x, y int) {
	return X + BlockX(n) + blockSize/2, Y + blockY + blockSize/2
}
