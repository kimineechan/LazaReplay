//go:build windows

package testcard

import (
	"unsafe"

	"lazareplay/internal/com"
	"lazareplay/internal/dx"
)

// Source stands in for the screen. Each time a picture is asked for it
// supplies the next frame of the card on a dark background, so a recording
// of it must contain every frame number exactly once and in order.
type Source struct {
	device  *dx.Device
	texture unsafe.Pointer
	pixels  []byte
	width   int
	height  int
	next    int
	pointer dx.Pointer

	// Still makes the source stop changing after this many frames, to
	// exercise what the recorder does with a screen that stands still.
	Still int
}

// NewSource makes a source of the given picture size.
func NewSource(device *dx.Device, width, height int) (*Source, error) {
	s := &Source{device: device, width: width, height: height, pixels: make([]byte, width*height*4)}
	for i := 0; i < len(s.pixels); i += 4 {
		s.pixels[i], s.pixels[i+1], s.pixels[i+2], s.pixels[i+3] = 40, 40, 40, 255
	}
	Paint(s.pixels, width*4, 0, true)
	var err error
	s.texture, err = device.NewTexture(width, height, dx.FormatBGRA, dx.UsageDefault, dx.BindRenderTarget|dx.BindShaderResource, 0, s.pixels, width*4)
	return s, err
}

// SetPointer gives the source a mouse pointer to report.
func (s *Source) SetPointer(p dx.Pointer) { s.pointer = p }

func (s *Source) Size() (width, height int) { return s.width, s.height }

// Acquire draws the next frame. It never waits.
func (s *Source) Acquire(wait uint32) (bool, error) {
	if s.Still > 0 && s.next >= s.Still {
		return false, nil
	}
	Paint(s.pixels, s.width*4, s.next, false)
	s.device.Update(s.texture, s.pixels, s.width*4)
	s.next++
	return true, nil
}

// Frame returns the number of the frame drawn last.
func (s *Source) Frame() int { return s.next - 1 }

func (s *Source) Texture() unsafe.Pointer { return s.texture }
func (s *Source) Pointer() dx.Pointer     { return s.pointer }
func (s *Source) Close()                  { com.Release(&s.texture) }
