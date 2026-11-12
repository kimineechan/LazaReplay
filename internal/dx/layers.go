package dx

// Rect is a rectangle in desktop coordinates.
type Rect struct{ Left, Top, Right, Bottom int32 }

func (r Rect) Width() int  { return int(r.Right - r.Left) }
func (r Rect) Height() int { return int(r.Bottom - r.Top) }

// fit returns the largest rectangle of the input's shape inside the output,
// centred, on even coordinates.
func fit(inWidth, inHeight, outWidth, outHeight int) Rect {
	w, h := outWidth, outHeight
	if inWidth*outHeight > inHeight*outWidth {
		h = (inHeight*outWidth/inWidth + 1) &^ 1
	} else if inWidth*outHeight < inHeight*outWidth {
		w = (inWidth*outHeight/inHeight + 1) &^ 1
	}
	if w > outWidth {
		w = outWidth
	}
	if h > outHeight {
		h = outHeight
	}
	x, y := (outWidth-w)/2&^1, (outHeight-h)/2&^1
	return Rect{int32(x), int32(y), int32(x + w), int32(y + h)}
}

// Pointer shape types, as Windows reports them.
const (
	ShapeMonochrome  = 1 // one bit per pixel: an AND mask above an XOR mask
	ShapeColour      = 2 // four bytes per pixel with transparency
	ShapeMaskedColor = 4 // four bytes per pixel, the fourth choosing replace or XOR
)

// Shape is the picture of the mouse pointer as Windows reports it.
type Shape struct {
	Type          uint32
	Width, Height int // for a monochrome shape Height counts both masks
	Pitch         int // bytes per row
	Data          []byte
}

// Layers turns a pointer shape into two pictures of four bytes per pixel
// (blue, green, red, alpha) that are drawn one after the other.
//
// The first is painted over the screen through its alpha. The second
// inverts the screen wherever it is white, which is how the text cursor and
// some others stay visible on any background; Windows describes those with
// XOR masks. A layer that would be empty is returned as nil.
func Layers(s *Shape) (colour, invert []byte, width, height int) {
	if s == nil || s.Width <= 0 || s.Height <= 0 || s.Pitch <= 0 {
		return nil, nil, 0, 0
	}
	width, height = s.Width, s.Height
	if s.Type == ShapeMonochrome {
		height /= 2
	}
	rows := s.Height
	if len(s.Data) < rows*s.Pitch || height == 0 {
		return nil, nil, 0, 0
	}
	if (s.Type == ShapeMonochrome && s.Pitch*8 < width) || (s.Type != ShapeMonochrome && s.Pitch < width*4) {
		return nil, nil, 0, 0
	}
	colour, invert = make([]byte, width*height*4), make([]byte, width*height*4)
	usedColour, usedInvert := false, false
	set := func(layer []byte, at int, b, g, r, a byte) {
		layer[at], layer[at+1], layer[at+2], layer[at+3] = b, g, r, a
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			at := (y*width + x) * 4
			switch s.Type {
			case ShapeMonochrome:
				bit := byte(0x80) >> uint(x&7)
				and := s.Data[y*s.Pitch+x/8]&bit != 0
				xor := s.Data[(y+height)*s.Pitch+x/8]&bit != 0
				switch {
				case !and && !xor:
					set(colour, at, 0, 0, 0, 255)
					usedColour = true
				case !and && xor:
					set(colour, at, 255, 255, 255, 255)
					usedColour = true
				case and && xor:
					set(invert, at, 255, 255, 255, 255)
					usedInvert = true
				}
			case ShapeColour:
				p := s.Data[y*s.Pitch+x*4:]
				if p[3] != 0 {
					set(colour, at, p[0], p[1], p[2], p[3])
					usedColour = true
				}
			case ShapeMaskedColor:
				p := s.Data[y*s.Pitch+x*4:]
				if p[3] == 0 {
					set(colour, at, p[0], p[1], p[2], 255)
					usedColour = true
				} else if p[0]|p[1]|p[2] != 0 {
					set(invert, at, p[0], p[1], p[2], 255)
					usedInvert = true
				}
			}
		}
	}
	if !usedColour {
		colour = nil
	}
	if !usedInvert {
		invert = nil
	}
	return colour, invert, width, height
}
